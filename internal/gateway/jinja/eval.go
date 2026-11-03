package jinja

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

var (
	errBreak    = errors.New("break")
	errContinue = errors.New("continue")
)

// The state of one render, owned by a single goroutine
type renderer struct {
	clock      func() time.Time
	autoescape bool
	buffer     Value
	root       *scope
	// Python frames the equivalent jinja2 render would have on its stack, and the deepest it has reached
	frames, peak int
	// Bytes charged against the allocation budget so far
	allocated int
	// The most frames each measured operation key pushed, recorded only when non nil
	costs map[string]int
	// Whether a model is measuring frames ahead of time, recursion limits not applying
	probing bool
}

// CPython's default recursion limit and the frames generate.py's render call holds below jinja2's root function
const (
	recursionLimit = 1000
	rootFrames     = 6
)

// Python frames jinja2 pushes per macro call, macro body, recursive loop call and generation block
const (
	macroCallFrames  = 3
	macroBodyFrames  = 2
	loopCallFrames   = 4
	generationFrames = 6
)

// Pushes Python frames, failing with RecursionError once the stack would pass the recursion limit
func (r *renderer) enter(n int) error {
	if r.frames+n > recursionLimit && !r.probing {
		return errorf("RecursionError", "maximum recursion depth exceeded")
	}
	r.frames += n
	r.peak = max(r.peak, r.frames)
	return nil
}

func (r *renderer) leave(n int) { r.frames -= n }

type scope struct {
	vars   map[string]Value
	parent *scope
}

func newScope(parent *scope) *scope { return &scope{vars: map[string]Value{}, parent: parent} }

func (s *scope) lookup(name string) (Value, bool) {
	for c := s; c != nil; c = c.parent {
		if v, ok := c.vars[name]; ok {
			return v, true
		}
	}
	return nil, false
}

// Renders a parsed template with the given top level variables over the global functions
func render(body []stmt, vars map[string]Value, clock func() time.Time) (string, error) {
	out, _, err := renderTracked(body, vars, clock)
	return out, err
}

// Renders like render and also reports the deepest Python frame the equivalent jinja2 render reaches
func renderTracked(body []stmt, vars map[string]Value, clock func() time.Time) (string, int, error) {
	r := &renderer{clock: clock, frames: rootFrames, peak: rootFrames}
	out, err := r.renderBody(body, vars)
	return out, r.peak, err
}

// Runs a parsed template's body with the given top level variables over the global functions
func (r *renderer) renderBody(body []stmt, vars map[string]Value) (string, error) {
	globals := newScope(nil)
	for k, v := range r.globals() {
		globals.vars[k] = v
	}
	r.root = newScope(globals)
	for k, v := range vars {
		r.root.vars[k] = v
	}
	var w strings.Builder
	if err := r.exec(body, r.root, &w); err != nil {
		return "", err
	}
	return w.String(), nil
}

// Writes a value as jinja2 prints it, escaping unless it is Markup when autoescape is on
func (r *renderer) emit(w *strings.Builder, v Value) error {
	s, err := r.text(v)
	if err == nil {
		err = r.charge(len(s))
	}
	if err != nil {
		return err
	}
	w.WriteString(s)
	return nil
}

// A value as output text, escaped unless it is Markup when autoescape is on
func (r *renderer) text(v Value) (string, error) {
	if r.autoescape {
		m, err := escapeValue(v)
		return string(m), err
	}
	return pyStr(v)
}

// markupsafe's escape, Markup kept as is and anything else escaped from its str()
func escapeValue(v Value) (Markup, error) {
	if m, ok := v.(Markup); ok {
		return m, nil
	}
	s, err := pyStr(v)
	return Markup(htmlEscape(s)), err
}

// Wraps rendered text as jinja2 does for blocks, Markup under autoescape and str otherwise
func (r *renderer) wrapRendered(s string) Value {
	if r.autoescape {
		return Markup(s)
	}
	return s
}

func (r *renderer) exec(body []stmt, sc *scope, w *strings.Builder) error {
	for _, s := range body {
		if err := r.execStmt(s, sc, w); err != nil {
			return err
		}
	}
	return nil
}

func (r *renderer) execStmt(s stmt, sc *scope, w *strings.Builder) error {
	switch n := s.(type) {
	case *textStmt:
		if err := r.charge(len(n.text)); err != nil {
			return err
		}
		w.WriteString(n.text)
	case *outputStmt:
		var v Value
		var err error
		if c, folded := n.expr.(*constExpr); folded {
			v = c.v
		} else if v, err = r.eval(n.expr, sc); err != nil {
			return err
		}
		if c, folded := n.expr.(*constExpr); !folded || !isMarkup(c.v) {
			var err error
			if r.autoescape {
				key := "escape " + keyType(v)
				err = r.measured(key, r.opFrames(key, v, nil, nil))
			} else {
				err = r.transient(strFrames(v))
			}
			if err != nil {
				return err
			}
		}
		return r.emit(w, v)
	case *ifStmt:
		return r.execIf(n, sc, w)
	case *forStmt:
		v, err := r.eval(n.iter, sc)
		if err != nil {
			return err
		}
		if !n.recursive {
			return r.runFor(n, sc, w, v, 0)
		}
		if err := r.enter(1); err != nil {
			return err
		}
		defer r.leave(1)
		return r.runFor(n, sc, w, v, 0)
	case *setStmt:
		if err := checkNamespaces(n.target, sc, map[string]bool{}); err != nil {
			return err
		}
		v, err := r.eval(n.value, sc)
		if err != nil {
			return err
		}
		return r.assign(n.target, v, sc)
	case *setBlockStmt:
		var buf strings.Builder
		if err := r.exec(n.body, newScope(sc), &buf); err != nil {
			return err
		}
		v := r.wrapRendered(buf.String())
		if n.filter != nil {
			var err error
			if v, err = r.withBuffer(v, func() (Value, error) { return r.eval(n.filter, sc) }); err != nil {
				return err
			}
		}
		return r.assign(n.target, v, sc)
	case *macroStmt:
		sc.vars[n.name] = &Macro{name: n.name, sig: n.sig, closure: sc}
	case *callBlockStmt:
		caller := &Macro{anonymous: true, sig: n.sig, closure: sc}
		fn, err := r.eval(n.call.fn, sc)
		if err != nil {
			return err
		}
		args, kw, err := r.evalArgs(n.call.args, sc)
		if err != nil {
			return err
		}
		if _, dup := kw.lookup("caller"); dup {
			return typeError("got multiple values for keyword argument 'caller'")
		}
		kw.put("caller", caller)
		v, err := r.call(fn, args, kw)
		if err != nil {
			return err
		}
		return r.emit(w, v)
	case *filterBlockStmt:
		var buf strings.Builder
		if err := r.exec(n.body, newScope(sc), &buf); err != nil {
			return err
		}
		v, err := r.withBuffer(r.wrapRendered(buf.String()), func() (Value, error) { return r.eval(n.filter, sc) })
		if err != nil {
			return err
		}
		return r.emit(w, v)
	case *withStmt:
		inner := newScope(sc)
		for i, t := range n.targets {
			v, err := r.eval(n.values[i], sc)
			if err != nil {
				return err
			}
			if err := r.assign(t, v, inner); err != nil {
				return err
			}
		}
		return r.exec(n.body, inner, w)
	case *blockStmt:
		parent := r.root
		if n.scoped {
			parent = sc
		}
		if err := r.enter(1); err != nil {
			return err
		}
		saved := r.autoescape
		r.autoescape = false
		err := r.exec(n.body, newScope(parent), w)
		r.autoescape = saved
		r.leave(1)
		return err
	case *bindStmt:
		sc.vars[n.name] = n.value
	case *autoescapeStmt:
		v, err := r.eval(n.value, sc)
		if err != nil {
			return err
		}
		saved := r.autoescape
		r.autoescape = truthy(v)
		err = r.exec(n.body, newScope(sc), w)
		r.autoescape = saved
		return err
	case *loaderStmt:
		v, err := r.eval(n.template, sc)
		if err != nil {
			return err
		}
		if n.tag != "include" {
			return errNoLoader()
		}
		err = selectTemplate(n.loadFunc, v)
		var te *Error
		if n.ignoreMissing && errors.As(err, &te) && te.Kind == "TemplatesNotFound" {
			return nil
		}
		return err
	case *generationStmt:
		if err := r.enter(generationFrames); err != nil {
			return err
		}
		var buf strings.Builder
		err := r.exec(n.body, newScope(sc), &buf)
		r.leave(generationFrames)
		if err != nil {
			return err
		}
		return r.emit(w, r.wrapRendered(buf.String()))
	case *breakStmt:
		return errBreak
	case *continueStmt:
		return errContinue
	default:
		return fmt.Errorf("unknown statement %T", s)
	}
	return nil
}

// The TypeError Environment._load_template raises with no loader configured
func errNoLoader() error { return typeError("no loader for this environment specified") }

// The error get_template, get_or_select_template or select_template raises for a template value without a loader
func selectTemplate(fn string, v Value) error {
	if fn == "get_template" {
		return errNoLoader()
	}
	if fn == "get_or_select_template" {
		switch v.(type) {
		case string, Markup, *Undefined:
			return errNoLoader()
		}
	}
	if u, ok := v.(*Undefined); ok {
		return u.fail()
	}
	if !truthy(v) {
		return &Error{Kind: "TemplatesNotFound", Msg: "Tried to select from an empty list of templates."}
	}
	switch x := v.(type) {
	case *Iterator:
		_, ok, err := x.pull()
		if err != nil {
			return err
		}
		if !ok {
			return typeError("%s object is not subscriptable", strRepr(typeName(v)))
		}
		return errNoLoader()
	case *List, *Tuple, string, Markup, *Dict, *Range, *DictView:
		return errNoLoader()
	}
	if _, err := iterValues(v); err != nil {
		return err
	}
	return errNoLoader()
}

// Evaluates a filter chain whose innermost value is rendered block text
func (r *renderer) withBuffer(v Value, f func() (Value, error)) (Value, error) {
	saved := r.buffer
	r.buffer = v
	out, err := f()
	r.buffer = saved
	return out, err
}

func (r *renderer) execIf(n *ifStmt, sc *scope, w *strings.Builder) error {
	branches := append([]*ifStmt{n}, n.elifs...)
	for _, b := range branches {
		v, err := r.eval(b.test, sc)
		if err != nil {
			return err
		}
		if ok, err := r.truth(v); err != nil || ok {
			if err != nil {
				return err
			}
			return r.exec(b.body, sc, w)
		}
	}
	return r.exec(n.orelse, sc, w)
}

// Binds a value to a name, a namespace attribute or an unpacked tuple of targets
// Refuses a set whose dotted targets name anything but a namespace, before its value is evaluated
func checkNamespaces(target expr, sc *scope, seen map[string]bool) error {
	switch t := target.(type) {
	case *nsRefExpr:
		if seen[t.name] {
			return nil
		}
		seen[t.name] = true
		if obj, _ := sc.lookup(t.name); obj != nil {
			if _, ok := obj.(*Namespace); ok {
				return nil
			}
		}
		return errorf("TemplateRuntimeError", "cannot assign attribute on non-namespace object")
	case *tupleExpr:
		for _, item := range t.items {
			if err := checkNamespaces(item, sc, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *renderer) assign(target expr, v Value, sc *scope) error {
	switch t := target.(type) {
	case *nameExpr:
		sc.vars[t.name] = v
		return nil
	case *nsRefExpr:
		obj, ok := sc.lookup(t.name)
		if !ok {
			return typeError("'_MissingType' object does not support item assignment")
		}
		switch x := obj.(type) {
		case *Namespace:
			x.attrs.put(t.attr, v)
		case *Dict:
			x.put(t.attr, v)
		case *List:
			return typeError("list indices must be integers or slices, not str")
		default:
			return typeError("'%s' object does not support item assignment", typeName(obj))
		}
		return nil
	case *tupleExpr:
		items, err := unpack(v, len(t.items))
		if err != nil {
			return err
		}
		for i, sub := range t.items {
			if err := r.assign(sub, items[i], sc); err != nil {
				return err
			}
		}
		return nil
	}
	return errorf("TemplateSyntaxError", "cannot assign to %T", target)
}

// Evaluates an expression
// Evaluates an expression, charging what operators, calls and filters build to the allocation budget
func (r *renderer) eval(e expr, sc *scope) (Value, error) {
	v, err := r.evalNode(e, sc)
	if err != nil {
		return v, err
	}
	switch e.(type) {
	case *binExpr, *concatExpr, *callExpr, *filterExpr:
		if err := r.chargeValue(v); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func (r *renderer) evalNode(e expr, sc *scope) (Value, error) {
	switch x := e.(type) {
	case *constExpr:
		if containsMarkup(x.v) {
			if err := r.transient(1); err != nil {
				return nil, err
			}
		}
		if x.fresh {
			return freshCopy(x.v), nil
		}
		return x.v, nil
	case *nameExpr:
		if v, ok := sc.lookup(x.name); ok {
			return v, nil
		}
		if err := r.measured("undefined", r.opFrames("undefined", nil, nil, nil)); err != nil {
			return nil, err
		}
		return undefinedName(x.name), nil
	case *tupleExpr:
		items, err := r.evalList(x.items, sc)
		return &Tuple{items: items}, err
	case *listExpr:
		items, err := r.evalList(x.items, sc)
		return &List{items: items}, err
	case *dictExpr:
		d := newDict()
		for i := range x.keys {
			k, err := r.eval(x.keys[i], sc)
			if err != nil {
				return nil, err
			}
			v, err := r.eval(x.vals[i], sc)
			if err != nil {
				return nil, err
			}
			if err := d.set(k, v); err != nil {
				return nil, err
			}
		}
		return d, nil
	case *getattrExpr:
		obj, err := r.eval(x.obj, sc)
		if err != nil {
			return nil, err
		}
		return r.getattr(obj, x.attr)
	case *getitemExpr:
		obj, err := r.eval(x.obj, sc)
		if err != nil {
			return nil, err
		}
		key, err := r.eval(x.arg, sc)
		if err != nil {
			return nil, err
		}
		if s, ok := key.(*sliceValue); ok {
			if u, isU := obj.(*Undefined); isU {
				return nil, u.fail()
			}
			return sliceOf(obj, s)
		}
		return r.getitem(obj, key)
	case *sliceExpr:
		var parts [3]Value
		for i, p := range []expr{x.start, x.stop, x.step} {
			parts[i] = none
			if p != nil {
				v, err := r.eval(p, sc)
				if err != nil {
					return nil, err
				}
				parts[i] = v
			}
		}
		return &sliceValue{start: parts[0], stop: parts[1], step: parts[2]}, nil
	case *callExpr:
		fn, err := r.eval(x.fn, sc)
		if err != nil {
			return nil, err
		}
		args, kw, err := r.evalArgs(x.args, sc)
		if err != nil {
			return nil, err
		}
		return r.call(fn, args, kw)
	case *filterExpr:
		v, err := r.eval(x.node, sc)
		if err != nil {
			return nil, err
		}
		args, kw, err := r.evalArgs(x.args, sc)
		if err != nil {
			return nil, err
		}
		if _, ok := builtinFilters[x.name]; !ok {
			return nil, errorf("TemplateRuntimeError", "No filter named %s found.", strRepr(x.name))
		}
		return r.callFilter(x.name, v, args, kw)
	case *testExpr:
		v, err := r.eval(x.node, sc)
		if err != nil {
			return nil, err
		}
		args, kw, err := r.evalArgs(x.args, sc)
		if err != nil {
			return nil, err
		}
		if _, ok := builtinTests[x.name]; !ok {
			return nil, errorf("TemplateRuntimeError", "No test named %s found.", strRepr(x.name))
		}
		ok, err := r.callTest(x.name, v, args, kw)
		return ok, err
	case *bufferExpr:
		return r.buffer, nil
	case *nameErrorExpr:
		return nil, x.err()
	case *condExpr:
		t, err := r.eval(x.test, sc)
		if err != nil {
			return nil, err
		}
		if ok, err := r.truth(t); err != nil || ok {
			if err != nil {
				return nil, err
			}
			return r.eval(x.expr1, sc)
		}
		if x.expr2 != nil {
			return r.eval(x.expr2, sc)
		}
		return &Undefined{hint: fmt.Sprintf("the inline if-expression on line %d evaluated to false and no else section was defined.", x.line)}, nil
	case *andExpr:
		l, err := r.eval(x.left, sc)
		if err != nil {
			return nil, err
		}
		if ok, err := r.truth(l); err != nil || !ok {
			return l, err
		}
		return r.eval(x.right, sc)
	case *orExpr:
		l, err := r.eval(x.left, sc)
		if err != nil {
			return nil, err
		}
		if ok, err := r.truth(l); err != nil || ok {
			return l, err
		}
		return r.eval(x.right, sc)
	case *notExpr:
		v, err := r.eval(x.e, sc)
		if err != nil {
			return nil, err
		}
		ok, err := r.truth(v)
		return !ok, err
	case *negExpr:
		v, err := r.eval(x.e, sc)
		if err != nil {
			return nil, err
		}
		return unaryOp("-", v)
	case *posExpr:
		v, err := r.eval(x.e, sc)
		if err != nil {
			return nil, err
		}
		return unaryOp("+", v)
	case *binExpr:
		l, err := r.eval(x.left, sc)
		if err != nil {
			return nil, err
		}
		rv, err := r.eval(x.right, sc)
		if err != nil {
			return nil, err
		}
		return binaryOp(x.op, l, rv)
	case *concatExpr:
		vals, err := r.evalList(x.items, sc)
		if err != nil {
			return nil, err
		}
		join := "concat str_join"
		if r.autoescape {
			join = "concat markup_join"
		}
		seq := &Tuple{items: vals}
		if err := r.measured(join, r.opFrames(join, seq, nil, nil)); err != nil {
			return nil, err
		}
		var b strings.Builder
		for _, v := range vals {
			s, err := r.text(v)
			if err == nil {
				err = checkBytes(b.Len() + len(s))
			}
			if err != nil {
				return nil, err
			}
			b.WriteString(s)
		}
		return r.wrapRendered(b.String()), nil
	case *compareExpr:
		left, err := r.eval(x.first, sc)
		if err != nil {
			return nil, err
		}
		for i, op := range x.ops {
			right, err := r.eval(x.operands[i], sc)
			if err != nil {
				return nil, err
			}
			ok, err := compareOp(op, left, right)
			if err != nil {
				return nil, err
			}
			if !ok {
				return false, nil
			}
			left = right
		}
		return true, nil
	}
	return nil, fmt.Errorf("unknown expression %T", e)
}

func (r *renderer) evalList(es []expr, sc *scope) ([]Value, error) {
	out := make([]Value, len(es))
	for i, e := range es {
		v, err := r.eval(e, sc)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// Evaluates call arguments, expanding star and double star arguments as Python does
func (r *renderer) evalArgs(ca callArgs, sc *scope) ([]Value, *Dict, error) {
	args, err := r.evalList(ca.args, sc)
	if err != nil {
		return nil, nil, err
	}
	kw := newDict()
	for i, name := range ca.kwNames {
		v, err := r.eval(ca.kwVals[i], sc)
		if err != nil {
			return nil, nil, err
		}
		kw.put(name, v)
	}
	if ca.dynArgs != nil {
		v, err := r.eval(ca.dynArgs, sc)
		if err != nil {
			return nil, nil, err
		}
		extra, err := iterValues(v)
		if err != nil {
			return nil, nil, typeError("argument after * must be an iterable, not %s", typeName(v))
		}
		args = append(args, extra...)
	}
	if ca.dynKwargs != nil {
		v, err := r.eval(ca.dynKwargs, sc)
		if err != nil {
			return nil, nil, err
		}
		d, ok := v.(*Dict)
		if !ok {
			return nil, nil, typeError("argument after ** must be a mapping, not %s", typeName(v))
		}
		for i, k := range d.keys {
			ks, ok := k.(string)
			if !ok {
				return nil, nil, typeError("keywords must be strings")
			}
			if _, dup := kw.lookup(ks); dup {
				return nil, nil, typeError("got multiple values for keyword argument %s", strRepr(ks))
			}
			kw.put(ks, d.vals[i])
		}
	}
	return args, kw, nil
}

// Calls a callable value through the sandbox
func (r *renderer) call(fn Value, args []Value, kw *Dict) (Value, error) {
	if key := callKey(fn); key != "" {
		if err := r.measured(key, r.opFrames(key, callSubject(fn), args, kw)); err != nil {
			return nil, err
		}
	}
	switch f := fn.(type) {
	case *Undefined:
		return nil, f.fail()
	case *Builtin:
		return f.fn(r, args, kw)
	case *Macro:
		return r.callMacro(f, args, kw)
	case *BlockRef:
		return r.callBlockRef(f, args, kw)
	case *LoopContext:
		if f.recurse == nil {
			return nil, typeError("The loop must have the 'recursive' marker to be called recursively.")
		}
		v, err := bindArgs("LoopContext.__call__", []param{{name: "iterable"}}, args, kw)
		if err != nil {
			return nil, err
		}
		return f.recurse(v[0])
	case *Joiner:
		if len(args) > 0 || len(kw.keys) > 0 {
			return nil, typeError("Joiner.__call__() takes 1 positional argument but %d were given", len(args)+1)
		}
		if !f.used {
			f.used = true
			return "", nil
		}
		return f.sep, nil
	}
	return nil, typeError("%s object is not callable", strRepr(typeName(fn)))
}

// A user defined macro or a call block's caller bound to its defining scope, a caller having no name as jinja2 gives it None
type Macro struct {
	name      string
	anonymous bool
	sig       *signature
	closure   *scope
}

// The repr of the macro's name, None for a call block's caller
func (m *Macro) nameRepr() string {
	if m.anonymous {
		return "None"
	}
	return strRepr(m.name)
}

// Calls a macro with jinja2's rules for defaults, caller, varargs and kwargs
func (r *renderer) callMacro(m *Macro, args []Value, kw *Dict) (Value, error) {
	if err := r.enter(macroCallFrames); err != nil {
		return nil, err
	}
	defer r.leave(macroCallFrames)
	if err := r.charge(scopeBytes); err != nil {
		return nil, err
	}
	kw = kw.copy()
	n := len(m.sig.params)
	values := make([]Value, n)
	given := make([]bool, n)
	for i := 0; i < n && i < len(args); i++ {
		values[i], given[i] = args[i], true
	}
	foundCaller := false
	if len(args) < n {
		for i := len(args); i < n; i++ {
			name := m.sig.params[i]
			if v, ok := kw.lookup(name); ok {
				values[i], given[i] = v, true
				kw = withoutKey(kw, name)
			}
			if name == "caller" {
				foundCaller = true
			}
		}
	} else {
		foundCaller = m.sig.explicitCaller
	}
	sc := newScope(m.closure)
	// An explicit caller parameter the positional arguments skipped makes jinja2 pass the caller as one argument too many
	extraArg := false
	if m.sig.usesCaller && !foundCaller {
		caller, ok := kw.lookup("caller")
		if ok {
			kw = withoutKey(kw, "caller")
		}
		if _, isNone := caller.(NoneType); !ok || isNone {
			caller = &Undefined{hint: "No caller defined", name: "caller"}
		}
		if m.sig.explicitCaller {
			extraArg = true
		} else {
			sc.vars["caller"] = caller
		}
	}
	if m.sig.usesKwargs {
		sc.vars["kwargs"] = kw
	} else if len(kw.keys) > 0 {
		if _, ok := kw.lookup("caller"); ok {
			return nil, typeError("macro %s was invoked with two values for the special caller argument. This is most likely a bug.", m.nameRepr())
		}
		return nil, typeError("macro %s takes no keyword argument %s", m.nameRepr(), strRepr(kw.keys[0].(string)))
	}
	if m.sig.usesVarargs {
		var extra []Value
		if len(args) > n {
			extra = append(extra, args[n:]...)
		}
		sc.vars["varargs"] = &Tuple{items: extra}
	} else if len(args) > n {
		return nil, typeError("macro %s takes not more than %d argument(s)", m.nameRepr(), n)
	}
	if extraArg {
		accepted := n
		if m.sig.usesKwargs {
			accepted++
		}
		if m.sig.usesVarargs {
			accepted++
		}
		plural := "s"
		if accepted == 1 {
			plural = ""
		}
		return nil, typeError("%s() takes %d positional argument%s but %d were given", m.sig.qualname, accepted, plural, accepted+1)
	}
	if err := r.enter(macroBodyFrames); err != nil {
		return nil, err
	}
	defer r.leave(macroBodyFrames)
	callEscape := r.autoescape
	if !m.sig.volatile {
		r.autoescape = m.sig.escape
	}
	defer func() { r.autoescape = callEscape }()
	firstDefault := n - len(m.sig.defaults)
	for i, name := range m.sig.params {
		if given[i] {
			sc.vars[name] = values[i]
			continue
		}
		if i >= firstDefault {
			v, err := r.eval(m.sig.defaults[i-firstDefault], sc)
			if err != nil {
				return nil, err
			}
			sc.vars[name] = v
			continue
		}
		sc.vars[name] = &Undefined{hint: fmt.Sprintf("parameter %s was not provided", strRepr(name)), name: name}
	}
	var buf strings.Builder
	if err := r.exec(m.sig.body, sc, &buf); err != nil {
		return nil, err
	}
	if callEscape {
		return Markup(buf.String()), nil
	}
	return buf.String(), nil
}

func withoutKey(d *Dict, key string) *Dict {
	out := newDict()
	for i, k := range d.keys {
		if k != key {
			_ = out.set(k, d.vals[i])
		}
	}
	return out
}

// A builtin callable, a bound method when self is set
type Builtin struct {
	name string
	self Value
	fn   func(r *renderer, args []Value, kw *Dict) (Value, error)
	// The qualified Python class name when the callable is a class, and the type name of its instances
	class, instance string
}

// One parameter of a builtin's Python signature
type param struct {
	name    string
	def     Value
	hasDef  bool
	posOnly bool
}

func opt(name string, def Value) param { return param{name: name, def: def, hasDef: true} }

// Binds positional and keyword arguments to a Python signature, failing as Python does
func bindArgs(fname string, params []param, args []Value, kw *Dict) ([]Value, error) {
	if len(args) > len(params) {
		return nil, typeError("%s() takes %s but %d were given", fname, positionalCount(params), len(args))
	}
	out := make([]Value, len(params))
	set := make([]bool, len(params))
	for i, a := range args {
		out[i], set[i] = a, true
	}
	for i, k := range kw.keys {
		name := k.(string)
		idx := -1
		for j, p := range params {
			if p.name == name && !p.posOnly {
				idx = j
			}
		}
		if idx < 0 {
			for _, p := range params {
				if p.name == name && p.posOnly {
					return nil, typeError("%s() got some positional-only arguments passed as keyword arguments: %s", fname, strRepr(name))
				}
			}
			return nil, typeError("%s() got an unexpected keyword argument %s", fname, strRepr(name))
		}
		if set[idx] {
			return nil, typeError("%s() got multiple values for argument %s", fname, strRepr(name))
		}
		out[idx], set[idx] = kw.vals[i], true
	}
	for i, p := range params {
		if set[i] {
			continue
		}
		if !p.hasDef {
			return nil, typeError("%s() missing 1 required positional argument: %s", fname, strRepr(p.name))
		}
		out[i] = p.def
	}
	return out, nil
}

func positionalCount(params []param) string {
	required := 0
	for _, p := range params {
		if !p.hasDef {
			required++
		}
	}
	if required == len(params) {
		if required == 1 {
			return "1 positional argument"
		}
		return fmt.Sprintf("%d positional arguments", required)
	}
	return fmt.Sprintf("from %d to %d positional arguments", required, len(params))
}

// A Python slice object built by subscript syntax
type sliceValue struct{ start, stop, step Value }

// Resolves slice bounds for a sequence length as slice.indices does
func (s *sliceValue) indices(n int) (int, int, int, error) {
	idx := func(v Value) (*big.Int, bool, error) {
		if _, ok := v.(NoneType); ok {
			return nil, false, nil
		}
		if !isIntegral(v) {
			return nil, false, typeError("slice indices must be integers or None or have an __index__ method")
		}
		return toBig(v), true, nil
	}
	stepB, hasStep, err := idx(s.step)
	if err != nil {
		return 0, 0, 0, err
	}
	step := 1
	if hasStep {
		if stepB.Sign() == 0 {
			return 0, 0, 0, errorf("ValueError", "slice step cannot be zero")
		}
		step = clampInt(stepB, n)
	}
	clamp := func(v Value, def int) (int, error) {
		b, has, err := idx(v)
		if err != nil || !has {
			return def, err
		}
		i := clampInt(b, n)
		if i < 0 {
			i += n
			if i < 0 {
				if step < 0 {
					return -1, nil
				}
				return 0, nil
			}
		} else if i >= n {
			if step < 0 {
				return n - 1, nil
			}
			return n, nil
		}
		return i, nil
	}
	defStart, defStop := 0, n
	if step < 0 {
		defStart, defStop = n-1, -1
	}
	start, err := clamp(s.start, defStart)
	if err != nil {
		return 0, 0, 0, err
	}
	stop, err := clamp(s.stop, defStop)
	if err != nil {
		return 0, 0, 0, err
	}
	return start, stop, step, nil
}

// A big integer clamped to a range wide enough that slicing a sequence of length n is unaffected
func clampInt(b *big.Int, n int) int {
	limit := int64(n) + 1
	if b.Cmp(big.NewInt(limit)) > 0 {
		return int(limit)
	}
	if b.Cmp(big.NewInt(-limit)) < 0 {
		return int(-limit)
	}
	return int(b.Int64())
}

// The positions a slice selects from a sequence of length n
func (s *sliceValue) positions(n int) ([]int, error) {
	start, stop, step, err := s.indices(n)
	if err != nil {
		return nil, err
	}
	var out []int
	if step > 0 {
		for i := start; i < stop; i += step {
			out = append(out, i)
		}
	} else {
		for i := start; i > stop; i += step {
			out = append(out, i)
		}
	}
	return out, nil
}

// Whether a constant holds Markup, which the generated module rebuilds through Markup.__new__ each time it runs
func containsMarkup(v Value) bool {
	switch x := v.(type) {
	case Markup:
		return true
	case *List:
		return anyMarkup(x.items)
	case *Tuple:
		return anyMarkup(x.items)
	case *Dict:
		return anyMarkup(x.keys) || anyMarkup(x.vals)
	}
	return false
}

func anyMarkup(items []Value) bool {
	for _, it := range items {
		if containsMarkup(it) {
			return true
		}
	}
	return false
}
