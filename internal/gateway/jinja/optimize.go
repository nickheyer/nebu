package jinja

import "strings"

// jinja2's Impossible, raised by as_const for a value only render time knows
var errImpossible = &Error{Kind: "Impossible"}

func (g *codegen) impossible() { g.raise(errImpossible) }

// Optimizer.visit, folding a mirrored expression's children first and the node itself when as_const succeeds
func (g *codegen) optimizeNode(n *jnode) *jnode {
	g.enter()
	defer g.leave()
	g.need(1)
	g.enter()
	defer g.leave()
	g.transformChildren(n)
	if !n.isExpr() {
		return n
	}
	var out *jnode
	err := g.try(func() { out = g.fromUntrusted(g.asConst(n), n) })
	if err == errImpossible {
		return n
	}
	if err != nil {
		g.raise(err)
	}
	return out
}

// NodeTransformer.generic_visit, replacing each child with what the optimizer returns for it
func (g *codegen) transformChildren(n *jnode) {
	g.enter()
	defer g.leave()
	for i := range n.slots {
		s := &n.slots[i]
		g.need(1)
		if s.list {
			for j, c := range s.many {
				if r := g.optimizeNode(c); r != c {
					s.many[j] = r
					r.setGo()
				}
			}
		} else if s.one != nil {
			if r := g.optimizeNode(s.one); r != s.one {
				s.one = r
				r.setGo()
			}
		}
	}
	g.need(1)
}

// Writes a folded Const into the parsed tree slot of the expression it replaced
func (n *jnode) setGo() {
	if n.set != nil {
		n.set(&constExpr{v: n.value, fresh: true, line: n.line})
	}
}

// Const.from_untrusted, a Const node for a value with a safe repr, raising Impossible otherwise
func (g *codegen) fromUntrusted(v Value, orig *jnode) *jnode {
	g.enter()
	defer g.leave()
	if !g.hasSafeRepr(v) {
		g.impossible()
	}
	g.need(1)
	return &jnode{cls: "Const", line: orig.line, value: v, set: orig.set}
}

// has_safe_repr, recursing through containers with its all() generator frames
func (g *codegen) hasSafeRepr(v Value) bool {
	g.enter()
	defer g.leave()
	all := func(items []Value) bool {
		g.enter()
		defer g.leave()
		for _, it := range items {
			if !g.hasSafeRepr(it) {
				return false
			}
		}
		return true
	}
	switch x := v.(type) {
	case NoneType, bool, int64, float64, complex128, string, Markup, *Range:
		return true
	case *Tuple:
		return x.names == nil && all(x.items)
	case *List:
		return all(x.items)
	case *Dict:
		g.enter()
		defer g.leave()
		for i := range x.keys {
			if !g.hasSafeRepr(x.keys[i]) || !g.hasSafeRepr(x.vals[i]) {
				return false
			}
		}
		return true
	}
	return isIntegral(v)
}

// get_eval_context's frame above the running as_const
func (g *codegen) evalContext() { g.need(1) }

// Runs f, turning any exception it raises into Impossible as as_const's try blocks do
func (g *codegen) orImpossible(f func()) {
	if err := g.try(f); err != nil {
		g.impossible()
	}
}

// Node.as_const for a mirrored expression, its value or Impossible
func (g *codegen) asConst(n *jnode) Value {
	g.enter()
	defer g.leave()
	f := g.f
	switch n.cls {
	case "Const":
		return n.value
	case "TemplateData":
		g.evalContext()
		if f.volatile {
			g.impossible()
		}
		if f.r.autoescape {
			g.need(1)
			return Markup(n.value.(string))
		}
		return n.value
	case "Tuple", "List":
		g.evalContext()
		if n.cls == "Tuple" {
			g.enter()
			defer g.leave()
		}
		items := make([]Value, 0, len(n.many("items")))
		for _, it := range n.many("items") {
			items = append(items, g.asConst(it))
		}
		if n.cls == "Tuple" {
			return &Tuple{items: items}
		}
		return &List{items: items}
	case "Dict":
		g.evalContext()
		g.enter()
		defer g.leave()
		d := newDict()
		for _, p := range n.many("items") {
			k, v := g.pairConst(p)
			if err := d.set(k, v); err != nil {
				g.raise(err)
			}
		}
		return d
	case "CondExpr":
		g.evalContext()
		if truthy(g.asConst(n.one("test"))) {
			return g.asConst(n.one("expr1"))
		}
		if n.one("expr2") == nil {
			g.impossible()
		}
		return g.asConst(n.one("expr2"))
	case "And", "Or":
		g.evalContext()
		left := g.asConst(n.one("left"))
		if truthy(left) == (n.cls == "Or") {
			return left
		}
		return g.asConst(n.one("right"))
	case "Add", "Sub", "Mul", "Div", "FloorDiv", "Mod", "Pow":
		g.evalContext()
		var out Value
		g.orImpossible(func() {
			left := g.asConst(n.one("left"))
			right := g.asConst(n.one("right"))
			v, err := binaryOp(binOps[n.cls], left, right)
			if err == nil {
				err = f.r.chargeValue(v)
			}
			if err != nil {
				g.raise(err)
			}
			out = v
		})
		return out
	case "Not", "Neg", "Pos":
		g.evalContext()
		var out Value
		g.orImpossible(func() {
			v := g.asConst(n.one("node"))
			if n.cls == "Not" {
				out = !truthy(v)
				return
			}
			r, err := unaryOp(map[string]string{"Neg": "-", "Pos": "+"}[n.cls], v)
			if err == nil {
				err = f.r.chargeValue(r)
			}
			if err != nil {
				g.raise(err)
			}
			out = r
		})
		return out
	case "Concat":
		g.evalContext()
		g.enter()
		defer g.leave()
		var b strings.Builder
		for _, it := range n.many("nodes") {
			s, err := pyStr(g.asConst(it))
			if err == nil {
				err = checkBytes(b.Len() + len(s))
			}
			if err != nil {
				g.raise(err)
			}
			b.WriteString(s)
		}
		if err := f.r.charge(b.Len()); err != nil {
			g.raise(err)
		}
		return b.String()
	case "Compare":
		g.evalContext()
		left := g.asConst(n.one("expr"))
		var out Value = left
		g.orImpossible(func() {
			for _, op := range n.many("ops") {
				right := g.asConst(op.one("expr"))
				if op.op == "in" || op.op == "notin" {
					g.need(1)
				}
				res, err := compareOp(op.op, left, right)
				if err != nil {
					g.raise(err)
				}
				if !res {
					out = false
					return
				}
				out = true
				left = right
			}
		})
		return out
	case "Getattr", "Getitem":
		g.evalContext()
		var out Value
		g.orImpossible(func() {
			obj := g.asConst(n.one("node"))
			var v Value
			var err error
			if n.cls == "Getattr" {
				g.need(f.r.getattrFrames(obj, n.attr))
				v, err = f.r.getattr(obj, n.attr)
			} else {
				arg := g.asConst(n.one("arg"))
				g.need(f.r.getitemFrames(obj, arg))
				v, err = f.r.getitem(obj, arg)
			}
			if err == nil {
				err = f.r.chargeValue(v)
			}
			if err != nil {
				g.raise(err)
			}
			out = v
		})
		return out
	case "Slice":
		g.evalContext()
		parts := [3]Value{none, none, none}
		for i, name := range []string{"start", "stop", "step"} {
			g.enter()
			if c := n.one(name); c != nil {
				parts[i] = g.asConst(c)
			}
			g.leave()
		}
		return &sliceValue{start: parts[0], stop: parts[1], step: parts[2]}
	case "Filter":
		if n.one("node") == nil {
			g.impossible()
		}
		g.enter()
		defer g.leave()
		return g.callConst(n, true)
	case "Test":
		return g.callConst(n, false)
	}
	g.impossible()
	return nil
}

// jinja2 class names of binary nodes mapped to the operator token kinds binaryOp takes
var binOps = map[string]string{"Add": "add", "Sub": "sub", "Mul": "mul", "Div": "div", "FloorDiv": "floordiv", "Mod": "mod", "Pow": "pow"}

// Pair.as_const, the key and value through their own eval context lookup
func (g *codegen) pairConst(p *jnode) (Value, Value) {
	g.enter()
	defer g.leave()
	g.evalContext()
	return g.asConst(p.one("key")), g.asConst(p.one("value"))
}

// _FilterTestCommon.as_const, calling the builtin on constant arguments unless it needs the render context
func (g *codegen) callConst(n *jnode, isFilter bool) Value {
	f := g.f
	g.evalContext()
	if f.volatile {
		g.impossible()
	}
	known := false
	if isFilter {
		_, known = builtinFilters[n.name]
	} else {
		_, known = builtinTests[n.name]
	}
	g.need(1)
	if !known || passArg(n.name, isFilter) == "context" {
		g.impossible()
	}
	args, kw := g.argsConst(n)
	v := g.asConst(n.one("node"))
	var out Value
	g.orImpossible(func() {
		g.need(f.r.builtinCallFrames(n.name, isFilter, v, args, kw))
		var err error
		if isFilter {
			out, err = f.r.callFilter(n.name, v, args, kw)
			if err == nil {
				err = f.r.chargeValue(out)
			}
		} else {
			out, err = f.r.callTest(n.name, v, args, kw)
		}
		if err != nil {
			g.raise(err)
		}
	})
	return out
}

// args_as_const, positional values, keyword values through Keyword.as_const and the starred arguments
func (g *codegen) argsConst(n *jnode) ([]Value, *Dict) {
	g.enter()
	defer g.leave()
	var args []Value
	for _, a := range n.many("args") {
		args = append(args, g.asConst(a))
	}
	kw := newDict()
	g.enter()
	for _, k := range n.many("kwargs") {
		g.enter()
		g.evalContext()
		kw.put(k.name, g.asConst(k.one("value")))
		g.leave()
	}
	g.leave()
	if d := n.one("dyn_args"); d != nil {
		g.orImpossible(func() {
			v := g.asConst(d)
			extra, err := iterValues(v)
			if err != nil {
				g.raise(err)
			}
			args = append(args, extra...)
		})
	}
	if d := n.one("dyn_kwargs"); d != nil {
		g.orImpossible(func() {
			v := g.asConst(d)
			m, ok := v.(*Dict)
			if !ok {
				g.raise(typeError("'%s' object is not a mapping", typeName(v)))
			}
			for i, k := range m.keys {
				ks, ok := k.(string)
				if !ok {
					g.raise(typeError("keywords must be strings"))
				}
				kw.put(ks, m.vals[i])
			}
		})
	}
	return args, kw
}
