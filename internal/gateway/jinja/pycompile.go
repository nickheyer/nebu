package jinja

import (
	"fmt"
	"strings"
)

// CPython limits on jinja2's generated module, MAXLEVEL brackets, MAXINDENT indentation levels and CO_MAXBLOCKS nested blocks
const (
	pyMaxLevel  = 200
	pyMaxIndent = 100
	pyMaxBlocks = 21
)

// A token of the generated module, its kind name, op, str or num, and the physical line it starts on
type ptok struct {
	kind, text string
	line       int
}

// A logical line of the generated module with its indentation column
type pline struct {
	line, col int
	toks      []ptok
}

// A SyntaxError or IndentationError as Python's str prints it for the generated module
func pySyntaxError(kind, msg string, line int) error {
	return &Error{Kind: kind, Msg: fmt.Sprintf("%s (<template>, line %d)", msg, line), Line: line}
}

// Compiles jinja2's generated module as CPython does, failing with the first error in CPython's phase order
func pyCompile(code string, lits []litMark) error {
	lines, err := pyTokenize(code, lits)
	if err != nil {
		return err
	}
	if err := pyCheckNames(lines); err != nil {
		return err
	}
	stmts, _ := pyStatements(lines, 0, -1)
	return (&pyUnit{}).compile(stmts)
}

// Tokenizes the module into logical lines, failing on CPython's bracket nesting and indentation limits
func pyTokenize(code string, lits []litMark) ([]pline, error) {
	var lines []pline
	var cur *pline
	level, line := 0, 1
	indents := []int{0}
	atLineStart := true
	litAt := 0
	flush := func() {
		if cur != nil && len(cur.toks) > 0 {
			lines = append(lines, *cur)
		}
		cur = nil
	}
	open := func() error {
		if level >= pyMaxLevel {
			return pySyntaxError("SyntaxError", "too many nested parentheses", line)
		}
		level++
		return nil
	}
	emit := func(kind, text string, col int) error {
		if cur == nil {
			if col > indents[len(indents)-1] {
				if len(indents) >= pyMaxIndent {
					return pySyntaxError("IndentationError", "too many levels of indentation", line)
				}
				indents = append(indents, col)
			}
			for col < indents[len(indents)-1] {
				indents = indents[:len(indents)-1]
			}
			cur = &pline{line: line, col: col}
		}
		cur.toks = append(cur.toks, ptok{kind: kind, text: text, line: line})
		return nil
	}
	col := 0
	for i := 0; i <= len(code); {
		for litAt < len(lits) && lits[litAt].pos == i {
			if _, markup := lits[litAt].v.(Markup); markup {
				if err := emit("name", "Markup", col); err != nil {
					return nil, err
				}
				if err := open(); err != nil {
					return nil, err
				}
				level--
				emit("op", "(", col)
				emit("str", "", col)
				emit("op", ")", col)
			} else if err := emit("str", "", col); err != nil {
				return nil, err
			}
			atLineStart = false
			litAt++
		}
		if i == len(code) {
			break
		}
		c := code[i]
		switch {
		case c == '\n':
			line++
			i++
			if level == 0 {
				flush()
				atLineStart, col = true, 0
			}
			continue
		case c == ' ':
			if atLineStart {
				col++
			}
			i++
			continue
		}
		atLineStart = false
		switch {
		case c == '\'' || c == '"':
			end := i + 1
			for code[end] != c {
				if code[end] == '\\' {
					end++
				}
				end++
			}
			if err := emit("str", code[i:end+1], col); err != nil {
				return nil, err
			}
			i = end + 1
		case c == '(' || c == '[' || c == '{':
			if err := emit("op", string(c), col); err != nil {
				return nil, err
			}
			if err := open(); err != nil {
				return nil, err
			}
			i++
		case c == ')' || c == ']' || c == '}':
			level--
			if err := emit("op", string(c), col); err != nil {
				return nil, err
			}
			i++
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			end := i
			for end < len(code) && (code[end] == '_' || code[end] >= 'a' && code[end] <= 'z' || code[end] >= 'A' && code[end] <= 'Z' || code[end] >= '0' && code[end] <= '9') {
				end++
			}
			if end < len(code) && (code[end] == '\'' || code[end] == '"') && code[i:end] == "f" {
				i = end
				continue
			}
			if err := emit("name", code[i:end], col); err != nil {
				return nil, err
			}
			i = end
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(code) && code[i+1] >= '0' && code[i+1] <= '9':
			end := i
			for end < len(code) && strings.IndexByte("0123456789abcdefABCDEFxXoObB_.+-jJ", code[end]) >= 0 {
				if (code[end] == '+' || code[end] == '-') && code[end-1] != 'e' && code[end-1] != 'E' {
					break
				}
				end++
			}
			if err := emit("num", code[i:end], col); err != nil {
				return nil, err
			}
			i = end
		default:
			end := i + 1
			for _, op := range []string{"**", "//", "==", "!=", "<=", ">="} {
				if strings.HasPrefix(code[i:], op) {
					end = i + len(op)
					break
				}
			}
			if err := emit("op", code[i:end], col); err != nil {
				return nil, err
			}
			i = end
		}
	}
	flush()
	return lines, nil
}

// The checks CPython makes after parsing, in source order: keywords assigning __debug__ and repeated def parameters
func pyCheckNames(lines []pline) error {
	for _, l := range lines {
		for i, t := range l.toks {
			if t.kind == "name" && t.text == "__debug__" && i+1 < len(l.toks) && l.toks[i+1].text == "=" {
				return pySyntaxError("SyntaxError", "cannot assign to __debug__", t.line)
			}
		}
		if len(l.toks) < 2 || l.toks[0].text != "def" {
			continue
		}
		seen := map[string]bool{}
		depth := 0
		expectName := false
		for _, t := range l.toks[2:] {
			switch t.text {
			case "(":
				depth++
				if depth == 1 {
					expectName = true
				}
				continue
			case ")":
				depth--
				continue
			case ",":
				if depth == 1 {
					expectName = true
				}
				continue
			}
			if depth == 1 && expectName && t.kind == "name" {
				if seen[t.text] {
					return pySyntaxError("SyntaxError", "duplicate argument "+strRepr(t.text)+" in function definition", t.line)
				}
				seen[t.text] = true
			}
			if t.text != "*" {
				expectName = false
			}
		}
	}
	return nil
}

// A statement of the generated module with the clauses nested under it
type pstmt struct {
	kw   string
	line int
	toks []ptok
	body []*pstmt
}

// Python keywords that start the statements the checks distinguish
var pyStmtKeywords = map[string]bool{"def": true, "for": true, "if": true, "elif": true, "else": true, "try": true, "except": true, "finally": true, "break": true, "continue": true}

// Groups logical lines indented past col into statements with their nested bodies
func pyStatements(lines []pline, i, col int) ([]*pstmt, int) {
	var out []*pstmt
	for i < len(lines) && lines[i].col > col {
		l := lines[i]
		s := &pstmt{line: l.line, toks: l.toks}
		if first := l.toks[0]; first.kind == "name" && pyStmtKeywords[first.text] {
			s.kw = first.text
		}
		i++
		if s.kw != "" && s.kw != "break" && s.kw != "continue" && i < len(lines) && lines[i].col > l.col {
			s.body, i = pyStatements(lines, i, l.col)
		}
		out = append(out, s)
	}
	return out, i
}

// One code object being compiled, its enclosing loops and statically nested blocks
type pyUnit struct{ loops, blocks int }

// Compiles statements in CPython's order, try bodies then else then handlers
func (u *pyUnit) compile(stmts []*pstmt) error {
	for i := 0; i < len(stmts); i++ {
		s := stmts[i]
		switch s.kw {
		case "def":
			fn := &pyUnit{}
			if pyGenerator(s.body) {
				fn.blocks = 1
			}
			if err := fn.compile(s.body); err != nil {
				return err
			}
		case "for":
			if err := pyCalls(s.toks); err != nil {
				return err
			}
			if err := u.push(s.line, 1); err != nil {
				return err
			}
			inner := &pyUnit{loops: u.loops + 1, blocks: u.blocks + 1}
			if err := inner.compile(s.body); err != nil {
				return err
			}
		case "try":
			var handlers, orelse []*pstmt
			for i+1 < len(stmts) && (stmts[i+1].kw == "except" || stmts[i+1].kw == "else" || stmts[i+1].kw == "finally") {
				i++
				if stmts[i].kw == "else" {
					orelse = append(orelse, stmts[i])
				} else if stmts[i].kw == "except" {
					handlers = append(handlers, stmts[i])
				}
			}
			if err := u.push(s.line, 1); err != nil {
				return err
			}
			if err := (&pyUnit{loops: u.loops, blocks: u.blocks + 1}).compile(s.body); err != nil {
				return err
			}
			for _, e := range orelse {
				if err := u.compile(e.body); err != nil {
					return err
				}
			}
			for _, h := range handlers {
				if err := u.push(h.line, 2); err != nil {
					return err
				}
				if err := (&pyUnit{loops: u.loops, blocks: u.blocks + 2}).compile(h.body); err != nil {
					return err
				}
			}
		case "break":
			if u.loops == 0 {
				return pySyntaxError("SyntaxError", "'break' outside loop", s.line)
			}
		case "continue":
			if u.loops == 0 {
				return pySyntaxError("SyntaxError", "'continue' not properly in loop", s.line)
			}
		default:
			if err := pyCalls(s.toks); err != nil {
				return err
			}
			if err := u.compile(s.body); err != nil {
				return err
			}
		}
	}
	return nil
}

// Whether a function body yields outside nested defs, making CPython wrap it in its StopIteration block
func pyGenerator(body []*pstmt) bool {
	for _, s := range body {
		if s.kw == "def" {
			continue
		}
		for _, t := range s.toks {
			if t.kind == "name" && t.text == "yield" {
				return true
			}
		}
		if pyGenerator(s.body) {
			return true
		}
	}
	return false
}

// Fails once n more statically nested blocks would pass CO_MAXBLOCKS, at the line pushing them
func (u *pyUnit) push(line, n int) error {
	if u.blocks+n > pyMaxBlocks {
		return pySyntaxError("SyntaxError", "too many statically nested blocks", line)
	}
	return nil
}

// Names that make a following parenthesis a grouping rather than a call
var pyGroupingNames = map[string]bool{"in": true, "not": true, "and": true, "or": true, "if": true, "else": true, "yield": true, "return": true, "is": true, "for": true, "elif": true}

// Validates each call's keywords before the calls in its arguments, as CPython's codegen does
func pyCalls(toks []ptok) error {
	type call struct {
		kws   []ptok
		depth int
	}
	var calls []*call
	var stack []*call
	depth := 0
	for i, t := range toks {
		switch t.text {
		case "(", "[", "{":
			depth++
			var c *call
			if t.text == "(" && i > 0 {
				prev := toks[i-1]
				if prev.kind == "name" && !pyGroupingNames[prev.text] || prev.text == ")" || prev.text == "]" {
					c = &call{depth: depth}
					calls = append(calls, c)
				}
			}
			stack = append(stack, c)
			continue
		case ")", "]", "}":
			depth--
			stack = stack[:len(stack)-1]
			continue
		}
		if t.kind == "name" && i+1 < len(toks) && toks[i+1].text == "=" && len(stack) > 0 {
			if c := stack[len(stack)-1]; c != nil && c.depth == depth {
				c.kws = append(c.kws, t)
			}
		}
	}
	for _, c := range calls {
		for i, k := range c.kws {
			for _, other := range c.kws[i+1:] {
				if other.text == k.text {
					return pySyntaxError("SyntaxError", "keyword argument repeated: "+k.text, other.line)
				}
			}
		}
	}
	return nil
}
