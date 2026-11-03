package jinja

// A node shaped as jinja2's nodes.py builds it, mirroring a parsed statement or expression for code generation
type jnode struct {
	cls   string
	line  int
	slots []jslot
	// Scalar fields of the jinja2 node
	name, attr, op, ctx string
	value               Value
	scoped, required    bool
	recursive           bool
	ignoreMissing       bool
	withContext         bool
	names               []importName
	// The parsed node this mirrors and, for expressions, the setter that replaces it in its parent
	src any
	set func(expr)
}

// A node valued field in jinja2's field order, one child or a list of them
type jslot struct {
	name string
	list bool
	one  *jnode
	many []*jnode
}

func (n *jnode) slot(name string) *jslot {
	for i := range n.slots {
		if n.slots[i].name == name {
			return &n.slots[i]
		}
	}
	return nil
}

func (n *jnode) one(name string) *jnode {
	if s := n.slot(name); s != nil {
		return s.one
	}
	return nil
}

func (n *jnode) many(name string) []*jnode {
	if s := n.slot(name); s != nil {
		return s.many
	}
	return nil
}

func (n *jnode) addOne(name string, c *jnode) *jnode {
	n.slots = append(n.slots, jslot{name: name, one: c})
	return n
}

func (n *jnode) addMany(name string, cs []*jnode) *jnode {
	n.slots = append(n.slots, jslot{name: name, list: true, many: cs})
	return n
}

// The direct child nodes in field order, as Node.iter_child_nodes yields them
func (n *jnode) children() []*jnode {
	var out []*jnode
	for _, s := range n.slots {
		if s.list {
			out = append(out, s.many...)
		} else if s.one != nil {
			out = append(out, s.one)
		}
	}
	return out
}

// Whether the node is one of jinja2's expression classes
func (n *jnode) isExpr() bool { return exprClasses[n.cls] }

var exprClasses = map[string]bool{
	"Name": true, "NSRef": true, "Const": true, "TemplateData": true, "Tuple": true, "List": true, "Dict": true,
	"CondExpr": true, "Filter": true, "Test": true, "Call": true, "Getitem": true, "Getattr": true, "Slice": true,
	"Concat": true, "Compare": true, "Add": true, "Sub": true, "Mul": true, "Div": true, "FloorDiv": true, "Mod": true,
	"Pow": true, "And": true, "Or": true, "Not": true, "Neg": true, "Pos": true, "ExtensionAttribute": true,
}

// jinja2 class names of binary operators by the parser's token kind
var binClasses = map[string]string{"add": "Add", "sub": "Sub", "mul": "Mul", "div": "Div", "floordiv": "FloorDiv", "mod": "Mod", "pow": "Pow"}

// Builds the jinja2 shaped tree of a parsed template body
func mirrorBody(body []stmt) []*jnode {
	var out []*jnode
	var group *jnode
	for _, s := range body {
		switch x := s.(type) {
		case *textStmt:
			item := &jnode{cls: "TemplateData", line: x.line, value: x.text, src: x}
			group = appendOutput(&out, group, x.cont, item)
			continue
		case *outputStmt:
			item := mirrorExpr(x.expr, func(e expr) { x.expr = e })
			item.src = x.expr
			group = appendOutput(&out, group, x.cont, item)
			continue
		}
		group = nil
		out = append(out, mirrorStmt(s))
	}
	return out
}

// Adds an output item to the current output node, or opens a new one at the item's line
func appendOutput(out *[]*jnode, group *jnode, cont bool, item *jnode) *jnode {
	if group == nil || !cont {
		group = (&jnode{cls: "Output", line: item.line}).addMany("nodes", nil)
		*out = append(*out, group)
	}
	group.slots[0].many = append(group.slots[0].many, item)
	return group
}

func mirrorStmt(s stmt) *jnode {
	switch x := s.(type) {
	case *forStmt:
		n := &jnode{cls: "For", line: x.line, recursive: x.recursive, src: x}
		n.addOne("target", mirrorTarget(x.target, "store"))
		n.addOne("iter", mirrorExpr(x.iter, func(e expr) { x.iter = e }))
		n.addMany("body", mirrorBody(x.body)).addMany("else_", mirrorBody(x.orelse))
		if x.test != nil {
			n.addOne("test", mirrorExpr(x.test, func(e expr) { x.test = e }))
		} else {
			n.addOne("test", nil)
		}
		return n
	case *ifStmt:
		n := &jnode{cls: "If", line: x.line, src: x}
		n.addOne("test", mirrorExpr(x.test, func(e expr) { x.test = e }))
		n.addMany("body", mirrorBody(x.body))
		elifs := make([]*jnode, len(x.elifs))
		for i, e := range x.elifs {
			elifs[i] = mirrorStmt(e)
		}
		return n.addMany("elif_", elifs).addMany("else_", mirrorBody(x.orelse))
	case *setStmt:
		n := &jnode{cls: "Assign", line: x.line, src: x}
		n.addOne("target", mirrorTarget(x.target, "store"))
		return n.addOne("node", mirrorExpr(x.value, func(e expr) { x.value = e }))
	case *setBlockStmt:
		n := &jnode{cls: "AssignBlock", line: x.line, src: x}
		n.addOne("target", mirrorTarget(x.target, "store"))
		if x.filter != nil {
			n.addOne("filter", mirrorExpr(x.filter, func(e expr) { x.filter = e }))
		} else {
			n.addOne("filter", nil)
		}
		return n.addMany("body", mirrorBody(x.body))
	case *macroStmt:
		n := &jnode{cls: "Macro", line: x.line, name: x.name, src: x}
		return mirrorSignature(n, x.sig)
	case *callBlockStmt:
		n := &jnode{cls: "CallBlock", line: x.line, src: x}
		n.addOne("call", mirrorExpr(x.call, func(e expr) {
			if c, ok := e.(*callExpr); ok {
				x.call = c
			}
		}))
		return mirrorSignature(n, x.sig)
	case *filterBlockStmt:
		n := &jnode{cls: "FilterBlock", line: x.line, src: x}
		n.addMany("body", mirrorBody(x.body))
		return n.addOne("filter", mirrorExpr(x.filter, func(e expr) { x.filter = e }))
	case *withStmt:
		n := &jnode{cls: "With", line: x.line, src: x}
		targets := make([]*jnode, len(x.targets))
		for i, t := range x.targets {
			targets[i] = mirrorTarget(t, "param")
		}
		values := make([]*jnode, len(x.values))
		for i := range x.values {
			values[i] = mirrorExpr(x.values[i], func(e expr) { x.values[i] = e })
		}
		return n.addMany("targets", targets).addMany("values", values).addMany("body", mirrorBody(x.body))
	case *blockStmt:
		n := &jnode{cls: "Block", line: x.line, name: x.name, scoped: x.scoped, required: x.required, src: x}
		return n.addMany("body", mirrorBody(x.body))
	case *autoescapeStmt:
		mod := &jnode{cls: "ScopedEvalContextModifier", line: x.line, src: x}
		kw := &jnode{cls: "Keyword", name: "autoescape"}
		kw.addOne("value", mirrorExpr(x.value, func(e expr) { x.value = e }))
		mod.addMany("options", []*jnode{kw}).addMany("body", mirrorBody(x.body))
		return (&jnode{cls: "Scope", src: x}).addMany("body", []*jnode{mod})
	case *loaderStmt:
		cls := map[string]string{"extends": "Extends", "include": "Include", "import": "Import", "from": "FromImport"}[x.tag]
		n := &jnode{cls: cls, line: x.line, ignoreMissing: x.ignoreMissing, withContext: x.withContext, name: x.target, names: x.names, src: x}
		return n.addOne("template", mirrorExpr(x.template, func(e expr) { x.template = e }))
	case *generationStmt:
		attr := &jnode{cls: "ExtensionAttribute", line: x.line, name: "_generation_support"}
		call := (&jnode{cls: "Call", line: x.line}).addOne("node", attr).addMany("args", nil).addMany("kwargs", nil).addOne("dyn_args", nil).addOne("dyn_kwargs", nil)
		n := &jnode{cls: "CallBlock", line: x.line, src: x}
		return n.addOne("call", call).addMany("args", nil).addMany("defaults", nil).addMany("body", mirrorBody(x.body))
	case *breakStmt:
		return &jnode{cls: "Break", line: x.line, src: x}
	case *continueStmt:
		return &jnode{cls: "Continue", line: x.line, src: x}
	}
	panic("jinja: unmirrored statement")
}

// Adds a macro or call block's parameter names, defaults and body
func mirrorSignature(n *jnode, sig *signature) *jnode {
	args := make([]*jnode, len(sig.params))
	for i, p := range sig.params {
		args[i] = &jnode{cls: "Name", line: sig.paramLines[i], name: p, ctx: "param"}
	}
	defaults := make([]*jnode, len(sig.defaults))
	for i := range sig.defaults {
		defaults[i] = mirrorExpr(sig.defaults[i], func(e expr) { sig.defaults[i] = e })
	}
	return n.addMany("args", args).addMany("defaults", defaults).addMany("body", mirrorBody(sig.body))
}

// Mirrors an assignment target, names and tuples taking the given context
func mirrorTarget(e expr, ctx string) *jnode {
	n := mirrorExpr(e, nil)
	var setCtx func(*jnode)
	setCtx = func(m *jnode) {
		if m.cls == "Name" || m.cls == "Tuple" {
			m.ctx = ctx
		}
		for _, c := range m.children() {
			setCtx(c)
		}
	}
	setCtx(n)
	return n
}

// Mirrors an expression, the setter replacing it in the parsed tree
func mirrorExpr(e expr, set func(expr)) *jnode {
	n := mirrorExprNode(e)
	n.src, n.set = e, set
	return n
}

func mirrorList(es []expr) []*jnode {
	out := make([]*jnode, len(es))
	for i := range es {
		out[i] = mirrorExpr(es[i], func(e expr) { es[i] = e })
	}
	return out
}

// Mirrors call arguments into args, kwargs as Keyword nodes, dyn_args and dyn_kwargs
func mirrorArgs(n *jnode, a *callArgs) *jnode {
	n.addMany("args", mirrorList(a.args))
	kws := make([]*jnode, len(a.kwNames))
	for i, k := range a.kwNames {
		kw := &jnode{cls: "Keyword", name: k, line: lineOf(a.kwVals[i])}
		kws[i] = kw.addOne("value", mirrorExpr(a.kwVals[i], func(e expr) { a.kwVals[i] = e }))
	}
	n.addMany("kwargs", kws)
	n.addOne("dyn_args", mirrorOptional(a.dynArgs, func(e expr) { a.dynArgs = e }))
	return n.addOne("dyn_kwargs", mirrorOptional(a.dynKwargs, func(e expr) { a.dynKwargs = e }))
}

func mirrorOptional(e expr, set func(expr)) *jnode {
	if e == nil {
		return nil
	}
	return mirrorExpr(e, set)
}

func mirrorExprNode(e expr) *jnode {
	switch x := e.(type) {
	case *constExpr:
		return &jnode{cls: "Const", line: x.line, value: x.v}
	case *nameExpr:
		return &jnode{cls: "Name", line: x.line, name: x.name, ctx: "load"}
	case *nsRefExpr:
		return &jnode{cls: "NSRef", line: x.line, name: x.name, attr: x.attr}
	case *tupleExpr:
		return (&jnode{cls: "Tuple", line: x.line, ctx: "load"}).addMany("items", mirrorList(x.items))
	case *listExpr:
		return (&jnode{cls: "List", line: x.line}).addMany("items", mirrorList(x.items))
	case *dictExpr:
		pairs := make([]*jnode, len(x.keys))
		for i := range x.keys {
			p := &jnode{cls: "Pair", line: lineOf(x.keys[i])}
			p.addOne("key", mirrorExpr(x.keys[i], func(e expr) { x.keys[i] = e }))
			pairs[i] = p.addOne("value", mirrorExpr(x.vals[i], func(e expr) { x.vals[i] = e }))
		}
		return (&jnode{cls: "Dict", line: x.line}).addMany("items", pairs)
	case *getattrExpr:
		return (&jnode{cls: "Getattr", line: x.line, attr: x.attr, ctx: "load"}).addOne("node", mirrorExpr(x.obj, func(e expr) { x.obj = e }))
	case *getitemExpr:
		n := (&jnode{cls: "Getitem", line: x.line, ctx: "load"}).addOne("node", mirrorExpr(x.obj, func(e expr) { x.obj = e }))
		return n.addOne("arg", mirrorExpr(x.arg, func(e expr) { x.arg = e }))
	case *sliceExpr:
		n := &jnode{cls: "Slice", line: x.line}
		n.addOne("start", mirrorOptional(x.start, func(e expr) { x.start = e }))
		n.addOne("stop", mirrorOptional(x.stop, func(e expr) { x.stop = e }))
		return n.addOne("step", mirrorOptional(x.step, func(e expr) { x.step = e }))
	case *callExpr:
		n := (&jnode{cls: "Call", line: x.line}).addOne("node", mirrorExpr(x.fn, func(e expr) { x.fn = e }))
		return mirrorArgs(n, &x.args)
	case *filterExpr:
		n := &jnode{cls: "Filter", line: x.line, name: x.name}
		if _, buffered := x.node.(*bufferExpr); buffered {
			n.addOne("node", nil)
		} else {
			n.addOne("node", mirrorExpr(x.node, func(e expr) { x.node = e }))
		}
		return mirrorArgs(n, &x.args)
	case *testExpr:
		n := (&jnode{cls: "Test", line: x.line, name: x.name}).addOne("node", mirrorExpr(x.node, func(e expr) { x.node = e }))
		return mirrorArgs(n, &x.args)
	case *condExpr:
		n := (&jnode{cls: "CondExpr", line: x.line}).addOne("test", mirrorExpr(x.test, func(e expr) { x.test = e }))
		n.addOne("expr1", mirrorExpr(x.expr1, func(e expr) { x.expr1 = e }))
		return n.addOne("expr2", mirrorOptional(x.expr2, func(e expr) { x.expr2 = e }))
	case *binExpr:
		n := (&jnode{cls: binClasses[x.op], line: x.line}).addOne("left", mirrorExpr(x.left, func(e expr) { x.left = e }))
		return n.addOne("right", mirrorExpr(x.right, func(e expr) { x.right = e }))
	case *andExpr:
		n := (&jnode{cls: "And", line: x.line}).addOne("left", mirrorExpr(x.left, func(e expr) { x.left = e }))
		return n.addOne("right", mirrorExpr(x.right, func(e expr) { x.right = e }))
	case *orExpr:
		n := (&jnode{cls: "Or", line: x.line}).addOne("left", mirrorExpr(x.left, func(e expr) { x.left = e }))
		return n.addOne("right", mirrorExpr(x.right, func(e expr) { x.right = e }))
	case *notExpr:
		return (&jnode{cls: "Not", line: x.line}).addOne("node", mirrorExpr(x.e, func(e expr) { x.e = e }))
	case *negExpr:
		return (&jnode{cls: "Neg", line: x.line}).addOne("node", mirrorExpr(x.e, func(e expr) { x.e = e }))
	case *posExpr:
		return (&jnode{cls: "Pos", line: x.line}).addOne("node", mirrorExpr(x.e, func(e expr) { x.e = e }))
	case *concatExpr:
		return (&jnode{cls: "Concat", line: x.line}).addMany("nodes", mirrorList(x.items))
	case *compareExpr:
		ops := make([]*jnode, len(x.ops))
		for i, op := range x.ops {
			ops[i] = (&jnode{cls: "Operand", op: op}).addOne("expr", mirrorExpr(x.operands[i], func(e expr) { x.operands[i] = e }))
		}
		return (&jnode{cls: "Compare", line: x.line}).addOne("expr", mirrorExpr(x.first, func(e expr) { x.first = e })).addMany("ops", ops)
	}
	panic("jinja: unmirrored expression")
}
