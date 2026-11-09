package jinja

import "strings"

// Node.find_all as jinja2's recursive generator runs it, the consumer at the running frame and stopping when yield returns false
func (g *codegen) findAll(root *jnode, match func(*jnode) bool, yield func(*jnode) bool) {
	var walk func(n *jnode, at int) bool
	walk = func(n *jnode, at int) bool {
		g.check(at)
		for _, c := range n.children() {
			g.check(at + 2)
			if match(c) && !yield(c) {
				return false
			}
			if !walk(c, at+1) {
				return false
			}
		}
		g.check(at + 2)
		return true
	}
	walk(root, g.py.depth+1)
}

// Requires a frame at an absolute depth, for generator frames resumed above a suspended consumer
func (g *codegen) check(at int) {
	if err := g.py.check(at); err != nil {
		g.raise(err)
	}
}

func classIs(cls string) func(*jnode) bool { return func(n *jnode) bool { return n.cls == cls } }

// CodeGenerator.visit_Template, the module header, root function, block functions and trailer
func (g *codegen) visitTemplate(body []*jnode) {
	g.enter()
	defer g.leave()
	g.need(1)
	tmpl := (&jnode{cls: "Template", line: 1}).addMany("body", body)
	g.writeline("from jinja2.runtime import LoopContext, Macro, Markup, Namespace, TemplateNotFound, TemplateReference, TemplateRuntimeError, Undefined, escape, identity, internalcode, markup_join, missing, str_join", nil, 0)
	haveExtends := false
	g.enter()
	g.findAll(tmpl, classIs("Extends"), func(*jnode) bool {
		haveExtends = true
		return false
	})
	g.leave()
	g.findAll(tmpl, classIs("Block"), func(b *jnode) bool {
		if _, dup := g.blocks[b.name]; dup {
			g.fail("block "+strRepr(b.name)+" defined twice", b.line)
		}
		g.blocks[b.name] = b
		g.blockOrder = append(g.blockOrder, b.name)
		return true
	})
	g.findAll(tmpl, classIs("ImportedName"), func(*jnode) bool { return true })
	g.writeline("name = None", nil, 0)
	g.writeline(g.fn("root")+"(context, missing=missing, environment=environment):", nil, 1)
	g.indent()
	g.writeCommons()
	fr := g.newFrame(nil, 0, false)
	if g.findUndeclared(body, false, "self")["self"] {
		g.writeline(g.declareParameter(fr.sym, "self")+" = TemplateReference(context)", nil, 0)
		g.rootSelf = &bindStmt{name: "self", value: g.templateRef()}
	}
	g.analyzeNode(fr.sym, tmpl, "body")
	fr.toplevel, fr.rootlevel = true, true
	fr.requireOutputCheck = haveExtends && !g.knownExtends
	if haveExtends {
		g.writeline("parent_template = None", nil, 0)
	}
	g.enterFrame(fr)
	g.pullDependencies(body)
	g.defs = []string{"root"}
	g.blockvisit(body, fr)
	g.leaveFrame(fr, true)
	g.outdent(1)
	if haveExtends {
		if !g.knownExtends {
			g.indent()
			g.writeline("if parent_template is not None:", nil, 0)
		}
		g.indent()
		g.writeline("yield from parent_template.root_render_func(context)", nil, 0)
		if g.knownExtends {
			g.outdent(1)
		} else {
			g.outdent(2)
		}
	}
	for _, name := range g.blockOrder {
		b := g.blocks[name]
		g.writeline(g.fn("block_"+name)+"(context, missing=missing, environment=environment):", b, 1)
		g.indent()
		g.writeCommons()
		bfr := g.newFrame(nil, 0, false)
		bfr.blockFrame = true
		undeclared := g.findUndeclared(b.many("body"), false, "self", "super")
		if undeclared["self"] {
			g.writeline(g.declareParameter(bfr.sym, "self")+" = TemplateReference(context)", nil, 0)
			blk := b.src.(*blockStmt)
			blk.body = append([]stmt{&bindStmt{name: "self", value: g.templateRef()}}, blk.body...)
		}
		if undeclared["super"] {
			g.writeline(g.declareParameter(bfr.sym, "super")+" = context.super("+strRepr(name)+", block_"+name+")", nil, 0)
			blk := b.src.(*blockStmt)
			parentless := &Undefined{hint: "there is no parent block called " + strRepr(name) + ".", name: "super"}
			blk.body = append([]stmt{&bindStmt{name: "super", value: parentless}}, blk.body...)
		}
		g.analyzeNode(bfr.sym, b, "body")
		bfr.block = name
		g.writeline("_block_vars = {}", nil, 0)
		g.enterFrame(bfr)
		g.pullDependencies(b.many("body"))
		g.defs = []string{"block_" + name}
		g.blockvisit(b.many("body"), bfr)
		g.leaveFrame(bfr, true)
		g.outdent(1)
	}
	g.need(1)
	kv := make([]string, len(g.blockOrder))
	for i, name := range g.blockOrder {
		kv[i] = strRepr(name) + ": block_" + name
	}
	g.writeline("blocks = {"+strings.Join(kv, ", ")+"}", nil, 1)
	g.need(1)
	pairs := make([]string, len(g.debug))
	for i, d := range g.debug {
		pairs[i] = itoa(d[0]) + "=" + itoa(d[1])
	}
	g.writeline("debug_info = "+strRepr(strings.Join(pairs, "&")), nil, 0)
}

func (g *codegen) visitBlock(n *jnode, fr *cframe) {
	level := 0
	if fr.toplevel {
		if g.knownExtends {
			return
		}
		if g.extendsSoFar > 0 {
			g.writeline("if parent_template is None:", nil, 0)
			g.indent()
			level++
		}
	}
	var ctx string
	if n.scoped {
		ctx = g.deriveContext(fr)
	} else {
		ctx = g.contextRef()
	}
	if n.required {
		g.writeline("if len(context.blocks["+strRepr(n.name)+"]) <= 1:", n, 0)
		g.indent()
		g.writeline("raise TemplateRuntimeError(\"Required block "+strRepr(n.name)+" not found\")", n, 0)
		g.outdent(1)
	}
	if fr.buffer == "" {
		g.writeline("yield from context.blocks["+strRepr(n.name)+"][0]("+ctx+")", n, 0)
	} else {
		g.writeline("gen = context.blocks["+strRepr(n.name)+"][0]("+ctx+")", nil, 0)
		g.writeline("try:", nil, 0)
		g.indent()
		g.writeline(g.chooseAsync("async ", "")+"for event in gen:", n, 0)
		g.indent()
		g.simpleWrite("event", fr, nil)
		g.outdent(1)
		g.outdent(1)
		g.writeline("finally: "+g.chooseAsync("await gen.aclose()", "gen.close()"), nil, 0)
	}
	g.outdent(level)
}

func (g *codegen) visitExtends(n *jnode, fr *cframe) {
	if !fr.toplevel {
		g.fail("cannot use extend from a non top-level scope", n.line)
	}
	if g.extendsSoFar > 0 {
		if !g.knownExtends {
			g.writeline("if parent_template is not None:", nil, 0)
			g.indent()
		}
		g.writeline("raise TemplateRuntimeError(\"extended multiple times\")", nil, 0)
		if g.knownExtends {
			g.raise(errCompilerExit)
		}
		g.outdent(1)
	}
	g.writeline("parent_template = environment.get_template(", n, 0)
	g.visit(n.one("template"), fr)
	g.write(", None)")
	g.writeline("for name, parent_block in parent_template.blocks.items():", nil, 0)
	g.indent()
	g.writeline("context.blocks.setdefault(name, []).append(parent_block)", nil, 0)
	g.outdent(1)
	if fr.rootlevel {
		g.knownExtends = true
	}
	g.extendsSoFar++
}

// The environment method an include calls for its template expression, chosen from the unoptimized node
func includeFunc(t *jnode) string {
	switch t.cls {
	case "Const":
		switch t.value.(type) {
		case string, Markup:
			return "get_template"
		case *Tuple, *List:
			return "select_template"
		}
	case "Tuple", "List":
		return "select_template"
	}
	return "get_or_select_template"
}

func (g *codegen) visitInclude(n *jnode, fr *cframe) {
	if n.ignoreMissing {
		g.writeline("try:", nil, 0)
		g.indent()
	}
	fn := includeFunc(n.one("template"))
	n.src.(*loaderStmt).loadFunc = fn
	g.writeline("template = environment."+fn+"(", n, 0)
	g.visit(n.one("template"), fr)
	g.write(", None)")
	if n.ignoreMissing {
		g.outdent(1)
		g.writeline("except TemplateNotFound:", nil, 0)
		g.indent()
		g.writeline("pass", nil, 0)
		g.outdent(1)
		g.writeline("else:", nil, 0)
		g.indent()
	}
	loopBody := func() {
		g.enter()
		defer g.leave()
		g.indent()
		g.simpleWrite("event", fr, nil)
		g.outdent(1)
	}
	if n.withContext {
		g.writeline("gen = template.root_render_func(template.new_context(context.get_all(), True, "+g.dumpLocalContext(fr)+"))", nil, 0)
		g.writeline("try:", nil, 0)
		g.indent()
		g.writeline(g.chooseAsync("async ", "")+"for event in gen:", nil, 0)
		loopBody()
		g.outdent(1)
		g.writeline("finally: "+g.chooseAsync("await gen.aclose()", "gen.close()"), nil, 0)
	} else {
		g.writeline("yield from template._get_default_module()._body_stream", nil, 0)
	}
	if n.ignoreMissing {
		g.outdent(1)
	}
}

// CodeGenerator._import_common, the get_template call and module construction of import and from
func (g *codegen) importCommon(n *jnode, fr *cframe) {
	g.enter()
	defer g.leave()
	g.write(g.chooseAsync("await ", "") + "environment.get_template(")
	g.visit(n.one("template"), fr)
	g.write(", None).")
	if n.withContext {
		name := "make_module" + g.chooseAsync("_async", "")
		g.write(name + "(context.get_all(), True, " + g.dumpLocalContext(fr) + ")")
	} else {
		g.write("_get_default_module" + g.chooseAsync("_async", "") + "(context)")
	}
}

func (g *codegen) visitFromImport(n *jnode, fr *cframe) {
	g.newline(n, 0)
	g.write("included_template = ")
	g.importCommon(n, fr)
	var varNames, discarded []string
	for _, imp := range n.names {
		alias := imp.alias
		if alias == "" {
			alias = imp.name
		}
		g.writeline(g.ref(fr.sym, alias)+" = getattr(included_template, "+strRepr(imp.name)+", missing)", nil, 0)
		g.writeline("if "+g.ref(fr.sym, alias)+" is missing:", nil, 0)
		g.indent()
		position := strings.NewReplacer("{", "{{", "}", "}}").Replace(g.position(n))
		message := "the template {included_template.__name__!r} (imported on " + position + ") does not export the requested name " + strRepr(imp.name)
		g.writeline(g.ref(fr.sym, alias)+" = undefined(f"+strRepr(message)+", name="+strRepr(imp.name)+")", nil, 0)
		g.outdent(1)
		if fr.toplevel {
			varNames = append(varNames, alias)
			if !strings.HasPrefix(alias, "_") {
				discarded = append(discarded, alias)
			}
		}
	}
	if len(varNames) == 1 {
		g.writeline("context.vars["+strRepr(varNames[0])+"] = "+g.ref(fr.sym, varNames[0]), nil, 0)
	} else if len(varNames) > 1 {
		g.need(1)
		kv := make([]string, len(varNames))
		for i, name := range varNames {
			kv[i] = strRepr(name) + ": " + g.ref(fr.sym, name)
		}
		g.writeline("context.vars.update({"+strings.Join(kv, ", ")+"})", nil, 0)
	}
	if len(discarded) == 1 {
		g.writeline("context.exported_vars.discard("+strRepr(discarded[0])+")", nil, 0)
	} else if len(discarded) > 1 {
		reprs := make([]string, len(discarded))
		for i, name := range discarded {
			reprs[i] = strRepr(name)
		}
		g.writeline("context.exported_vars.difference_update(("+strings.Join(reprs, ", ")+"))", nil, 0)
	}
}

func (g *codegen) visitFor(n *jnode, fr *cframe) {
	loopFr := g.inner(fr, false)
	loopFr.loopFrame = true
	testFr := g.inner(fr, false)
	elseFr := g.inner(fr, false)
	extended := n.recursive
	if !extended {
		g.need(1)
		extended = g.findUndeclared(n.many("body"), true, "loop")["loop"]
	}
	if !extended {
		g.enter()
		g.findAll(n, classIs("Block"), func(b *jnode) bool {
			extended = b.scoped
			return !extended
		})
		g.leave()
	}
	loopRef := ""
	n.src.(*forStmt).extended = extended
	if extended {
		loopRef = g.declareParameter(loopFr.sym, "loop")
	}
	g.analyzeNode(loopFr.sym, n, "body")
	elseBody := n.many("else_")
	if len(elseBody) > 0 {
		g.analyzeNode(elseFr.sym, n, "else")
	}
	filterFunc := ""
	if test := n.one("test"); test != nil {
		filterFunc = g.temporaryIdentifier()
		g.analyzeNode(testFr.sym, n, "test")
		g.writeline(g.fn(filterFunc)+"(fiter):", test, 0)
		g.indent()
		g.enterFrame(testFr)
		for _, l := range testFr.sym.loads {
			if l.action == loadResolve {
				n.src.(*forStmt).testResolves = true
			}
		}
		g.writeline(g.chooseAsync("async for ", "for "), nil, 0)
		g.visit(n.one("target"), loopFr)
		g.write(" in ")
		g.write(g.chooseAsync("auto_aiter(fiter)", "fiter"))
		g.write(":")
		g.indent()
		g.writeline("if ", test, 0)
		g.visit(test, testFr)
		g.write(":")
		g.indent()
		g.writeline("yield ", nil, 0)
		g.visit(n.one("target"), loopFr)
		g.outdent(3)
		g.leaveFrame(testFr, true)
	}
	if n.recursive {
		g.writeline(g.fn("loop")+"(reciter, loop_render_func, depth=0):", n, 0)
		g.indent()
		g.buffer(loopFr)
		elseFr.buffer = loopFr.buffer
		g.defs = append(g.defs, "loop")
		defer func() { g.defs = g.defs[:len(g.defs)-1] }()
	}
	if extended {
		g.writeline(loopRef+" = missing", nil, 0)
	}
	g.findAll(n, classIs("Name"), func(name *jnode) bool {
		if name.ctx == "store" && name.name == "loop" {
			g.fail("Can't assign to special loop variable in for-loop target", name.line)
		}
		return true
	})
	indicator := ""
	if len(elseBody) > 0 {
		indicator = g.temporaryIdentifier()
		g.writeline(indicator+" = 1", nil, 0)
	}
	g.writeline(g.chooseAsync("async for ", "for "), n, 0)
	g.visit(n.one("target"), loopFr)
	if extended {
		g.write(", " + loopRef + " in " + g.chooseAsync("Async", "") + "LoopContext(")
	} else {
		g.write(" in ")
	}
	if filterFunc != "" {
		g.write(filterFunc + "(")
	}
	if n.recursive {
		g.write("reciter")
	} else {
		g.visit(n.one("iter"), fr)
	}
	if filterFunc != "" {
		g.write(")")
	}
	switch {
	case n.recursive:
		g.write(", undefined, loop_render_func, depth):")
	case extended:
		g.write(", undefined):")
	default:
		g.write(":")
	}
	g.indent()
	g.enterFrame(loopFr)
	g.writeline("_loop_vars = {}", nil, 0)
	g.blockvisit(n.many("body"), loopFr)
	if len(elseBody) > 0 {
		g.writeline(indicator+" = 0", nil, 0)
	}
	g.outdent(1)
	g.leaveFrame(loopFr, n.recursive && len(elseBody) == 0)
	if len(elseBody) > 0 {
		g.writeline("if "+indicator+":", nil, 0)
		g.indent()
		g.enterFrame(elseFr)
		g.blockvisit(elseBody, elseFr)
		g.leaveFrame(elseFr, false)
		g.outdent(1)
	}
	if n.recursive {
		g.returnBufferContents(loopFr, false)
		g.outdent(1)
		g.startWrite(fr, n)
		g.write(g.chooseAsync("await ", "") + "loop(")
		g.visit(n.one("iter"), fr)
		g.write(", loop)")
		g.endWrite(fr)
	}
	if len(g.assignStack) > 0 {
		for name := range loopFr.sym.stores {
			delete(g.assignStack[len(g.assignStack)-1], name)
		}
	}
}

func (g *codegen) visitIf(n *jnode, fr *cframe) {
	ifFr := g.soft(fr)
	g.writeline("if ", n, 0)
	g.visit(n.one("test"), ifFr)
	g.write(":")
	g.indent()
	g.blockvisit(n.many("body"), ifFr)
	g.outdent(1)
	for _, e := range n.many("elif_") {
		g.writeline("elif ", e, 0)
		g.visit(e.one("test"), ifFr)
		g.write(":")
		g.indent()
		g.blockvisit(e.many("body"), ifFr)
		g.outdent(1)
	}
	if els := n.many("else_"); len(els) > 0 {
		g.writeline("else:", nil, 0)
		g.indent()
		g.blockvisit(els, ifFr)
		g.outdent(1)
	}
}

func (g *codegen) visitAssign(n *jnode, fr *cframe) {
	g.pushAssignTracking()
	seen := map[string]bool{}
	g.findAll(n, classIs("NSRef"), func(ns *jnode) bool {
		if seen[ns.name] {
			return true
		}
		seen[ns.name] = true
		r := g.ref(fr.sym, ns.name)
		g.writeline("if not isinstance("+r+", Namespace):", nil, 0)
		g.indent()
		g.writeline("raise TemplateRuntimeError(\"cannot assign attribute on non-namespace object\")", nil, 0)
		g.outdent(1)
		return true
	})
	g.newline(n, 0)
	g.visit(n.one("target"), fr)
	g.write(" = ")
	g.visit(n.one("node"), fr)
	g.popAssignTracking(fr)
}

func (g *codegen) visitAssignBlock(n *jnode, fr *cframe) {
	g.pushAssignTracking()
	bfr := g.inner(fr, false)
	bfr.requireOutputCheck = false
	g.analyzeNode(bfr.sym, n, "body")
	g.enterFrame(bfr)
	g.buffer(bfr)
	g.blockvisit(n.many("body"), bfr)
	g.newline(n, 0)
	g.visit(n.one("target"), fr)
	g.write(" = (Markup if context.eval_ctx.autoescape else identity)(")
	if f := n.one("filter"); f != nil {
		g.optimized(f, bfr, func(f *jnode) { g.visitFilter(f, bfr) })
	} else {
		g.write("concat(" + bfr.buffer + ")")
	}
	g.write(")")
	g.popAssignTracking(fr)
	g.leaveFrame(bfr, false)
}

// The finalize information visit_Output builds once, a namedtuple on its first use
func (g *codegen) makeFinalize() {
	g.enter()
	defer g.leave()
	if !g.finalized {
		g.need(1)
		g.finalized = true
	}
}

func (g *codegen) visitOutput(n *jnode, fr *cframe) {
	if fr.requireOutputCheck {
		if g.knownExtends {
			return
		}
		g.writeline("if parent_template is None:", nil, 0)
		g.indent()
	}
	g.makeFinalize()
	// Each item is a run of constant text or one node evaluated at render time
	type item struct {
		consts []string
		node   *jnode
	}
	var body []item
	for _, child := range n.many("nodes") {
		var text string
		err := g.try(func() { text = g.outputConst(child) })
		if err != nil {
			body = append(body, item{node: child})
			continue
		}
		if child.cls != "TemplateData" && child.set != nil {
			child.set(&constExpr{v: Markup(text), line: child.line})
		}
		if len(body) > 0 && body[len(body)-1].node == nil {
			body[len(body)-1].consts = append(body[len(body)-1].consts, text)
		} else {
			body = append(body, item{consts: []string{text}})
		}
	}
	if fr.buffer != "" {
		if len(body) == 1 {
			g.writeline(fr.buffer+".append(", nil, 0)
		} else {
			g.writeline(fr.buffer+".extend((", nil, 0)
		}
		g.indent()
	}
	for _, it := range body {
		if it.node == nil {
			g.need(1)
			val := strings.Join(it.consts, "")
			if fr.buffer == "" {
				g.writeline("yield ", nil, 0)
				g.writeRepr(val)
			} else {
				g.writeline("", nil, 0)
				g.writeRepr(val)
				g.write(",")
			}
			continue
		}
		if fr.buffer == "" {
			g.writeline("yield ", it.node, 0)
		} else {
			g.newline(it.node, 0)
		}
		g.enter()
		switch {
		case g.f.volatile:
			g.write("(escape if context.eval_ctx.autoescape else str)(")
		case g.f.r.autoescape:
			g.write("escape(")
		default:
			g.write("str(")
		}
		g.leave()
		g.visit(it.node, fr)
		g.enter()
		g.write(")")
		g.leave()
		if fr.buffer != "" {
			g.write(",")
		}
	}
	if fr.buffer != "" {
		g.outdent(1)
		if len(body) == 1 {
			g.writeline(")", nil, 0)
		} else {
			g.writeline("))", nil, 0)
		}
	}
	if fr.requireOutputCheck {
		g.outdent(1)
	}
}

// CodeGenerator._output_child_to_const, the finalized text of a constant output child
func (g *codegen) outputConst(child *jnode) string {
	g.enter()
	defer g.leave()
	v := g.asConst(child)
	if g.f.r.autoescape {
		g.need(g.f.r.markupEscapeFrames(v))
		m, err := escapeValue(v)
		if err != nil {
			g.raise(err)
		}
		v = m
	}
	if child.cls != "TemplateData" {
		g.need(1)
	}
	s, err := pyStr(v)
	if err != nil {
		g.raise(err)
	}
	return s
}
