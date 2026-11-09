package jinja

import "strings"

// The argument jinja2's pass decorators give each builtin filter and test before its value
var filterPassArgs = map[string]string{
	"attr": "environment", "first": "environment", "groupby": "environment", "join": "eval_context", "last": "environment",
	"list": "eval_context", "map": "context", "max": "environment", "min": "environment", "random": "context",
	"reject": "context", "rejectattr": "context", "replace": "eval_context", "select": "context", "selectattr": "context",
	"slice": "eval_context", "sort": "environment", "sum": "environment", "truncate": "environment", "unique": "environment",
	"urlize": "eval_context", "wordwrap": "environment", "xmlattr": "eval_context",
}

var testPassArgs = map[string]string{"filter": "environment", "test": "environment"}

func passArg(name string, isFilter bool) string {
	if isFilter {
		return filterPassArgs[name]
	}
	return testPassArgs[name]
}

// The code generated for each pass argument
var passArgCode = map[string]string{"context": "context", "eval_context": "context.eval_ctx", "environment": "environment"}

// The identifier transformers' generation extension is registered under
const generationExtension = "transformers.utils.chat_template_utils.AssistantTracker"

// Comparison operators as Python writes them
var pyCompareOps = map[string]string{"eq": "==", "ne": "!=", "gt": ">", "gteq": ">=", "lt": "<", "lteq": "<=", "in": "in", "notin": "not in"}

// Python operators of binary and unary nodes
var pyBinOps = map[string]string{"Add": "+", "Sub": "-", "Mul": "*", "Div": "/", "FloorDiv": "//", "Mod": "%", "Pow": "**", "And": "and", "Or": "or"}

// NodeVisitor.visit, dispatching to the CodeGenerator method of the node's class
func (g *codegen) visit(n *jnode, fr *cframe) {
	g.enter()
	defer g.leave()
	g.need(1)
	switch n.cls {
	case "Add", "Sub", "Mul", "Div", "FloorDiv", "Mod", "Pow", "And", "Or":
		g.optimized(n, fr, func(n *jnode) {
			g.write("(")
			g.visit(n.one("left"), fr)
			g.write(" " + pyBinOps[n.cls] + " ")
			g.visit(n.one("right"), fr)
			g.write(")")
		})
	case "Not", "Neg", "Pos":
		g.optimized(n, fr, func(n *jnode) {
			g.write("(" + map[string]string{"Not": "not ", "Neg": "-", "Pos": "+"}[n.cls])
			g.visit(n.one("node"), fr)
			g.write(")")
		})
	case "Concat":
		g.optimized(n, fr, func(n *jnode) { g.visitConcat(n, fr) })
	case "Compare":
		g.optimized(n, fr, func(n *jnode) {
			g.write("(")
			g.visit(n.one("expr"), fr)
			for _, op := range n.many("ops") {
				g.visit(op, fr)
			}
			g.write(")")
		})
	case "Getattr":
		g.optimized(n, fr, func(n *jnode) {
			g.write("environment.getattr(")
			g.visit(n.one("node"), fr)
			g.write(", " + strRepr(n.attr) + ")")
		})
	case "Getitem":
		g.optimized(n, fr, func(n *jnode) { g.visitGetitem(n, fr) })
	case "Filter":
		g.optimized(n, fr, func(n *jnode) { g.visitFilter(n, fr) })
	case "Test":
		g.optimized(n, fr, func(n *jnode) {
			g.filterTestCommon(n, fr, false, func() { g.visit(n.one("node"), fr) })
		})
	case "CondExpr":
		g.optimized(n, fr, func(n *jnode) { g.visitCondExpr(n, fr) })
	case "Call":
		g.optimized(n, fr, func(n *jnode) { g.visitCall(n, fr, false) })
	default:
		g.enter()
		defer g.leave()
		g.visitPlain(n, fr)
	}
}

// optimizeconst's wrapper, folding the node unless volatile and visiting a folded replacement instead
func (g *codegen) optimized(n *jnode, fr *cframe, body func(*jnode)) {
	g.enter()
	defer g.leave()
	if !g.f.volatile {
		if r := g.optimizeNode(n); r != n {
			g.need(1)
			r.setGo()
			g.visit(r, fr)
			return
		}
		g.need(2)
	}
	g.enter()
	defer g.leave()
	body(n)
}

// The visit methods jinja2 does not wrap in optimizeconst, running in their own frame
func (g *codegen) visitPlain(n *jnode, fr *cframe) {
	switch n.cls {
	case "Output":
		g.visitOutput(n, fr)
	case "Extends":
		g.visitExtends(n, fr)
	case "For":
		g.visitFor(n, fr)
	case "If":
		g.visitIf(n, fr)
	case "Macro":
		mfr, ref := g.macroBody(n, fr)
		g.newline(nil, 0)
		if fr.toplevel {
			if !strings.HasPrefix(n.name, "_") {
				g.write("context.exported_vars.add(" + strRepr(n.name) + ")")
			}
			g.writeline("context.vars["+strRepr(n.name)+"] = ", nil, 0)
		}
		g.write(g.ref(fr.sym, n.name) + " = ")
		g.macroDef(ref, mfr)
		g.recordMacro(n, ref, mfr)
	case "CallBlock":
		cfr, ref := g.macroBody(n, fr)
		g.writeline("caller = ", nil, 0)
		g.macroDef(ref, cfr)
		g.recordMacro(n, ref, cfr)
		g.startWrite(fr, n)
		g.optimized(n.one("call"), fr, func(c *jnode) { g.visitCall(c, fr, true) })
		g.endWrite(fr)
	case "FilterBlock":
		ffr := g.inner(fr, false)
		g.analyzeNode(ffr.sym, n, "body")
		g.enterFrame(ffr)
		g.buffer(ffr)
		g.blockvisit(n.many("body"), ffr)
		g.startWrite(fr, n)
		g.optimized(n.one("filter"), ffr, func(f *jnode) { g.visitFilter(f, ffr) })
		g.endWrite(fr)
		g.leaveFrame(ffr, false)
	case "With":
		wfr := g.inner(fr, false)
		g.analyzeNode(wfr.sym, n, "body")
		g.enterFrame(wfr)
		values := n.many("values")
		for i, t := range n.many("targets") {
			g.newline(nil, 0)
			g.visit(t, wfr)
			g.write(" = ")
			g.visit(values[i], fr)
		}
		g.blockvisit(n.many("body"), wfr)
		g.leaveFrame(wfr, false)
	case "Block":
		g.visitBlock(n, fr)
	case "Include":
		g.visitInclude(n, fr)
	case "Import":
		g.writeline(g.ref(fr.sym, n.name)+" = ", n, 0)
		if fr.toplevel {
			g.write("context.vars[" + strRepr(n.name) + "] = ")
		}
		g.importCommon(n, fr)
		if fr.toplevel && !strings.HasPrefix(n.name, "_") {
			g.writeline("context.exported_vars.discard("+strRepr(n.name)+")", nil, 0)
		}
	case "FromImport":
		g.visitFromImport(n, fr)
	case "Assign":
		g.visitAssign(n, fr)
	case "AssignBlock":
		g.visitAssignBlock(n, fr)
	case "Name":
		g.visitName(n, fr)
	case "NSRef":
		g.writeline(g.ref(fr.sym, n.name)+"["+strRepr(n.attr)+"]", nil, 0)
	case "Const":
		g.visitConst(n)
	case "TemplateData":
		var v Value
		if err := g.try(func() { v = g.asConst(n) }); err == errImpossible {
			g.write("(Markup if context.eval_ctx.autoescape else identity)(")
			g.writeRepr(n.value)
			g.write(")")
		} else if err != nil {
			g.raise(err)
		} else {
			g.writeRepr(v)
		}
	case "Tuple":
		g.write("(")
		items := n.many("items")
		for i, it := range items {
			if i > 0 {
				g.write(", ")
			}
			g.visit(it, fr)
		}
		if len(items) == 1 {
			g.write(",)")
		} else {
			g.write(")")
		}
	case "List":
		g.write("[")
		for i, it := range n.many("items") {
			if i > 0 {
				g.write(", ")
			}
			g.visit(it, fr)
		}
		g.write("]")
	case "Dict":
		g.write("{")
		for i, p := range n.many("items") {
			if i > 0 {
				g.write(", ")
			}
			g.visit(p.one("key"), fr)
			g.write(": ")
			g.visit(p.one("value"), fr)
		}
		g.write("}")
	case "Operand":
		g.write(" " + pyCompareOps[n.op] + " ")
		g.visit(n.one("expr"), fr)
	case "Slice":
		if s := n.one("start"); s != nil {
			g.visit(s, fr)
		}
		g.write(":")
		if s := n.one("stop"); s != nil {
			g.visit(s, fr)
		}
		if s := n.one("step"); s != nil {
			g.write(":")
			g.visit(s, fr)
		}
	case "Keyword":
		g.write(n.name + "=")
		g.visit(n.one("value"), fr)
	case "ExtensionAttribute":
		g.write("environment.extensions[" + strRepr(generationExtension) + "]." + n.name)
	case "Continue":
		g.writeline("continue", n, 0)
	case "Break":
		g.writeline("break", n, 0)
	case "Scope":
		sfr := g.inner(fr, false)
		g.analyzeNode(sfr.sym, n, "body")
		g.enterFrame(sfr)
		g.blockvisit(n.many("body"), sfr)
		g.leaveFrame(sfr, false)
	case "ScopedEvalContextModifier":
		g.visitScopedEval(n, fr)
	}
}

// Stores what macro_body learned on the parsed signature the renderer reads
func (g *codegen) recordMacro(n *jnode, ref *macroRef, fr *cframe) {
	var sig *signature
	switch x := n.src.(type) {
	case *macroStmt:
		sig = x.sig
	case *callBlockStmt:
		sig = x.sig
	case *generationStmt:
		return
	}
	sig.usesCaller, sig.usesKwargs, sig.usesVarargs = ref.accessesCaller, ref.accessesKwargs, ref.accessesVarargs
	for _, p := range sig.params {
		if p == "caller" {
			sig.explicitCaller = true
		}
	}
	sig.escape, sig.volatile = g.f.r.autoescape, g.f.volatile
	sig.qualname = ref.qualname
}

// The repr jinja2 writes for a constant, raising what Python's repr raises
func (g *codegen) reprValue(v Value) string {
	s, err := pyRepr(v)
	if err != nil {
		g.raise(err)
	}
	return s
}

func (g *codegen) visitConst(n *jnode) {
	g.need(1)
	v := n.value
	if err := checkDigits(v); err != nil {
		g.raise(err)
	}
	if name, bad := nonFiniteName(v); bad && n.set != nil {
		n.set(&nameErrorExpr{name: name})
	}
	g.writeRepr(v)
}

func (g *codegen) visitName(n *jnode, fr *cframe) {
	if n.ctx == "store" && (fr.toplevel || fr.loopFrame || fr.blockFrame) && len(g.assignStack) > 0 {
		g.assignStack[len(g.assignStack)-1][n.name] = true
	}
	r := g.ref(fr.sym, n.name)
	if n.ctx == "load" {
		l, ok := g.findLoad(fr.sym, r)
		if !(ok && l.action == loadParameter && !g.parameterIsUndeclared(r)) {
			g.write("(undefined(name=" + strRepr(n.name) + ") if " + r + " is missing else " + r + ")")
			return
		}
	}
	g.write(r)
}

func (g *codegen) visitConcat(n *jnode, fr *cframe) {
	name := "str_join"
	switch {
	case g.f.volatile:
		name = "(markup_join if context.eval_ctx.volatile else str_join)"
	case g.f.r.autoescape:
		name = "markup_join"
	}
	g.write(name + "((")
	for _, a := range n.many("nodes") {
		g.visit(a, fr)
		g.write(", ")
	}
	g.write("))")
}

func (g *codegen) visitGetitem(n *jnode, fr *cframe) {
	if arg := n.one("arg"); arg.cls == "Slice" {
		g.visit(n.one("node"), fr)
		g.write("[")
		g.visit(arg, fr)
		g.write("]")
		return
	}
	g.write("environment.getitem(")
	g.visit(n.one("node"), fr)
	g.write(", ")
	g.visit(n.one("arg"), fr)
	g.write(")")
}

// _filter_test_common's context manager around visiting a filter's or test's value
func (g *codegen) filterTestCommon(n *jnode, fr *cframe, isFilter bool, body func()) {
	g.need(2)
	g.enter()
	g.enter()
	ids, kind := g.tests, "test"
	known := false
	if isFilter {
		ids, kind = g.filters, "filter"
		_, known = builtinFilters[n.name]
	} else {
		_, known = builtinTests[n.name]
	}
	g.write(ids[n.name] + "(")
	if !known && !fr.softFrame {
		g.fail("No "+kind+" named "+strRepr(n.name)+".", n.line)
	}
	g.need(1)
	if known {
		if arg := passArg(n.name, isFilter); arg != "" {
			g.write(passArgCode[arg] + ", ")
		}
	}
	g.leave()
	g.leave()
	body()
	g.enter()
	g.enter()
	g.signature(n, fr, nil)
	g.write(")")
	g.leave()
	g.leave()
}

func (g *codegen) visitFilter(n *jnode, fr *cframe) {
	g.filterTestCommon(n, fr, true, func() {
		switch {
		case n.one("node") != nil:
			g.visit(n.one("node"), fr)
		case g.f.volatile:
			g.write("(Markup(concat(" + fr.buffer + ")) if context.eval_ctx.autoescape else concat(" + fr.buffer + "))")
		case g.f.r.autoescape:
			g.write("Markup(concat(" + fr.buffer + "))")
		default:
			g.write("concat(" + fr.buffer + ")")
		}
	})
}

func (g *codegen) visitCondExpr(n *jnode, fr *cframe) {
	fr = g.soft(fr)
	g.write("(")
	g.visit(n.one("expr1"), fr)
	g.write(" if ")
	g.visit(n.one("test"), fr)
	g.write(" else ")
	g.enter()
	if e := n.one("expr2"); e != nil {
		g.visit(e, fr)
	} else {
		g.write("cond_expr_undefined(\"the inline if-expression on " + g.position(n) + " evaluated to false and no else section was defined.\")")
	}
	g.leave()
	g.write(")")
}

func (g *codegen) visitCall(n *jnode, fr *cframe, forwardCaller bool) {
	g.write("environment.call(context, ")
	g.visit(n.one("node"), fr)
	var extra []extraKwarg
	if forwardCaller {
		extra = append(extra, extraKwarg{"caller", "caller"})
	}
	if fr.loopFrame {
		extra = append(extra, extraKwarg{"_loop_vars", "_loop_vars"})
	}
	if fr.blockFrame {
		extra = append(extra, extraKwarg{"_block_vars", "_block_vars"})
	}
	g.signature(n, fr, extra)
	g.write(")")
}

func (g *codegen) visitScopedEval(n *jnode, fr *cframe) {
	old := g.temporaryIdentifier()
	g.need(1)
	savedEscape, savedVolatile := g.f.r.autoescape, g.f.volatile
	g.writeline(old+" = context.eval_ctx.save()", nil, 0)
	g.enter()
	for _, kw := range n.many("options") {
		g.writeline("context.eval_ctx."+kw.name+" = ", nil, 0)
		value := kw.one("value")
		g.visit(value, fr)
		var v Value
		if err := g.try(func() { v = g.asConst(value) }); err == errImpossible {
			g.f.volatile = true
		} else if err != nil {
			g.raise(err)
		} else {
			g.f.r.autoescape = truthy(v)
		}
	}
	g.leave()
	for _, c := range n.many("body") {
		g.visit(c, fr)
	}
	g.need(1)
	g.f.r.autoescape, g.f.volatile = savedEscape, savedVolatile
	g.writeline("context.eval_ctx.revert("+old+")", nil, 0)
}
