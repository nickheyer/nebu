package jinja

import (
	"sort"
	"strconv"
	"strings"
)

// A Python exception raised while jinja2 generates code, unwinding to the frame that catches it
type pyRaise struct{ err error }

// jinja2's CodeGenerator for transformers' sandboxed environment, writing the module source and raising its compile time errors
type codegen struct {
	py *pyStack
	// Folds constants, its renderer holding the shared eval context's autoescape and the folder its volatility
	f *folder
	// The module source with str and Markup constants held aside as marks, and the writer state CodeGenerator keeps
	out          strings.Builder
	lits         []litMark
	codeLineno   int
	debug        [][2]int
	debugPending bool
	debugLine    int
	newLines     int
	lastLine     int
	firstWrite   bool
	lastIdent    int
	indentation  int
	assignStack  []map[string]bool
	paramDefs    []map[string]bool
	ctxRefs      []string
	filters      map[string]string
	tests        map[string]string
	blockOrder   []string
	blocks       map[string]*jnode
	extendsSoFar int
	knownExtends bool
	finalized    bool
	// Names of the Python functions enclosing the code being written, for a macro def's qualified name
	defs []string
	// The self binding the root body opens with when it reads self
	rootSelf *bindStmt
}

// The template reference self binds to, reaching every block the template defines
func (g *codegen) templateRef() *TemplateRef {
	blocks := make(map[string]*blockStmt, len(g.blocks))
	for name, b := range g.blocks {
		blocks[name] = b.src.(*blockStmt)
	}
	return &TemplateRef{blocks: blocks}
}

func itoa(n int) string { return strconv.Itoa(n) }

// Generates jinja2's module source for a parsed body, folding constants into it and raising jinja2's compile time errors in its order
func compileTree(body []stmt, py *pyStack) (*codegen, error) {
	g := &codegen{
		py:         py,
		f:          &folder{r: &renderer{clock: now}},
		codeLineno: 1,
		firstWrite: true,
		ctxRefs:    []string{"context"},
		filters:    map[string]string{},
		tests:      map[string]string{},
		blocks:     map[string]*jnode{},
	}
	tree := mirrorBody(body)
	err := g.try(func() {
		g.enter()
		defer g.leave()
		g.need(2)
		g.enter()
		defer g.leave()
		g.need(1)
		g.visitTemplate(tree)
	})
	if err == nil {
		err = pyCompile(g.out.String(), g.lits)
	}
	return g, err
}

// The module source as jinja2 writes it, every literal mark replaced by its repr
func (g *codegen) source() string {
	code := g.out.String()
	var b strings.Builder
	last := 0
	for _, m := range g.lits {
		b.WriteString(code[last:m.pos])
		b.WriteString(literalRepr(m.v))
		last = m.pos
	}
	b.WriteString(code[last:])
	return b.String()
}

// A str or Markup constant whose repr belongs at a byte offset of the written code
type litMark struct {
	pos int
	v   Value
}

func literalRepr(v Value) string {
	if m, ok := v.(Markup); ok {
		return "Markup(" + strRepr(string(m)) + ")"
	}
	return strRepr(v.(string))
}

// Writes a constant's repr, its str and Markup parts as literal marks and the rest as code
func (g *codegen) writeRepr(v Value) {
	g.write("")
	g.writeReprParts(v)
}

func (g *codegen) writeReprParts(v Value) {
	switch x := v.(type) {
	case string, Markup:
		g.lits = append(g.lits, litMark{pos: g.out.Len(), v: x})
	case *List:
		g.out.WriteString("[")
		g.writeItems(x.items)
		g.out.WriteString("]")
	case *Tuple:
		g.out.WriteString("(")
		g.writeItems(x.items)
		if len(x.items) == 1 {
			g.out.WriteString(",")
		}
		g.out.WriteString(")")
	case *Dict:
		g.out.WriteString("{")
		for i := range x.keys {
			if i > 0 {
				g.out.WriteString(", ")
			}
			g.writeReprParts(x.keys[i])
			g.out.WriteString(": ")
			g.writeReprParts(x.vals[i])
		}
		g.out.WriteString("}")
	default:
		g.out.WriteString(g.reprValue(v))
	}
}

func (g *codegen) writeItems(items []Value) {
	for i, it := range items {
		if i > 0 {
			g.out.WriteString(", ")
		}
		g.writeReprParts(it)
	}
}

func (g *codegen) raise(err error) { panic(pyRaise{err}) }

// Pushes the frame of a called Python function, raising RecursionError past the limit
func (g *codegen) enter() {
	if err := g.py.enter(); err != nil {
		g.raise(err)
	}
}

func (g *codegen) leave() { g.py.leave() }

// Requires k frames above the running one for calls that return before it goes on
func (g *codegen) need(k int) {
	if err := g.py.need(k); err != nil {
		g.raise(err)
	}
}

// Runs f, returning the Python exception it raised as try and except would catch it
func (g *codegen) try(f func()) (err error) {
	depth := g.py.depth
	defer func() {
		if r := recover(); r != nil {
			pr, ok := r.(pyRaise)
			if !ok {
				panic(r)
			}
			g.py.depth = depth
			err = pr.err
		}
	}()
	f()
	return nil
}

// A TemplateAssertionError raised by CodeGenerator.fail, its exception __init__ frames above fail
func (g *codegen) fail(msg string, line int) {
	g.enter()
	defer g.leave()
	g.need(exceptionInitFrames)
	g.raise(&Error{Kind: "TemplateAssertionError", Msg: msg, Line: line})
}

// The frame jinja2 compiles a template body in, the eval context shared through the codegen
type cframe struct {
	sym                *symbols
	requireOutputCheck bool
	buffer             string
	block              string
	toplevel           bool
	rootlevel          bool
	loopFrame          bool
	blockFrame         bool
	softFrame          bool
}

// Frame.__init__ with its Symbols, a child of parent unless parent is nil
func (g *codegen) newFrame(parent *cframe, level int, hasLevel bool) *cframe {
	g.enter()
	defer g.leave()
	if parent == nil {
		return &cframe{sym: g.newSymbols(nil, level, hasLevel)}
	}
	return &cframe{
		sym:                g.newSymbols(parent.sym, level, hasLevel),
		requireOutputCheck: parent.requireOutputCheck,
		buffer:             parent.buffer,
		block:              parent.block,
	}
}

// Frame.inner, a child frame or an isolated one a level below
func (g *codegen) inner(fr *cframe, isolated bool) *cframe {
	g.enter()
	defer g.leave()
	if isolated {
		return g.newFrame(nil, fr.sym.level+1, true)
	}
	return g.newFrame(fr, 0, false)
}

// Frame.soft, a copy no longer at root level marked as an if or conditional expression frame
func (g *codegen) soft(fr *cframe) *cframe {
	g.enter()
	defer g.leave()
	c := g.copyFrame(fr)
	c.rootlevel = false
	c.softFrame = true
	return c
}

func (g *codegen) copyFrame(fr *cframe) *cframe {
	g.enter()
	defer g.leave()
	c := *fr
	c.sym = g.copySymbols(fr.sym)
	return &c
}

func (g *codegen) temporaryIdentifier() string {
	g.enter()
	defer g.leave()
	g.lastIdent++
	return "t_" + itoa(g.lastIdent)
}

func (g *codegen) buffer(fr *cframe) {
	g.enter()
	defer g.leave()
	fr.buffer = g.temporaryIdentifier()
	g.writeline(fr.buffer+" = []", nil, 0)
}

func (g *codegen) returnBufferContents(fr *cframe, forceUnescaped bool) {
	g.enter()
	defer g.leave()
	if !forceUnescaped {
		if g.f.volatile {
			g.writeline("if context.eval_ctx.autoescape:", nil, 0)
			g.indent()
			g.writeline("return Markup(concat("+fr.buffer+"))", nil, 0)
			g.outdent(1)
			g.writeline("else:", nil, 0)
			g.indent()
			g.writeline("return concat("+fr.buffer+")", nil, 0)
			g.outdent(1)
			return
		}
		if g.f.r.autoescape {
			g.writeline("return Markup(concat("+fr.buffer+"))", nil, 0)
			return
		}
	}
	g.writeline("return concat("+fr.buffer+")", nil, 0)
}

func (g *codegen) indent() {
	g.enter()
	defer g.leave()
	g.indentation++
}

func (g *codegen) outdent(step int) {
	g.enter()
	defer g.leave()
	g.indentation -= step
}

func (g *codegen) startWrite(fr *cframe, node *jnode) {
	g.enter()
	defer g.leave()
	if fr.buffer == "" {
		g.writeline("yield ", node, 0)
	} else {
		g.writeline(fr.buffer+".append(", node, 0)
	}
}

func (g *codegen) endWrite(fr *cframe) {
	g.enter()
	defer g.leave()
	if fr.buffer != "" {
		g.write(")")
	}
}

func (g *codegen) simpleWrite(s string, fr *cframe, node *jnode) {
	g.enter()
	defer g.leave()
	g.startWrite(fr, node)
	g.write(s)
	g.endWrite(fr)
}

// jinja2's CompilerExit, raised by an extends after a known one so the rest of the block is skipped
var errCompilerExit = &Error{Kind: "CompilerExit"}

// Writes pass then visits each node, a CompilerExit from an extends ending the block
func (g *codegen) blockvisit(nodes []*jnode, fr *cframe) {
	g.enter()
	defer g.leave()
	err := g.try(func() {
		g.writeline("pass", nil, 0)
		for _, n := range nodes {
			g.visit(n, fr)
		}
	})
	if err != nil && err != errCompilerExit {
		g.raise(err)
	}
}

func (g *codegen) write(x string) {
	g.enter()
	defer g.leave()
	if g.newLines > 0 {
		if !g.firstWrite {
			g.out.WriteString(strings.Repeat("\n", g.newLines))
			g.codeLineno += g.newLines
			if g.debugPending {
				g.debug = append(g.debug, [2]int{g.debugLine, g.codeLineno})
				g.debugPending = false
			}
		}
		g.firstWrite = false
		g.out.WriteString(strings.Repeat("    ", g.indentation))
		g.newLines = 0
	}
	g.out.WriteString(x)
}

func (g *codegen) writeline(x string, node *jnode, extra int) {
	g.enter()
	defer g.leave()
	g.newline(node, extra)
	g.write(x)
}

func (g *codegen) newline(node *jnode, extra int) {
	g.enter()
	defer g.leave()
	g.newLines = max(g.newLines, 1+extra)
	if node != nil && node.line != g.lastLine {
		g.debugPending, g.debugLine = true, node.line
		g.lastLine = node.line
	}
}

// Python keywords, which call keywords must not be written as so jinja2 passes them through a dict
var pythonKeywords = map[string]bool{
	"False": true, "None": true, "True": true, "and": true, "as": true, "assert": true, "async": true, "await": true,
	"break": true, "class": true, "continue": true, "def": true, "del": true, "elif": true, "else": true, "except": true,
	"finally": true, "for": true, "from": true, "global": true, "if": true, "import": true, "in": true, "is": true,
	"lambda": true, "nonlocal": true, "not": true, "or": true, "pass": true, "raise": true, "return": true, "try": true,
	"while": true, "with": true, "yield": true,
}

// A call's extra keyword argument in the order jinja2 writes it
type extraKwarg struct{ key, value string }

// CodeGenerator.signature, writing a call's arguments with a leading comma
func (g *codegen) signature(n *jnode, fr *cframe, extra []extraKwarg) {
	g.enter()
	defer g.leave()
	workaround := false
	g.need(2)
	for _, kw := range n.many("kwargs") {
		if pythonKeywords[kw.name] {
			workaround = true
			break
		}
	}
	if !workaround {
		for _, e := range extra {
			if pythonKeywords[e.key] {
				workaround = true
				break
			}
		}
	}
	for _, a := range n.many("args") {
		g.write(", ")
		g.visit(a, fr)
	}
	if !workaround {
		for _, kw := range n.many("kwargs") {
			g.write(", ")
			g.visit(kw, fr)
		}
		for _, e := range extra {
			g.write(", " + e.key + "=" + e.value)
		}
	}
	if d := n.one("dyn_args"); d != nil {
		g.write(", *")
		g.visit(d, fr)
	}
	dk := n.one("dyn_kwargs")
	if workaround {
		if dk != nil {
			g.write(", **dict({")
		} else {
			g.write(", **{")
		}
		for _, kw := range n.many("kwargs") {
			g.write(strRepr(kw.name) + ": ")
			g.visit(kw.one("value"), fr)
			g.write(", ")
		}
		for _, e := range extra {
			g.write(strRepr(e.key) + ": " + e.value + ", ")
		}
		if dk != nil {
			g.write("}, **")
			g.visit(dk, fr)
			g.write(")")
		} else {
			g.write("}")
		}
	} else if dk != nil {
		g.write(", **")
		g.visit(dk, fr)
	}
}

// CodeGenerator.pull_dependencies, binding each filter and test the nodes use to a temporary with a runtime fallback
func (g *codegen) pullDependencies(nodes []*jnode) {
	g.enter()
	defer g.leave()
	g.need(1)
	filters, tests := map[string]bool{}, map[string]bool{}
	for _, n := range nodes {
		g.findDependencies(n, filters, tests)
	}
	for _, dep := range []struct {
		ids   map[string]string
		names map[string]bool
		kind  string
	}{{g.filters, filters, "filters"}, {g.tests, tests, "tests"}} {
		names := make([]string, 0, len(dep.names))
		for n := range dep.names {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			if _, ok := dep.ids[name]; !ok {
				dep.ids[name] = g.temporaryIdentifier()
			}
			id := dep.ids[name]
			g.writeline("try:", nil, 0)
			g.indent()
			g.writeline(id+" = environment."+dep.kind+"["+strRepr(name)+"]", nil, 0)
			g.outdent(1)
			g.writeline("except KeyError:", nil, 0)
			g.indent()
			g.writeline("@internalcode", nil, 0)
			g.writeline("def "+id+"(*unused):", nil, 0)
			g.indent()
			g.writeline("raise TemplateRuntimeError(\"No "+dep.kind[:len(dep.kind)-1]+" named "+strRepr(name)+" found.\")", nil, 0)
			g.outdent(1)
			g.outdent(1)
		}
	}
}

// DependencyFinderVisitor.visit, collecting filter and test names and stopping at blocks
func (g *codegen) findDependencies(n *jnode, filters, tests map[string]bool) {
	g.enter()
	defer g.leave()
	g.need(1)
	g.enter()
	defer g.leave()
	generic := func() {
		g.eachChild(n, nil, func(c *jnode) { g.findDependencies(c, filters, tests) })
	}
	switch n.cls {
	case "Filter":
		g.enter()
		generic()
		g.leave()
		filters[n.name] = true
	case "Test":
		g.enter()
		generic()
		g.leave()
		tests[n.name] = true
	case "Block":
	default:
		generic()
	}
}

func (g *codegen) enterFrame(fr *cframe) {
	g.enter()
	defer g.leave()
	var undefs []string
	for _, target := range fr.sym.loadKeys {
		l := fr.sym.loads[target]
		switch l.action {
		case loadParameter:
		case loadResolve:
			g.writeline(target+" = "+g.resolveFunc()+"("+strRepr(l.param)+")", nil, 0)
		case loadAlias:
			g.writeline(target+" = "+l.param, nil, 0)
		case loadUndefined:
			undefs = append(undefs, target)
		}
	}
	if len(undefs) > 0 {
		g.writeline(strings.Join(undefs, " = ")+" = missing", nil, 0)
	}
}

func (g *codegen) leaveFrame(fr *cframe, withPythonScope bool) {
	g.enter()
	defer g.leave()
	if !withPythonScope && len(fr.sym.loadKeys) > 0 {
		g.writeline(strings.Join(fr.sym.loadKeys, " = ")+" = missing", nil, 0)
	}
}

func (g *codegen) chooseAsync(asyncValue, syncValue string) string {
	g.enter()
	defer g.leave()
	return syncValue
}

func (g *codegen) fn(name string) string {
	g.enter()
	defer g.leave()
	return g.chooseAsync("async ", "") + "def " + name
}

// What macro_body learned about a macro or call block, the MacroRef jinja2 builds
type macroRef struct {
	node                                            *jnode
	accessesCaller, accessesKwargs, accessesVarargs bool
	// The def's Python qualified name, which a call passing too many arguments names
	qualname string
}

// CodeGenerator.macro_body, the def of a macro or call block with its parameter handling and body
func (g *codegen) macroBody(n *jnode, fr *cframe) (*cframe, *macroRef) {
	g.enter()
	defer g.leave()
	fr = g.inner(fr, false)
	g.analyzeNode(fr.sym, n, "body")
	g.need(1)
	ref := &macroRef{node: n}
	explicitCaller := -1
	skip := map[string]bool{}
	var args []string
	params := n.many("args")
	for i, a := range params {
		if a.name == "caller" {
			explicitCaller = i
		}
		if a.name == "kwargs" || a.name == "varargs" {
			skip[a.name] = true
		}
		args = append(args, g.ref(fr.sym, a.name))
	}
	undeclared := g.findUndeclared(n.many("body"), false, "caller", "kwargs", "varargs")
	defaults := n.many("defaults")
	if undeclared["caller"] {
		if explicitCaller >= 0 {
			if idx := explicitCaller - len(params) + len(defaults); idx < 0 {
				g.fail("When defining macros or call blocks the special \"caller\" argument must be omitted or be given a default.", n.line)
			}
		} else {
			args = append(args, g.declareParameter(fr.sym, "caller"))
		}
		ref.accessesCaller = true
	}
	if undeclared["kwargs"] && !skip["kwargs"] {
		args = append(args, g.declareParameter(fr.sym, "kwargs"))
		ref.accessesKwargs = true
	}
	if undeclared["varargs"] && !skip["varargs"] {
		args = append(args, g.declareParameter(fr.sym, "varargs"))
		ref.accessesVarargs = true
	}
	fr.requireOutputCheck = false
	g.analyzeNode(fr.sym, n, "body")
	ref.qualname = strings.Join(append(append([]string(nil), g.defs...), "macro"), ".<locals>.")
	g.defs = append(g.defs, "macro")
	defer func() { g.defs = g.defs[:len(g.defs)-1] }()
	g.writeline(g.fn("macro")+"("+strings.Join(args, ", ")+"):", n, 0)
	g.indent()
	g.buffer(fr)
	g.enterFrame(fr)
	g.pushParameterDefinitions(fr)
	for i, a := range params {
		r := g.ref(fr.sym, a.name)
		g.writeline("if "+r+" is missing:", nil, 0)
		g.indent()
		if di := i - len(params) + len(defaults); di < 0 {
			g.writeline(r+" = undefined(\"parameter "+strRepr(a.name)+" was not provided\", name="+strRepr(a.name)+")", nil, 0)
		} else {
			g.writeline(r+" = ", nil, 0)
			g.visit(defaults[di], fr)
		}
		g.markParameterStored(r)
		g.outdent(1)
	}
	g.popParameterDefinitions()
	g.blockvisit(n.many("body"), fr)
	g.returnBufferContents(fr, true)
	g.leaveFrame(fr, true)
	g.outdent(1)
	return fr, ref
}

// CodeGenerator.macro_def, the Macro construction for a def macro_body wrote
func (g *codegen) macroDef(ref *macroRef, fr *cframe) {
	g.enter()
	defer g.leave()
	params := ref.node.many("args")
	g.need(1)
	names := make([]string, len(params))
	for i, a := range params {
		names[i] = strRepr(a.name)
	}
	tuple := strings.Join(names, ", ")
	if len(params) == 1 {
		tuple += ","
	}
	name := "None"
	if ref.node.cls == "Macro" {
		name = strRepr(ref.node.name)
	}
	g.write("Macro(environment, macro, " + name + ", (" + tuple + "), " + pyBool(ref.accessesKwargs) + ", " +
		pyBool(ref.accessesVarargs) + ", " + pyBool(ref.accessesCaller) + ", context.eval_ctx.autoescape)")
}

func pyBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

func (g *codegen) position(n *jnode) string {
	g.enter()
	defer g.leave()
	return "line " + itoa(n.line)
}

func (g *codegen) dumpLocalContext(fr *cframe) string {
	g.enter()
	defer g.leave()
	names, refs := g.dumpStores(fr.sym)
	g.need(1)
	items := make([]string, len(names))
	for i, n := range names {
		items[i] = strRepr(n) + ": " + refs[n]
	}
	return "{" + strings.Join(items, ", ") + "}"
}

func (g *codegen) writeCommons() {
	g.enter()
	defer g.leave()
	g.writeline("resolve = context.resolve_or_missing", nil, 0)
	g.writeline("undefined = environment.undefined", nil, 0)
	g.writeline("concat = environment.concat", nil, 0)
	g.writeline("cond_expr_undefined = Undefined", nil, 0)
	g.writeline("if 0: yield None", nil, 0)
}

func (g *codegen) pushParameterDefinitions(fr *cframe) {
	g.enter()
	defer g.leave()
	g.paramDefs = append(g.paramDefs, g.dumpParamTargets(fr.sym))
}

func (g *codegen) popParameterDefinitions() {
	g.enter()
	defer g.leave()
	g.paramDefs = g.paramDefs[:len(g.paramDefs)-1]
}

func (g *codegen) markParameterStored(target string) {
	g.enter()
	defer g.leave()
	if len(g.paramDefs) > 0 {
		delete(g.paramDefs[len(g.paramDefs)-1], target)
	}
}

func (g *codegen) contextRef() string {
	g.enter()
	defer g.leave()
	return g.ctxRefs[len(g.ctxRefs)-1]
}

func (g *codegen) resolveFunc() string {
	g.enter()
	defer g.leave()
	target := g.ctxRefs[len(g.ctxRefs)-1]
	if target == "context" {
		return "resolve"
	}
	return target + ".resolve"
}

func (g *codegen) deriveContext(fr *cframe) string {
	g.enter()
	defer g.leave()
	return g.contextRef() + ".derived(" + g.dumpLocalContext(fr) + ")"
}

func (g *codegen) parameterIsUndeclared(target string) bool {
	g.enter()
	defer g.leave()
	if len(g.paramDefs) == 0 {
		return false
	}
	return g.paramDefs[len(g.paramDefs)-1][target]
}

func (g *codegen) pushAssignTracking() {
	g.enter()
	defer g.leave()
	g.assignStack = append(g.assignStack, map[string]bool{})
}

func (g *codegen) popAssignTracking(fr *cframe) {
	g.enter()
	defer g.leave()
	vars := g.assignStack[len(g.assignStack)-1]
	g.assignStack = g.assignStack[:len(g.assignStack)-1]
	if (!fr.blockFrame && !fr.loopFrame && !fr.toplevel) || len(vars) == 0 {
		return
	}
	names := make([]string, 0, len(vars))
	var public []string
	for n := range vars {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !strings.HasPrefix(n, "_") {
			public = append(public, n)
		}
	}
	if len(names) == 1 {
		name := names[0]
		r := g.ref(fr.sym, name)
		switch {
		case fr.loopFrame:
			g.writeline("_loop_vars["+strRepr(name)+"] = "+r, nil, 0)
			return
		case fr.blockFrame:
			g.writeline("_block_vars["+strRepr(name)+"] = "+r, nil, 0)
			return
		}
		g.writeline("context.vars["+strRepr(name)+"] = "+r, nil, 0)
	} else {
		switch {
		case fr.loopFrame:
			g.writeline("_loop_vars.update({", nil, 0)
		case fr.blockFrame:
			g.writeline("_block_vars.update({", nil, 0)
		default:
			g.writeline("context.vars.update({", nil, 0)
		}
		for i, name := range names {
			if i > 0 {
				g.write(", ")
			}
			g.write(strRepr(name) + ": " + g.ref(fr.sym, name))
		}
		g.write("})")
	}
	if !fr.blockFrame && !fr.loopFrame && len(public) > 0 {
		if len(public) == 1 {
			g.writeline("context.exported_vars.add("+strRepr(public[0])+")", nil, 0)
		} else {
			reprs := make([]string, len(public))
			for i, n := range public {
				reprs[i] = strRepr(n)
			}
			g.writeline("context.exported_vars.update(("+strings.Join(reprs, ", ")+"))", nil, 0)
		}
	}
}
