package jinja

import (
	"fmt"
	"strings"
)

// Statement nodes, each with the template line jinja2 records for it
type (
	// Template data, cont when it continues the output node the previous item opened
	textStmt struct {
		text string
		line int
		cont bool
	}
	outputStmt struct {
		expr expr
		line int
		cont bool
	}
	forStmt struct {
		target    expr
		iter      expr
		body      []stmt
		orelse    []stmt
		test      expr
		recursive bool
		line      int
		// Whether jinja2 iterates through a LoopContext, decided by the code generator
		extended bool
		// Whether the loop filter function resolves context names on entry, decided by the code generator
		testResolves bool
	}
	ifStmt struct {
		test   expr
		body   []stmt
		elifs  []*ifStmt
		orelse []stmt
		line   int
	}
	setStmt struct {
		target, value expr
		line          int
	}
	setBlockStmt struct {
		target expr
		filter expr
		body   []stmt
		line   int
	}
	macroStmt struct {
		name string
		sig  *signature
		line int
	}
	callBlockStmt struct {
		call *callExpr
		sig  *signature
		line int
	}
	filterBlockStmt struct {
		filter expr
		body   []stmt
		line   int
	}
	withStmt struct {
		targets []expr
		values  []expr
		body    []stmt
		line    int
	}
	blockStmt struct {
		name     string
		scoped   bool
		required bool
		body     []stmt
		line     int
	}
	autoescapeStmt struct {
		value expr
		body  []stmt
		line  int
	}
	// An extends, include, import or from tag with the options its tag accepts
	loaderStmt struct {
		tag      string
		template expr
		// The environment method an include calls, chosen by the code generator from the parsed template node
		loadFunc      string
		ignoreMissing bool
		withContext   bool
		target        string
		names         []importName
		line          int
	}
	generationStmt struct {
		body []stmt
		line int
	}
	breakStmt    struct{ line int }
	continueStmt struct{ line int }
)

// A name a from tag imports and the alias it binds, empty when it binds the name itself
type importName struct{ name, alias string }

// A macro or caller signature with its body and the special names the body reads
type signature struct {
	params         []string
	paramLines     []int
	defaults       []expr
	body           []stmt
	usesCaller     bool
	usesKwargs     bool
	usesVarargs    bool
	explicitCaller bool
	// The autoescape setting the body was compiled under, and whether only render time decides it
	escape, volatile bool
	// The Python qualified name of the def jinja2 generates for the body
	qualname string
}

// Expression nodes, each with the template line jinja2 records for it
type (
	constExpr struct {
		v Value
		// Set for folded containers, which Python rebuilds from their literal on every evaluation
		fresh bool
		line  int
	}
	nameExpr struct {
		name string
		line int
	}
	nsRefExpr struct {
		name, attr string
		line       int
	}
	tupleExpr struct {
		items []expr
		line  int
	}
	listExpr struct {
		items []expr
		line  int
	}
	dictExpr struct {
		keys, vals []expr
		line       int
	}
	getattrExpr struct {
		obj  expr
		attr string
		line int
	}
	getitemExpr struct {
		obj, arg expr
		line     int
	}
	sliceExpr struct {
		start, stop, step expr
		line              int
	}
	callExpr struct {
		fn   expr
		args callArgs
		line int
	}
	filterExpr struct {
		node expr
		name string
		args callArgs
		line int
	}
	testExpr struct {
		node expr
		name string
		args callArgs
		line int
	}
	condExpr struct {
		test, expr1, expr2 expr
		line               int
	}
	binExpr struct {
		op          string
		left, right expr
		line        int
	}
	andExpr struct {
		left, right expr
		line        int
	}
	orExpr struct {
		left, right expr
		line        int
	}
	notExpr struct {
		e    expr
		line int
	}
	negExpr struct {
		e    expr
		line int
	}
	posExpr struct {
		e    expr
		line int
	}
	concatExpr struct {
		items []expr
		line  int
	}
	compareExpr struct {
		first    expr
		ops      []string
		operands []expr
		line     int
	}
	bufferExpr struct{}
)

// Positional, keyword, star and double star arguments of a call, filter or test
type callArgs struct {
	args      []expr
	kwNames   []string
	kwVals    []expr
	dynArgs   expr
	dynKwargs expr
}

type stmt = any
type expr = any

var statementKeywords = map[string]bool{
	"for": true, "if": true, "block": true, "extends": true, "print": true, "macro": true,
	"include": true, "from": true, "import": true, "set": true, "with": true, "autoescape": true,
}

var compareOps = map[string]bool{"eq": true, "ne": true, "lt": true, "lteq": true, "gt": true, "gteq": true}

type parser struct {
	toks []token
	pos  int
	// The end tags each open subparse waits for and the statement tags still open, as jinja2 tracks them
	endStack [][]string
	tagStack []string
	// Python frames on the stack while the method running now executes, and whether the next token waits in the stream's pushed queue
	py     *pyStack
	pushed bool
}

// Parses template source into statements as jinja2's parser does
func parse(source string) ([]stmt, error) {
	py := newCompileStack()
	body, err := parseTemplate(source, py)
	if err != nil {
		return nil, err
	}
	g, err := compileTree(body, py)
	if err != nil {
		return nil, err
	}
	if g.rootSelf != nil {
		body = append([]stmt{g.rootSelf}, body...)
	}
	return body, nil
}

// Runs Environment._parse, the Parser constructor priming the stream and Parser.parse building the body
func parseTemplate(source string, py *pyStack) ([]stmt, error) {
	p := &parser{toks: tokenize(source), py: py}
	if err := p.construct(); err != nil {
		return nil, err
	}
	if err := py.enter(); err != nil {
		return nil, err
	}
	defer py.leave()
	body, err := p.subparse(nil)
	if err != nil {
		return nil, err
	}
	if err := py.need(parseTailFrames); err != nil {
		return nil, err
	}
	return body, nil
}

// Frames from Parser.__init__ up to the first TokenStream.__next__, through _tokenize, tokenize and TokenStream.__init__
const streamInitFrames = 4

// Frames above Parser.parse for the Template node and set_environment walking it with iter_child_nodes and iter_fields
const parseTailFrames = 3

// Runs Parser.__init__, whose TokenStream produces the first token with the stream frames above it
func (p *parser) construct() error {
	if err := p.py.enter(); err != nil {
		return err
	}
	defer p.py.leave()
	return p.produce(p.py.depth+streamInitFrames, 0)
}

func (p *parser) cur() token { return p.toks[p.pos] }

// Produces the token after the current one as TokenStream.__next__ running at frame depth at does
func (p *parser) produce(at, idx int) error {
	if err := p.py.check(at); err != nil {
		return err
	}
	t := p.toks[idx]
	if err := p.py.check(at + t.peak); err != nil {
		return err
	}
	if t.kind == tokError {
		return t.value.(error)
	}
	return nil
}

// TokenStream.__next__ at frame depth at, making the next token current and returning the old one
func (p *parser) advance(at int) (token, error) {
	t := p.toks[p.pos]
	if err := p.py.check(at); err != nil {
		return t, err
	}
	if p.pushed {
		p.pushed = false
		if t.kind != tokEOF {
			p.pos++
		}
		return t, nil
	}
	if t.kind == tokEOF {
		return t, nil
	}
	if err := p.produce(at, p.pos+1); err != nil {
		return t, err
	}
	p.pos++
	return t, nil
}

// next(self.stream) called from the running method
func (p *parser) next() (token, error) { return p.advance(p.py.depth + 1) }

// TokenStream.look, producing the next token into the pushed queue without moving
func (p *parser) look() (token, error) {
	at := p.py.depth + 1
	if err := p.py.check(at); err != nil {
		return token{}, err
	}
	if err := p.py.check(at + 1); err != nil {
		return token{}, err
	}
	if !p.pushed && p.cur().kind != tokEOF {
		if err := p.produce(at+1, p.pos+1); err != nil {
			return token{}, err
		}
	}
	p.pushed = true
	return p.toks[min(p.pos+1, len(p.toks)-1)], nil
}

// TokenStream.skip, n calls of next from inside skip
func (p *parser) skip(n int) error {
	at := p.py.depth + 1
	if err := p.py.check(at); err != nil {
		return err
	}
	for range n {
		if _, err := p.advance(at + 1); err != nil {
			return err
		}
	}
	return nil
}

// Token.test called from the running method at the given extra depth
func (p *parser) test(t token, want string, extra int) (bool, error) {
	if err := p.py.check(p.py.depth + extra + 1); err != nil {
		return false, err
	}
	if kind, val, ok := strings.Cut(want, ":"); ok {
		return t.kind == kind && t.value == val, nil
	}
	return t.kind == want, nil
}

// Token.test_any through its generator expression
func (p *parser) testAny(t token, wants ...string) (bool, error) {
	if err := p.py.check(p.py.depth + 2); err != nil {
		return false, err
	}
	for _, w := range wants {
		ok, err := p.test(t, w, 2)
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// TokenStream.skip_if through next_if
func (p *parser) skipIf(want string) (bool, error) {
	if err := p.py.check(p.py.depth + 2); err != nil {
		return false, err
	}
	ok, err := p.test(p.cur(), want, 2)
	if err != nil || !ok {
		return false, err
	}
	_, err = p.advance(p.py.depth + 3)
	return true, err
}

// TokenStream.expect, consuming a token of the given kind or a name with the value after the separator
func (p *parser) expect(want string) (token, error) {
	at := p.py.depth + 1
	if err := p.py.check(at); err != nil {
		return token{}, err
	}
	t := p.cur()
	ok, err := p.test(t, want, 1)
	if err != nil {
		return t, err
	}
	if ok {
		return p.advance(at + 1)
	}
	if err := p.py.need(1 + describeExprFrames(want)); err != nil {
		return t, err
	}
	if t.kind == tokEOF {
		return t, p.raise(1, "TemplateSyntaxError", t.line, "unexpected end of template, expected %s.", strRepr(describeExpr(want)))
	}
	if err := p.py.need(1 + describeFrames(t)); err != nil {
		return t, err
	}
	return t, p.raise(1, "TemplateSyntaxError", t.line, "expected token %s, got %s", strRepr(describeExpr(want)), strRepr(describe(t)))
}

// Builds a template exception extra frames above the running method, its two __init__ frames pushed first
func (p *parser) raise(extra int, kind string, line int, format string, args ...any) error {
	if err := p.py.need(extra + exceptionInitFrames); err != nil {
		return err
	}
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...), Line: line}
}

// Python frames TemplateSyntaxError.__init__ and TemplateError.__init__ push
const exceptionInitFrames = 2

// Parser.fail at the given line, or the current token's line when line is 0
func (p *parser) fail(line int, format string, args ...any) error {
	if err := p.py.need(1); err != nil {
		return err
	}
	if line == 0 {
		line = p.cur().line
	}
	return p.raise(1, "TemplateSyntaxError", line, format, args...)
}

// A node constructor's Node.__init__ frame
func (p *parser) node() error { return p.py.need(1) }

// Frames describe_token pushes for a token, one more for kinds _describe_token_type names
func describeFrames(t token) int {
	if t.kind == tokName {
		return 1
	}
	return 2
}

// Frames describe_token_expr pushes, one more unless a name expression returns its value directly
func describeExprFrames(want string) int {
	if kind, _, ok := strings.Cut(want, ":"); ok && kind == tokName {
		return 1
	}
	return 2
}

// jinja2's describe_token, a name's own text and any other token's kind description
func describe(t token) string {
	if t.kind == tokName {
		return t.value.(string)
	}
	return describeKind(t.kind)
}

// jinja2's describe_token_expr, the name after the separator or the kind's description
func describeExpr(want string) string {
	if kind, val, ok := strings.Cut(want, ":"); ok && kind == tokName {
		return val
	}
	kind, _, _ := strings.Cut(want, ":")
	return describeKind(kind)
}

// Token kinds jinja2 describes in words rather than by their operator text
var kindDescriptions = map[string]string{
	tokBlockBegin: "begin of statement block", tokBlockEnd: "end of statement block",
	tokVariableBegin: "begin of print statement", tokVariableEnd: "end of print statement",
	tokData: "template data / text", tokEOF: "end of template",
}

// jinja2's _describe_token_type, an operator's text, a described kind, or the kind itself
func describeKind(kind string) string {
	for _, op := range operatorList {
		if op.kind == kind {
			return op.text
		}
	}
	if d, ok := kindDescriptions[kind]; ok {
		return d
	}
	return kind
}

// Whether the stream still has tokens, TokenStream.__bool__ called from the running method
func (p *parser) more() (bool, error) {
	if err := p.py.need(1); err != nil {
		return false, err
	}
	return p.pushed || p.cur().kind != tokEOF, nil
}

// Parses template data, prints and statements until one of the end tokens
func (p *parser) subparse(ends []string) ([]stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	if ends != nil {
		p.endStack = append(p.endStack, ends)
		defer func() { p.endStack = p.endStack[:len(p.endStack)-1] }()
	}
	var body []stmt
	inData := false
	flush := func() error {
		if !inData {
			return p.py.need(1)
		}
		inData = false
		return p.py.need(2)
	}
	for {
		more, err := p.more()
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
		switch t := p.cur(); t.kind {
		case tokData:
			if err := p.node(); err != nil {
				return nil, err
			}
			body = append(body, &textStmt{text: t.value.(string), line: t.line, cont: inData})
			inData = true
			if _, err := p.next(); err != nil {
				return nil, err
			}
		case tokVariableBegin:
			if _, err := p.next(); err != nil {
				return nil, err
			}
			e, err := p.parseTuple(tupleOpts{withCondexpr: true})
			if err != nil {
				return nil, err
			}
			body = append(body, &outputStmt{expr: e, line: lineOf(e), cont: inData})
			inData = true
			if _, err := p.expect(tokVariableEnd); err != nil {
				return nil, err
			}
		case tokBlockBegin:
			if err := flush(); err != nil {
				return nil, err
			}
			if _, err := p.next(); err != nil {
				return nil, err
			}
			if ends != nil {
				at, err := p.testAny(p.cur(), ends...)
				if err != nil {
					return nil, err
				}
				if at {
					return body, nil
				}
			}
			s, err := p.parseStatement()
			if err != nil {
				return nil, err
			}
			if list, ok := s.([]stmt); ok {
				body = append(body, list...)
			} else {
				body = append(body, s)
			}
			if _, err := p.expect(tokBlockEnd); err != nil {
				return nil, err
			}
		default:
			return nil, &Error{Kind: "AssertionError", Msg: "internal parsing error"}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return body, nil
}

// Statement tags dispatched by name, the rest reaching extensions or failing as unknown
func (p *parser) parseStatement() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t := p.cur()
	if t.kind != tokName {
		return nil, p.fail(t.line, "tag name expected")
	}
	name := t.value.(string)
	p.tagStack = append(p.tagStack, name)
	pop := true
	defer func() {
		if pop {
			p.tagStack = p.tagStack[:len(p.tagStack)-1]
		}
	}()
	if statementKeywords[name] {
		switch name {
		case "for":
			return p.parseFor()
		case "if":
			return p.parseIf()
		case "block":
			return p.parseBlock()
		case "print":
			return p.parsePrint()
		case "macro":
			return p.parseMacro()
		case "set":
			return p.parseSet()
		case "with":
			return p.parseWith()
		case "autoescape":
			return p.parseAutoescape()
		case "extends":
			return p.parseExtends()
		case "include":
			return p.parseInclude()
		case "import":
			return p.parseImport()
		case "from":
			return p.parseFrom()
		}
	}
	switch name {
	case "call":
		return p.parseCallBlock()
	case "filter":
		return p.parseFilterBlock()
	case "break", "continue":
		return p.parseLoopControl()
	case "generation":
		return p.parseGeneration()
	}
	p.tagStack = p.tagStack[:len(p.tagStack)-1]
	pop = false
	if err := p.py.need(1); err != nil {
		return nil, err
	}
	return nil, p.failTag(1, name, p.endStack, t.line)
}

// LoopControlExtension.parse, a break or continue node at its tag's line
func (p *parser) parseLoopControl() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	if t.value == "break" {
		return &breakStmt{line: t.line}, nil
	}
	return &continueStmt{line: t.line}, nil
}

// Frames AssistantTracker.parse pushes above itself for call_method with attr and its node, and set_lineno walking the block
const generationTailFrames = 3

// transformers' AssistantTracker.parse, the generation body as a call block
func (p *parser) parseGeneration() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	body, err := p.parseStatements([]string{"name:endgeneration"}, true)
	if err != nil {
		return nil, err
	}
	if err := p.py.need(generationTailFrames); err != nil {
		return nil, err
	}
	return &generationStmt{body: body, line: t.line}, nil
}

// jinja2's _fail_ut_eof message for an unknown tag or early end, raised extra frames above the running method
func (p *parser) failTag(extra int, name string, stack [][]string, line int) error {
	tag := func(e string) string {
		_, v, _ := strings.Cut(e, ":")
		return v
	}
	if err := p.py.need(extra + 2); err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, ends := range stack {
		for _, e := range ends {
			expected[tag(e)] = true
		}
	}
	msg := []string{"Unexpected end of template."}
	if name != "" {
		msg[0] = "Encountered unknown tag " + strRepr(name) + "."
	}
	if len(stack) > 0 {
		last := stack[len(stack)-1]
		names := make([]string, len(last))
		for i, e := range last {
			names[i] = strRepr(tag(e))
		}
		looking := strings.Join(names, " or ")
		if name != "" && expected[name] {
			msg = append(msg, "You probably made a nesting mistake. Jinja is expecting this tag, but currently looking for "+looking+".")
		} else {
			msg = append(msg, "Jinja was looking for the following tags: "+looking+".")
		}
	}
	if len(p.tagStack) > 0 {
		msg = append(msg, "The innermost block that needs to be closed is "+strRepr(p.tagStack[len(p.tagStack)-1])+".")
	}
	if line == 0 {
		line = p.cur().line
	}
	return p.raise(extra+2, "TemplateSyntaxError", line, "%s", strings.Join(msg, " "))
}

// Parses a block body after the current tag up to one of the end tags
func (p *parser) parseStatements(ends []string, dropNeedle bool) ([]stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	if _, err := p.skipIf("colon"); err != nil {
		return nil, err
	}
	if _, err := p.expect(tokBlockEnd); err != nil {
		return nil, err
	}
	body, err := p.subparse(ends)
	if err != nil {
		return nil, err
	}
	if p.cur().kind == tokEOF {
		if err := p.py.need(1); err != nil {
			return nil, err
		}
		return nil, p.failTag(1, "", append(append([][]string(nil), p.endStack...), ends), 0)
	}
	if dropNeedle {
		if _, err := p.next(); err != nil {
			return nil, err
		}
	}
	return body, nil
}

func (p *parser) parseSet() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	target, err := p.parseAssignTarget(assignOpts{withNamespace: true})
	if err != nil {
		return nil, err
	}
	assign, err := p.skipIf("assign")
	if err != nil {
		return nil, err
	}
	if assign {
		value, err := p.parseTuple(tupleOpts{withCondexpr: true})
		if err != nil {
			return nil, err
		}
		return &setStmt{target: target, value: value, line: t.line}, p.node()
	}
	filter, err := p.parseFilter(nil, false)
	if err != nil {
		return nil, err
	}
	body, err := p.parseStatements([]string{"name:endset"}, true)
	if err != nil {
		return nil, err
	}
	return &setBlockStmt{target: target, filter: filter, body: body, line: t.line}, p.node()
}

func (p *parser) parseFor() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.expect("name:for")
	if err != nil {
		return nil, err
	}
	n := &forStmt{line: t.line}
	if n.target, err = p.parseAssignTarget(assignOpts{extraEnd: true}); err != nil {
		return nil, err
	}
	if _, err := p.expect("name:in"); err != nil {
		return nil, err
	}
	if n.iter, err = p.parseTuple(tupleOpts{extraEnd: true}); err != nil {
		return nil, err
	}
	hasTest, err := p.skipIf("name:if")
	if err != nil {
		return nil, err
	}
	if hasTest {
		if n.test, err = p.parseExpression(true); err != nil {
			return nil, err
		}
	}
	if n.recursive, err = p.skipIf("name:recursive"); err != nil {
		return nil, err
	}
	if n.body, err = p.parseStatements([]string{"name:endfor", "name:else"}, false); err != nil {
		return nil, err
	}
	end, err := p.next()
	if err != nil {
		return nil, err
	}
	if end.value != "endfor" {
		if n.orelse, err = p.parseStatements([]string{"name:endfor"}, true); err != nil {
			return nil, err
		}
	}
	return n, p.node()
}

func (p *parser) parseIf() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.expect("name:if")
	if err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	result := &ifStmt{line: t.line}
	node := result
	for {
		if node.test, err = p.parseTuple(tupleOpts{}); err != nil {
			return nil, err
		}
		if node.body, err = p.parseStatements([]string{"name:elif", "name:else", "name:endif"}, false); err != nil {
			return nil, err
		}
		t, err := p.next()
		if err != nil {
			return nil, err
		}
		isElif, err := p.test(t, "name:elif", 0)
		if err != nil {
			return nil, err
		}
		if isElif {
			if err := p.node(); err != nil {
				return nil, err
			}
			node = &ifStmt{line: p.cur().line}
			result.elifs = append(result.elifs, node)
			continue
		}
		isElse, err := p.test(t, "name:else", 0)
		if err != nil {
			return nil, err
		}
		if isElse {
			if result.orelse, err = p.parseStatements([]string{"name:endif"}, true); err != nil {
				return nil, err
			}
		}
		return result, nil
	}
}

func (p *parser) parseWith() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	n := &withStmt{line: t.line}
	for p.cur().kind != tokBlockEnd {
		if len(n.targets) > 0 {
			if _, err := p.expect("comma"); err != nil {
				return nil, err
			}
		}
		target, err := p.parseAssignTarget(assignOpts{})
		if err != nil {
			return nil, err
		}
		if err := p.py.need(setCtxFrames); err != nil {
			return nil, err
		}
		n.targets = append(n.targets, target)
		if _, err := p.expect("assign"); err != nil {
			return nil, err
		}
		v, err := p.parseExpression(true)
		if err != nil {
			return nil, err
		}
		n.values = append(n.values, v)
	}
	if n.body, err = p.parseStatements([]string{"name:endwith"}, true); err != nil {
		return nil, err
	}
	return n, nil
}

// Frames Node.set_ctx pushes walking a target, its own and the iter_child_nodes and iter_fields generators
const setCtxFrames = 3

func (p *parser) parseAutoescape() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	v, err := p.parseExpression(true)
	if err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	body, err := p.parseStatements([]string{"name:endautoescape"}, true)
	if err != nil {
		return nil, err
	}
	return &autoescapeStmt{value: v, body: body, line: t.line}, p.node()
}

func (p *parser) parseBlock() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	nameTok, err := p.expect(tokName)
	if err != nil {
		return nil, err
	}
	n := &blockStmt{name: nameTok.value.(string), line: t.line}
	if n.scoped, err = p.skipIf("name:scoped"); err != nil {
		return nil, err
	}
	if n.required, err = p.skipIf("name:required"); err != nil {
		return nil, err
	}
	if p.cur().kind == "sub" {
		return nil, p.fail(0, "Block names in Jinja have to be valid Python identifiers and may not contain hyphens, use an underscore instead.")
	}
	if n.body, err = p.parseStatements([]string{"name:endblock"}, true); err != nil {
		return nil, err
	}
	if n.required {
		if err := p.checkRequired(n.body); err != nil {
			return nil, err
		}
	}
	if _, err := p.skipIf("name:" + n.name); err != nil {
		return nil, err
	}
	return n, nil
}

// Refuses a required block body holding anything but whitespace output, the any generator checking each output node
func (p *parser) checkRequired(body []stmt) error {
	for i, s := range body {
		if outputCont(s) {
			continue
		}
		if !isOutputItem(s) {
			return p.fail(0, "Required blocks can only contain comments or whitespace")
		}
		if err := p.py.need(1); err != nil {
			return err
		}
		for j := i; j < len(body) && (j == i || outputCont(body[j])); j++ {
			if t, ok := body[j].(*textStmt); !ok || strings.TrimFunc(t.text, isPySpace) != "" {
				return p.fail(0, "Required blocks can only contain comments or whitespace")
			}
		}
	}
	return nil
}

// Whether a statement is template data or a print, an item of one of jinja2's output nodes
func isOutputItem(s stmt) bool {
	switch s.(type) {
	case *textStmt, *outputStmt:
		return true
	}
	return false
}

// Whether an output item continues the output node an earlier item opened
func outputCont(s stmt) bool {
	switch x := s.(type) {
	case *textStmt:
		return x.cont
	case *outputStmt:
		return x.cont
	}
	return false
}

func (p *parser) parseExtends() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	n := &loaderStmt{tag: "extends", line: t.line}
	n.template, err = p.parseExpression(true)
	return n, err
}

func (p *parser) parseInclude() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	n := &loaderStmt{tag: "include", line: t.line}
	if n.template, err = p.parseExpression(true); err != nil {
		return nil, err
	}
	ignore, err := p.test(p.cur(), "name:ignore", 0)
	if err != nil {
		return nil, err
	}
	if ignore {
		next, err := p.look()
		if err != nil {
			return nil, err
		}
		if n.ignoreMissing, err = p.test(next, "name:missing", 0); err != nil {
			return nil, err
		}
		if n.ignoreMissing {
			if err := p.skip(2); err != nil {
				return nil, err
			}
		}
	}
	return n, p.parseImportContext(n, true)
}

func (p *parser) parseImport() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	n := &loaderStmt{tag: "import", line: t.line}
	if n.template, err = p.parseExpression(true); err != nil {
		return nil, err
	}
	if _, err := p.expect("name:as"); err != nil {
		return nil, err
	}
	target, err := p.parseAssignTarget(assignOpts{nameOnly: true})
	if err != nil {
		return nil, err
	}
	n.target = target.(*nameExpr).name
	return n, p.parseImportContext(n, false)
}

// Reads an optional with context or without context suffix
func (p *parser) parseImportContext(n *loaderStmt, def bool) error {
	if err := p.py.enter(); err != nil {
		return err
	}
	defer p.py.leave()
	n.withContext = def
	ok, err := p.testAny(p.cur(), "name:with", "name:without")
	if err != nil || !ok {
		return err
	}
	next, err := p.look()
	if err != nil {
		return err
	}
	if ok, err = p.test(next, "name:context", 0); err != nil || !ok {
		return err
	}
	t, err := p.next()
	if err != nil {
		return err
	}
	n.withContext = t.value == "with"
	return p.skip(1)
}

func (p *parser) parseFrom() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	n := &loaderStmt{tag: "from", line: t.line}
	if n.template, err = p.parseExpression(true); err != nil {
		return nil, err
	}
	if _, err := p.expect("name:import"); err != nil {
		return nil, err
	}
	// The nested parse_context closure, true once it consumed a with or without context suffix
	context := func() (bool, error) {
		if err := p.py.enter(); err != nil {
			return false, err
		}
		defer p.py.leave()
		if v, _ := p.cur().value.(string); v != "with" && v != "without" {
			return false, nil
		}
		next, err := p.look()
		if err != nil {
			return false, err
		}
		if ok, err := p.test(next, "name:context", 0); err != nil || !ok {
			return false, err
		}
		t, err := p.next()
		if err != nil {
			return false, err
		}
		n.withContext = t.value == "with"
		return true, p.skip(1)
	}
	for {
		if len(n.names) > 0 {
			if _, err := p.expect("comma"); err != nil {
				return nil, err
			}
		}
		if p.cur().kind != tokName {
			_, err := p.expect(tokName)
			return nil, err
		}
		done, err := context()
		if err != nil {
			return nil, err
		}
		if done {
			break
		}
		target, err := p.parseAssignTarget(assignOpts{nameOnly: true})
		if err != nil {
			return nil, err
		}
		name := target.(*nameExpr)
		if strings.HasPrefix(name.name, "_") {
			if err := p.py.need(1); err != nil {
				return nil, err
			}
			return nil, p.raise(1, "TemplateAssertionError", name.line, "names starting with an underline can not be imported")
		}
		imp := importName{name: name.name}
		as, err := p.skipIf("name:as")
		if err != nil {
			return nil, err
		}
		if as {
			alias, err := p.parseAssignTarget(assignOpts{nameOnly: true})
			if err != nil {
				return nil, err
			}
			imp.alias = alias.(*nameExpr).name
		}
		n.names = append(n.names, imp)
		if done, err = context(); err != nil {
			return nil, err
		}
		if done || p.cur().kind != "comma" {
			break
		}
	}
	return n, nil
}

func (p *parser) parsePrint() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	if _, err := p.next(); err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	var out []stmt
	for p.cur().kind != tokBlockEnd {
		if len(out) > 0 {
			if _, err := p.expect("comma"); err != nil {
				return nil, err
			}
		}
		e, err := p.parseExpression(true)
		if err != nil {
			return nil, err
		}
		out = append(out, &outputStmt{expr: e, line: lineOf(e), cont: len(out) > 0})
	}
	return out, nil
}

// Parses a parenthesized parameter list with defaults
func (p *parser) parseSignature() (*signature, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	sig := &signature{}
	if _, err := p.expect("lparen"); err != nil {
		return nil, err
	}
	for p.cur().kind != "rparen" {
		if len(sig.params) > 0 {
			if _, err := p.expect("comma"); err != nil {
				return nil, err
			}
		}
		arg, err := p.parseAssignTarget(assignOpts{nameOnly: true})
		if err != nil {
			return nil, err
		}
		if err := p.py.need(setCtxFrames); err != nil {
			return nil, err
		}
		hasDefault, err := p.skipIf("assign")
		if err != nil {
			return nil, err
		}
		if hasDefault {
			d, err := p.parseExpression(true)
			if err != nil {
				return nil, err
			}
			sig.defaults = append(sig.defaults, d)
		} else if len(sig.defaults) > 0 {
			return nil, p.fail(0, "non-default argument follows default argument")
		}
		name := arg.(*nameExpr)
		sig.params = append(sig.params, name.name)
		sig.paramLines = append(sig.paramLines, name.line)
	}
	_, err := p.expect("rparen")
	return sig, err
}

func (p *parser) parseCallBlock() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	sig := &signature{}
	if p.cur().kind == "lparen" {
		if sig, err = p.parseSignature(); err != nil {
			return nil, err
		}
	}
	e, err := p.parseExpression(true)
	if err != nil {
		return nil, err
	}
	call, ok := e.(*callExpr)
	if !ok {
		return nil, p.fail(t.line, "expected call")
	}
	if sig.body, err = p.parseStatements([]string{"name:endcall"}, true); err != nil {
		return nil, err
	}
	return &callBlockStmt{call: call, sig: sig, line: t.line}, nil
}

func (p *parser) parseFilterBlock() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	f, err := p.parseFilter(nil, true)
	if err != nil {
		return nil, err
	}
	body, err := p.parseStatements([]string{"name:endfilter"}, true)
	return &filterBlockStmt{filter: f, body: body, line: t.line}, err
}

func (p *parser) parseMacro() (stmt, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	name, err := p.parseAssignTarget(assignOpts{nameOnly: true})
	if err != nil {
		return nil, err
	}
	sig, err := p.parseSignature()
	if err != nil {
		return nil, err
	}
	if sig.body, err = p.parseStatements([]string{"name:endmacro"}, true); err != nil {
		return nil, err
	}
	return &macroStmt{name: name.(*nameExpr).name, sig: sig, line: t.line}, nil
}

// How parse_assign_target was called, a tuple target unless nameOnly
type assignOpts struct {
	nameOnly, extraEnd, withNamespace bool
}

// Parses an assignment target, a name, a namespace attribute or a tuple of them
func (p *parser) parseAssignTarget(o assignOpts) (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	var target expr
	if o.nameOnly {
		t, err := p.expect(tokName)
		if err != nil {
			return nil, err
		}
		if err := p.node(); err != nil {
			return nil, err
		}
		target = &nameExpr{name: t.value.(string), line: t.line}
	} else {
		var err error
		if target, err = p.parseTuple(tupleOpts{simplified: true, extraEnd: o.extraEnd, withNamespace: o.withNamespace}); err != nil {
			return nil, err
		}
		if err := p.py.need(setCtxFrames); err != nil {
			return nil, err
		}
	}
	if err := p.py.need(canAssignFrames(target)); err != nil {
		return nil, err
	}
	if !canAssign(target) {
		return nil, p.fail(lineOf(target), "can't assign to %s", strRepr(nodeTypeName(target)))
	}
	return target, nil
}

// Frames can_assign pushes, one per tuple level down to its first refusing or deepest item
func canAssignFrames(e expr) int {
	t, ok := e.(*tupleExpr)
	if !ok {
		return 1
	}
	deepest := 1
	for _, it := range t.items {
		deepest = max(deepest, 1+canAssignFrames(it))
		if !canAssign(it) {
			break
		}
	}
	return deepest
}

// The template line jinja2 records for an expression node
func lineOf(e expr) int {
	switch x := e.(type) {
	case *constExpr:
		return x.line
	case *nameExpr:
		return x.line
	case *nsRefExpr:
		return x.line
	case *tupleExpr:
		return x.line
	case *listExpr:
		return x.line
	case *dictExpr:
		return x.line
	case *getattrExpr:
		return x.line
	case *getitemExpr:
		return x.line
	case *sliceExpr:
		return x.line
	case *callExpr:
		return x.line
	case *filterExpr:
		return x.line
	case *testExpr:
		return x.line
	case *condExpr:
		return x.line
	case *binExpr:
		return x.line
	case *andExpr:
		return x.line
	case *orExpr:
		return x.line
	case *notExpr:
		return x.line
	case *negExpr:
		return x.line
	case *posExpr:
		return x.line
	case *concatExpr:
		return x.line
	case *compareExpr:
		return x.line
	}
	return 0
}

func canAssign(e expr) bool {
	switch x := e.(type) {
	case *nameExpr, *nsRefExpr:
		return true
	case *tupleExpr:
		for _, it := range x.items {
			if !canAssign(it) {
				return false
			}
		}
		return true
	}
	return false
}

func nodeTypeName(e expr) string {
	switch e.(type) {
	case *constExpr:
		return "const"
	case *getattrExpr:
		return "getattr"
	case *getitemExpr:
		return "getitem"
	case *callExpr:
		return "call"
	case *listExpr:
		return "list"
	case *dictExpr:
		return "dict"
	}
	return strings.TrimSuffix(strings.TrimPrefix(fmt.Sprintf("%T", e), "*jinja."), "Expr")
}

func (p *parser) parseExpression(withCondexpr bool) (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	if withCondexpr {
		return p.parseCondexpr()
	}
	return p.parseOr()
}

func (p *parser) parseCondexpr() (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	line := p.cur().line
	e1, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	for {
		ok, err := p.skipIf("name:if")
		if err != nil {
			return nil, err
		}
		if !ok {
			return e1, nil
		}
		e2, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		var e3 expr
		hasElse, err := p.skipIf("name:else")
		if err != nil {
			return nil, err
		}
		if hasElse {
			if e3, err = p.parseCondexpr(); err != nil {
				return nil, err
			}
		}
		if err := p.node(); err != nil {
			return nil, err
		}
		e1 = &condExpr{test: e2, expr1: e1, expr2: e3, line: line}
		line = p.cur().line
	}
}

// Parses a left associative chain of and or or operands
func (p *parser) parseLogic(op string, operand func() (expr, error)) (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	line := p.cur().line
	left, err := operand()
	if err != nil {
		return nil, err
	}
	for {
		ok, err := p.skipIf("name:" + op)
		if err != nil {
			return nil, err
		}
		if !ok {
			return left, nil
		}
		right, err := operand()
		if err != nil {
			return nil, err
		}
		if err := p.node(); err != nil {
			return nil, err
		}
		if op == "or" {
			left = &orExpr{left: left, right: right, line: line}
		} else {
			left = &andExpr{left: left, right: right, line: line}
		}
		line = p.cur().line
	}
}

func (p *parser) parseOr() (expr, error) { return p.parseLogic("or", p.parseAnd) }

func (p *parser) parseAnd() (expr, error) { return p.parseLogic("and", p.parseNot) }

func (p *parser) parseNot() (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	isNot, err := p.test(p.cur(), "name:not", 0)
	if err != nil {
		return nil, err
	}
	if !isNot {
		return p.parseCompare()
	}
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	e, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	return &notExpr{e: e, line: t.line}, p.node()
}

func (p *parser) parseCompare() (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	line := p.cur().line
	e, err := p.parseMath1()
	if err != nil {
		return nil, err
	}
	cmp := &compareExpr{first: e}
	for {
		var op string
		if k := p.cur().kind; compareOps[k] {
			if _, err := p.next(); err != nil {
				return nil, err
			}
			op = k
		} else if in, err := p.skipIf("name:in"); err != nil {
			return nil, err
		} else if in {
			op = "in"
		} else if isNot, err := p.test(p.cur(), "name:not", 0); err != nil {
			return nil, err
		} else if !isNot {
			break
		} else if next, err := p.look(); err != nil {
			return nil, err
		} else if notIn, err := p.test(next, "name:in", 0); err != nil {
			return nil, err
		} else if !notIn {
			break
		} else {
			if err := p.skip(2); err != nil {
				return nil, err
			}
			op = "notin"
		}
		operand, err := p.parseMath1()
		if err != nil {
			return nil, err
		}
		if err := p.node(); err != nil {
			return nil, err
		}
		cmp.ops = append(cmp.ops, op)
		cmp.operands = append(cmp.operands, operand)
		line = p.cur().line
	}
	if len(cmp.ops) == 0 {
		return e, nil
	}
	cmp.line = line
	return cmp, p.node()
}

// Parses a left associative chain of binary operators of one precedence level
func (p *parser) parseBinary(ops map[string]bool, operand func() (expr, error)) (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	line := p.cur().line
	left, err := operand()
	if err != nil {
		return nil, err
	}
	for ops[p.cur().kind] {
		op := p.cur().kind
		if _, err := p.next(); err != nil {
			return nil, err
		}
		right, err := operand()
		if err != nil {
			return nil, err
		}
		if err := p.node(); err != nil {
			return nil, err
		}
		left = &binExpr{op: op, left: left, right: right, line: line}
		line = p.cur().line
	}
	return left, nil
}

var (
	math1Ops = map[string]bool{"add": true, "sub": true}
	math2Ops = map[string]bool{"mul": true, "div": true, "floordiv": true, "mod": true}
	powOps   = map[string]bool{"pow": true}
)

func (p *parser) parseMath1() (expr, error) { return p.parseBinary(math1Ops, p.parseConcat) }

func (p *parser) parseConcat() (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	line := p.cur().line
	first, err := p.parseMath2()
	if err != nil {
		return nil, err
	}
	items := []expr{first}
	for p.cur().kind == "tilde" {
		if _, err := p.next(); err != nil {
			return nil, err
		}
		e, err := p.parseMath2()
		if err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	if len(items) == 1 {
		return first, nil
	}
	return &concatExpr{items: items, line: line}, p.node()
}

func (p *parser) parseMath2() (expr, error) { return p.parseBinary(math2Ops, p.parsePow) }

func (p *parser) parsePow() (expr, error) {
	return p.parseBinary(powOps, func() (expr, error) { return p.parseUnary(true) })
}

func (p *parser) parseUnary(withFilter bool) (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t := p.cur()
	var node expr
	switch t.kind {
	case "sub", "add":
		if _, err := p.next(); err != nil {
			return nil, err
		}
		e, err := p.parseUnary(false)
		if err != nil {
			return nil, err
		}
		if err := p.node(); err != nil {
			return nil, err
		}
		if t.kind == "sub" {
			node = &negExpr{e: e, line: t.line}
		} else {
			node = &posExpr{e: e, line: t.line}
		}
	default:
		var err error
		if node, err = p.parsePrimary(false); err != nil {
			return nil, err
		}
	}
	node, err := p.parsePostfix(node)
	if err != nil {
		return nil, err
	}
	if withFilter {
		return p.parseFilterExpr(node)
	}
	return node, nil
}

func (p *parser) parsePrimary(withNamespace bool) (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t := p.cur()
	switch t.kind {
	case tokName:
		if _, err := p.next(); err != nil {
			return nil, err
		}
		switch name := t.value.(string); name {
		case "true", "True", "false", "False":
			return &constExpr{v: name == "true" || name == "True", line: t.line}, p.node()
		case "none", "None":
			return &constExpr{v: none, line: t.line}, p.node()
		default:
			if withNamespace && p.cur().kind == "dot" {
				if _, err := p.next(); err != nil {
					return nil, err
				}
				attr, err := p.expect(tokName)
				if err != nil {
					return nil, err
				}
				return &nsRefExpr{name: name, attr: attr.value.(string), line: t.line}, p.node()
			}
			return &nameExpr{name: name, line: t.line}, p.node()
		}
	case tokString:
		if _, err := p.next(); err != nil {
			return nil, err
		}
		var b strings.Builder
		b.WriteString(t.value.(string))
		for p.cur().kind == tokString {
			b.WriteString(p.cur().value.(string))
			if _, err := p.next(); err != nil {
				return nil, err
			}
		}
		return &constExpr{v: b.String(), line: t.line}, p.node()
	case tokInteger, tokFloat:
		if _, err := p.next(); err != nil {
			return nil, err
		}
		return &constExpr{v: t.value, line: t.line}, p.node()
	case "lparen":
		if _, err := p.next(); err != nil {
			return nil, err
		}
		e, err := p.parseTuple(tupleOpts{withCondexpr: true, explicitParens: true})
		if err != nil {
			return nil, err
		}
		if _, err := p.expect("rparen"); err != nil {
			return nil, err
		}
		return e, nil
	case "lbracket":
		return p.parseList()
	case "lbrace":
		return p.parseDict()
	}
	if err := p.py.need(describeFrames(t)); err != nil {
		return nil, err
	}
	return nil, p.fail(t.line, "unexpected %s", strRepr(describe(t)))
}

// How parse_tuple was called
type tupleOpts struct {
	simplified, withCondexpr, extraEnd, explicitParens, withNamespace bool
}

// Whether a tuple ends at the current token, by delimiters alone since jinja2 passes extra rules to test_any unpacked as one tuple
func (p *parser) isTupleEnd(extraEnd bool) (bool, error) {
	if err := p.py.need(1); err != nil {
		return false, err
	}
	switch p.cur().kind {
	case tokVariableEnd, tokBlockEnd, "rparen":
		return true, nil
	}
	if extraEnd {
		return false, p.py.need(4)
	}
	return false, nil
}

// Parses an expression or a comma separated tuple of them
func (p *parser) parseTuple(o tupleOpts) (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	line := p.cur().line
	// The nested parse closure, one frame above parse_tuple
	parse := func() (expr, error) {
		if err := p.py.enter(); err != nil {
			return nil, err
		}
		defer p.py.leave()
		if o.simplified {
			return p.parsePrimary(o.withNamespace)
		}
		return p.parseExpression(o.withCondexpr)
	}
	var items []expr
	isTuple := false
	for {
		if len(items) > 0 {
			if _, err := p.expect("comma"); err != nil {
				return nil, err
			}
		}
		end, err := p.isTupleEnd(o.extraEnd)
		if err != nil {
			return nil, err
		}
		if end {
			break
		}
		e, err := parse()
		if err != nil {
			return nil, err
		}
		items = append(items, e)
		if p.cur().kind != "comma" {
			break
		}
		isTuple = true
		line = p.cur().line
	}
	if !isTuple {
		if len(items) > 0 {
			return items[0], nil
		}
		if !o.explicitParens {
			t := p.cur()
			if err := p.py.need(describeFrames(t)); err != nil {
				return nil, err
			}
			return nil, p.fail(0, "Expected an expression, got %s", strRepr(describe(t)))
		}
	}
	return &tupleExpr{items: items, line: line}, p.node()
}

func (p *parser) parseList() (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.expect("lbracket")
	if err != nil {
		return nil, err
	}
	l := &listExpr{line: t.line}
	for p.cur().kind != "rbracket" {
		if len(l.items) > 0 {
			if _, err := p.expect("comma"); err != nil {
				return nil, err
			}
		}
		if p.cur().kind == "rbracket" {
			break
		}
		e, err := p.parseExpression(true)
		if err != nil {
			return nil, err
		}
		l.items = append(l.items, e)
	}
	if _, err := p.expect("rbracket"); err != nil {
		return nil, err
	}
	return l, p.node()
}

func (p *parser) parseDict() (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.expect("lbrace")
	if err != nil {
		return nil, err
	}
	d := &dictExpr{line: t.line}
	for p.cur().kind != "rbrace" {
		if len(d.keys) > 0 {
			if _, err := p.expect("comma"); err != nil {
				return nil, err
			}
		}
		if p.cur().kind == "rbrace" {
			break
		}
		k, err := p.parseExpression(true)
		if err != nil {
			return nil, err
		}
		if _, err := p.expect("colon"); err != nil {
			return nil, err
		}
		v, err := p.parseExpression(true)
		if err != nil {
			return nil, err
		}
		if err := p.node(); err != nil {
			return nil, err
		}
		d.keys = append(d.keys, k)
		d.vals = append(d.vals, v)
	}
	if _, err := p.expect("rbrace"); err != nil {
		return nil, err
	}
	return d, p.node()
}

func (p *parser) parsePostfix(node expr) (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	for {
		var err error
		switch p.cur().kind {
		case "dot", "lbracket":
			node, err = p.parseSubscript(node)
		case "lparen":
			node, err = p.parseCall(node)
		default:
			return node, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func (p *parser) parseFilterExpr(node expr) (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	for {
		var err error
		switch {
		case p.cur().kind == "pipe":
			node, err = p.parseFilter(node, false)
		case p.cur().isName("is"):
			node, err = p.parseTest(node)
		case p.cur().kind == "lparen":
			node, err = p.parseCall(node)
		default:
			return node, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func (p *parser) parseSubscript(node expr) (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if t.kind == "dot" {
		attr := p.cur()
		if _, err := p.next(); err != nil {
			return nil, err
		}
		switch attr.kind {
		case tokName:
			return &getattrExpr{obj: node, attr: attr.value.(string), line: t.line}, p.node()
		case tokInteger:
			if err := p.node(); err != nil {
				return nil, err
			}
			return &getitemExpr{obj: node, arg: &constExpr{v: attr.value, line: attr.line}, line: t.line}, p.node()
		}
		return nil, p.fail(attr.line, "expected name or number")
	}
	var args []expr
	for p.cur().kind != "rbracket" {
		if len(args) > 0 {
			if _, err := p.expect("comma"); err != nil {
				return nil, err
			}
		}
		a, err := p.parseSubscribed()
		if err != nil {
			return nil, err
		}
		args = append(args, a)
	}
	if _, err := p.expect("rbracket"); err != nil {
		return nil, err
	}
	arg := expr(nil)
	if len(args) == 1 {
		arg = args[0]
	} else {
		if err := p.node(); err != nil {
			return nil, err
		}
		arg = &tupleExpr{items: args, line: t.line}
	}
	return &getitemExpr{obj: node, arg: arg, line: t.line}, p.node()
}

func (p *parser) parseSubscribed() (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	s := &sliceExpr{line: p.cur().line}
	if p.cur().kind == "colon" {
		if _, err := p.next(); err != nil {
			return nil, err
		}
	} else {
		e, err := p.parseExpression(true)
		if err != nil {
			return nil, err
		}
		if p.cur().kind != "colon" {
			return e, nil
		}
		if _, err := p.next(); err != nil {
			return nil, err
		}
		s.start = e
	}
	switch p.cur().kind {
	case "colon", "rbracket", "comma":
	default:
		e, err := p.parseExpression(true)
		if err != nil {
			return nil, err
		}
		s.stop = e
	}
	if p.cur().kind == "colon" {
		if _, err := p.next(); err != nil {
			return nil, err
		}
		if k := p.cur().kind; k != "rbracket" && k != "comma" {
			e, err := p.parseExpression(true)
			if err != nil {
				return nil, err
			}
			s.step = e
		}
	}
	return s, p.node()
}

func (p *parser) parseCallArgs() (callArgs, error) {
	var ca callArgs
	if err := p.py.enter(); err != nil {
		return ca, err
	}
	defer p.py.leave()
	open, err := p.expect("lparen")
	if err != nil {
		return ca, err
	}
	// The nested ensure closure, failing at the opening parenthesis's line
	ensure := func(ok bool) error {
		if err := p.py.need(1); err != nil {
			return err
		}
		if ok {
			return nil
		}
		if err := p.py.need(2); err != nil {
			return err
		}
		return p.raise(2, "TemplateSyntaxError", open.line, "invalid syntax for function call expression")
	}
	requireComma := false
	for p.cur().kind != "rparen" {
		if requireComma {
			if _, err := p.expect("comma"); err != nil {
				return ca, err
			}
			if p.cur().kind == "rparen" {
				break
			}
		}
		switch p.cur().kind {
		case "mul":
			if err := ensure(ca.dynArgs == nil && ca.dynKwargs == nil); err != nil {
				return ca, err
			}
			if _, err := p.next(); err != nil {
				return ca, err
			}
			if ca.dynArgs, err = p.parseExpression(true); err != nil {
				return ca, err
			}
		case "pow":
			if err := ensure(ca.dynKwargs == nil); err != nil {
				return ca, err
			}
			if _, err := p.next(); err != nil {
				return ca, err
			}
			if ca.dynKwargs, err = p.parseExpression(true); err != nil {
				return ca, err
			}
		default:
			keyword := false
			if p.cur().kind == tokName {
				next, err := p.look()
				if err != nil {
					return ca, err
				}
				keyword = next.kind == "assign"
			}
			if keyword {
				if err := ensure(ca.dynKwargs == nil); err != nil {
					return ca, err
				}
				key := p.cur().value.(string)
				if err := p.skip(2); err != nil {
					return ca, err
				}
				v, err := p.parseExpression(true)
				if err != nil {
					return ca, err
				}
				if err := p.node(); err != nil {
					return ca, err
				}
				ca.kwNames = append(ca.kwNames, key)
				ca.kwVals = append(ca.kwVals, v)
			} else {
				if err := ensure(ca.dynArgs == nil && ca.dynKwargs == nil && len(ca.kwNames) == 0); err != nil {
					return ca, err
				}
				e, err := p.parseExpression(true)
				if err != nil {
					return ca, err
				}
				ca.args = append(ca.args, e)
			}
		}
		requireComma = true
	}
	_, err = p.expect("rparen")
	return ca, err
}

func (p *parser) parseCall(node expr) (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	line := p.cur().line
	args, err := p.parseCallArgs()
	if err != nil {
		return nil, err
	}
	return &callExpr{fn: node, args: args, line: line}, p.node()
}

// Parses a pipe filter chain, or a bare chain when it starts inline as filter blocks do
func (p *parser) parseFilter(node expr, startInline bool) (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	for p.cur().kind == "pipe" || startInline {
		if !startInline {
			if _, err := p.next(); err != nil {
				return nil, err
			}
		}
		t, err := p.expect(tokName)
		if err != nil {
			return nil, err
		}
		name, err := p.dottedName(t)
		if err != nil {
			return nil, err
		}
		f := &filterExpr{node: node, name: name, line: t.line}
		if node == nil {
			f.node = &bufferExpr{}
		}
		if p.cur().kind == "lparen" {
			if f.args, err = p.parseCallArgs(); err != nil {
				return nil, err
			}
		}
		if err := p.node(); err != nil {
			return nil, err
		}
		node = f
		startInline = false
	}
	return node, nil
}

// A filter or test name extended by the dotted parts that follow it
func (p *parser) dottedName(t token) (string, error) {
	name := t.value.(string)
	for p.cur().kind == "dot" {
		if _, err := p.next(); err != nil {
			return "", err
		}
		part, err := p.expect(tokName)
		if err != nil {
			return "", err
		}
		name += "." + part.value.(string)
	}
	return name, nil
}

// Token kinds that start a test's single argument written without parentheses
var testArgKinds = map[string]bool{tokName: true, tokString: true, tokInteger: true, tokFloat: true, "lparen": true, "lbracket": true, "lbrace": true}

func (p *parser) parseTest(node expr) (expr, error) {
	if err := p.py.enter(); err != nil {
		return nil, err
	}
	defer p.py.leave()
	is, err := p.next()
	if err != nil {
		return nil, err
	}
	negated, err := p.test(p.cur(), "name:not", 0)
	if err != nil {
		return nil, err
	}
	if negated {
		if _, err := p.next(); err != nil {
			return nil, err
		}
	}
	t, err := p.expect(tokName)
	if err != nil {
		return nil, err
	}
	name, err := p.dottedName(t)
	if err != nil {
		return nil, err
	}
	te := &testExpr{node: node, name: name, line: is.line}
	if p.cur().kind == "lparen" {
		if te.args, err = p.parseCallArgs(); err != nil {
			return nil, err
		}
	} else if testArgKinds[p.cur().kind] {
		stop, err := p.testAny(p.cur(), "name:else", "name:or", "name:and")
		if err != nil {
			return nil, err
		}
		if !stop {
			chained, err := p.test(p.cur(), "name:is", 0)
			if err != nil {
				return nil, err
			}
			if chained {
				return nil, p.fail(0, "You cannot chain multiple tests with is")
			}
			arg, err := p.parsePrimary(false)
			if err != nil {
				return nil, err
			}
			if arg, err = p.parsePostfix(arg); err != nil {
				return nil, err
			}
			te.args.args = []expr{arg}
		}
	}
	if err := p.node(); err != nil {
		return nil, err
	}
	if negated {
		return &notExpr{e: te, line: is.line}, p.node()
	}
	return te, nil
}
