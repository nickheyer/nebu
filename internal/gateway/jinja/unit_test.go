package jinja

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Token streams and parse trees jinja2 produced for the same sources
type syntaxTables struct {
	Lex []struct {
		Src    string   `json:"src"`
		Tokens []string `json:"tokens"`
	} `json:"lex"`
	Parse []struct {
		Src  string `json:"src"`
		Tree string `json:"tree"`
	} `json:"parse"`
	Errors []unitRow `json:"errors"`
}

// Sources per filter and test with jinja2's output or exception for each
type unitTables struct {
	Vars    json.RawMessage      `json:"vars"`
	Filters map[string][]unitRow `json:"filters"`
	Tests   map[string][]unitRow `json:"tests"`
	Groups  map[string][]unitRow `json:"groups"`
}

type unitRow struct {
	Src     string  `json:"src"`
	Out     *string `json:"out"`
	Error   string  `json:"error"`
	Message string  `json:"message"`
	Lineno  int     `json:"lineno"`
}

// Tokenizes each source and compares every token's line, kind and value with jinja2's lexer
func TestTokenize(t *testing.T) {
	var tables syntaxTables
	readJSON(t, &tables, "syntax.json")
	for i, c := range tables.Lex {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			toks := tokenize(c.Src)
			if last := toks[len(toks)-1]; last.kind != tokEOF {
				t.Fatalf("%q: stream ends with %s, not eof", c.Src, last.kind)
			}
			got := make([]string, len(toks)-1)
			for j, tok := range toks[:len(toks)-1] {
				got[j] = fmt.Sprintf("%d %s %s", tok.line, tok.kind, tokenValue(tok.value))
			}
			if strings.Join(got, "\n") != strings.Join(c.Tokens, "\n") {
				t.Errorf("%q\n--- got\n%s\n--- want\n%s", c.Src, strings.Join(got, "\n"), strings.Join(c.Tokens, "\n"))
			}
		})
	}
}

// A token value as generate.py prints it, strings as JSON and numbers as Python repr
func tokenValue(v Value) string {
	if s, ok := v.(string); ok {
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(s)
		return strings.TrimSuffix(b.String(), "\n")
	}
	return mustRepr(v)
}

// The repr of a value the test built itself, which never holds an integer past the digit limit
func mustRepr(v Value) string {
	s, err := pyRepr(v)
	if err != nil {
		panic(err)
	}
	return s
}

// The lexer refuses malformed sources with the errors jinja2 raises
func TestTokenizeErrors(t *testing.T) {
	tests := []struct{ src, want string }{
		{"{{ 'open }}", `unexpected char "'" at 3`},
		{"{{ a ) }}", "unexpected ')'"},
		{"{{ (a] }}", "unexpected ']', expected ')'"},
		{"{{ a $ b }}", "unexpected char '$' at 5"},
	}
	for _, tc := range tests {
		_, err := parse(tc.src)
		checkError(t, err, goldenResult{Error: "TemplateSyntaxError", Message: tc.want, Lineno: 1})
		if err != nil && err.Error() != tc.want {
			t.Errorf("%q: message %q, want %q", tc.src, err.Error(), tc.want)
		}
	}
}

// Parses each source without constant folding and compares the tree with the one jinja2 builds
func TestParseTree(t *testing.T) {
	var tables syntaxTables
	readJSON(t, &tables, "syntax.json")
	for i, c := range tables.Parse {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			body, err := parseTemplate(c.Src, newCompileStack())
			if err != nil {
				t.Fatalf("%q: %v", c.Src, err)
			}
			if got := dumpBody(body); got != c.Tree {
				t.Errorf("%q\n--- got\n%s\n--- want\n%s", c.Src, got, c.Tree)
			}
		})
	}
}

// The parser refuses malformed templates with jinja2's exception classes and messages
func TestParseErrors(t *testing.T) {
	tests := []struct{ src, kind, want string }{
		{"{% if x %}", "TemplateSyntaxError", "Unexpected end of template. Jinja was looking for the following tags: 'elif' or 'else' or 'endif'. The innermost block that needs to be closed is 'if'."},
		{"{% for x in y %}{% endif %}", "TemplateSyntaxError", "Encountered unknown tag 'endif'. Jinja was looking for the following tags: 'endfor' or 'else'. The innermost block that needs to be closed is 'for'."},
		{"{{ x is }}", "TemplateSyntaxError", "expected token 'name', got 'end of print statement'"},
		{"{% set %}", "TemplateSyntaxError", "Expected an expression, got 'end of statement block'"},
		{"{{ 1 + }}", "TemplateSyntaxError", "unexpected 'end of print statement'"},
		{"{% macro m(a=1, b) %}{% endmacro %}", "TemplateSyntaxError", "non-default argument follows default argument"},
		{"{% endfor %}", "TemplateSyntaxError", "Encountered unknown tag 'endfor'."},
		{"{% break %}", "SyntaxError", "'break' outside loop (<template>, line 11)"},
	}
	for _, tc := range tests {
		_, err := parse(tc.src)
		line := 1
		if tc.kind == "SyntaxError" {
			line = 11
		}
		checkError(t, err, goldenResult{Error: tc.kind, Message: tc.want, Lineno: line})
		if err != nil && tc.want != "" && err.Error() != tc.want {
			t.Errorf("%q: message %q, want %q", tc.src, err.Error(), tc.want)
		}
	}
}

// Every filter jinja2 registers renders as jinja2 renders it, and the engine registers no other
func TestFilters(t *testing.T) {
	var tables unitTables
	readJSON(t, &tables, "units.json")
	checkNames(t, "filter", keysOf(builtinFilters), tables.Filters, "")
	runUnits(t, tables.Vars, tables.Filters)
}

// Every test jinja2 registers evaluates as jinja2 evaluates it, and the engine registers no other
func TestTests(t *testing.T) {
	var tables unitTables
	readJSON(t, &tables, "units.json")
	checkNames(t, "test", keysOf(builtinTests), tables.Tests, "")
	runUnits(t, tables.Vars, tables.Tests)
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Requires the engine's names to equal the table's, apart from one nondeterministic name
func checkNames(t *testing.T, kind string, have []string, table map[string][]unitRow, skip string) {
	t.Helper()
	for _, name := range have {
		if _, ok := table[name]; !ok && name != skip {
			t.Errorf("%s %s has no unit cases", kind, name)
		}
	}
	for name := range table {
		if !contains(have, name) {
			t.Errorf("jinja2 %s %s is missing from the engine", kind, name)
		}
	}
}

func contains(list []string, s string) bool {
	i := sort.SearchStrings(list, s)
	return i < len(list) && list[i] == s
}

func runUnits(t *testing.T, vars json.RawMessage, table map[string][]unitRow) {
	for _, name := range keysOf(table) {
		for i, row := range table[name] {
			t.Run(fmt.Sprintf("%s/%d", name, i), func(t *testing.T) {
				got, err := renderSource(row.Src, vars)
				checkExact(t, row, got, err)
			})
		}
	}
}

// Fails unless the output, or the exception class and its message, equal what jinja2 produced
func checkExact(t *testing.T, row unitRow, got string, err error) {
	t.Helper()
	if row.Out != nil {
		if err != nil {
			t.Fatalf("%q: %v, want %q", row.Src, err, *row.Out)
		}
		if got != *row.Out {
			t.Errorf("%q\n--- got\n%q\n--- want\n%q", row.Src, got, *row.Out)
		}
		return
	}
	var te *Error
	if !errors.As(err, &te) {
		t.Fatalf("%q: got %q, %v, want %s: %s", row.Src, got, err, row.Error, row.Message)
	}
	if te.Kind != row.Error || te.Msg != row.Message || te.Line != row.Lineno {
		t.Errorf("%q\n--- got\n%s: %s at line %d\n--- want\n%s: %s at line %d", row.Src, te.Kind, te.Msg, te.Line, row.Error, row.Message, row.Lineno)
	}
}

// Renders cross cutting behaviors, digit limits, compile time order, recursion and memory bounds, as jinja2 does
func TestUnitGroups(t *testing.T) {
	var tables unitTables
	readJSON(t, &tables, "units.json")
	if len(tables.Groups) == 0 {
		t.Fatal("units.json holds no groups")
	}
	runUnits(t, tables.Vars, tables.Groups)
}

// Compiles malformed and refused sources and requires jinja2's exception class and message for each
func TestSyntaxErrors(t *testing.T) {
	var tables syntaxTables
	readJSON(t, &tables, "syntax.json")
	for i, row := range tables.Errors {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			_, err := parse(row.Src)
			checkExact(t, row, "", err)
		})
	}
}

// The tree in generate.py's notation, text and output nodes flattened as jinja2's Output holds them
func dumpBody(body []stmt) string {
	parts := make([]string, len(body))
	for i, s := range body {
		parts[i] = dumpStmt(s)
	}
	return "(" + strings.Join(parts, " ") + ")"
}

func dumpStmt(s stmt) string {
	switch x := s.(type) {
	case *textStmt:
		return "(text " + tokenValue(x.text) + ")"
	case *outputStmt:
		return "(out " + dumpExpr(x.expr) + ")"
	case *forStmt:
		return fmt.Sprintf("(for %s %s %s %s %s %s)", dumpExpr(x.target), dumpExpr(x.iter), dumpBody(x.body), dumpBody(x.orelse), dumpExpr(x.test), mustRepr(x.recursive))
	case *ifStmt:
		elifs := make([]string, len(x.elifs))
		for i, e := range x.elifs {
			elifs[i] = dumpStmt(e)
		}
		return fmt.Sprintf("(if %s %s (%s) %s)", dumpExpr(x.test), dumpBody(x.body), strings.Join(elifs, " "), dumpBody(x.orelse))
	case *setStmt:
		return fmt.Sprintf("(set %s %s)", dumpExpr(x.target), dumpExpr(x.value))
	case *setBlockStmt:
		return fmt.Sprintf("(setblock %s %s %s)", dumpExpr(x.target), dumpExpr(x.filter), dumpBody(x.body))
	case *macroStmt:
		return fmt.Sprintf("(macro %s %s)", x.name, dumpSig(x.sig))
	case *callBlockStmt:
		return fmt.Sprintf("(callblock %s %s)", dumpExpr(x.call), dumpSig(x.sig))
	case *filterBlockStmt:
		return fmt.Sprintf("(filterblock %s %s)", dumpExpr(x.filter), dumpBody(x.body))
	case *withStmt:
		return fmt.Sprintf("(with (%s) (%s) %s)", dumpExprs(x.targets), dumpExprs(x.values), dumpBody(x.body))
	case *autoescapeStmt:
		return fmt.Sprintf("(autoescape %s %s)", dumpExpr(x.value), dumpBody(x.body))
	case *breakStmt:
		return "(break)"
	case *continueStmt:
		return "(continue)"
	}
	return fmt.Sprintf("(unknown %T)", s)
}

func dumpSig(s *signature) string {
	return fmt.Sprintf("(%s) (%s) %s", strings.Join(s.params, " "), dumpExprs(s.defaults), dumpBody(s.body))
}

func dumpExprs(es []expr) string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = dumpExpr(e)
	}
	return strings.Join(parts, " ")
}

func dumpArgs(a callArgs) string {
	var b strings.Builder
	if len(a.args) > 0 {
		b.WriteString(" (args " + dumpExprs(a.args) + ")")
	}
	if len(a.kwNames) > 0 {
		kw := make([]string, len(a.kwNames))
		for i, k := range a.kwNames {
			kw[i] = k + "=" + dumpExpr(a.kwVals[i])
		}
		b.WriteString(" (kw " + strings.Join(kw, " ") + ")")
	}
	if a.dynArgs != nil {
		b.WriteString(" (star " + dumpExpr(a.dynArgs) + ")")
	}
	if a.dynKwargs != nil {
		b.WriteString(" (dstar " + dumpExpr(a.dynKwargs) + ")")
	}
	return b.String()
}

func dumpExpr(e expr) string {
	switch x := e.(type) {
	case nil, *bufferExpr:
		return "_"
	case *constExpr:
		if s, ok := x.v.(string); ok {
			return "(const " + tokenValue(s) + ")"
		}
		return "(const " + mustRepr(x.v) + ")"
	case *nameExpr:
		return "(name " + x.name + ")"
	case *nsRefExpr:
		return "(nsref " + x.name + " " + x.attr + ")"
	case *tupleExpr:
		return "(tuple" + prefixed(x.items) + ")"
	case *listExpr:
		return "(list" + prefixed(x.items) + ")"
	case *dictExpr:
		var b strings.Builder
		for i := range x.keys {
			b.WriteString(" (" + dumpExpr(x.keys[i]) + " " + dumpExpr(x.vals[i]) + ")")
		}
		return "(dict" + b.String() + ")"
	case *getattrExpr:
		return "(getattr " + dumpExpr(x.obj) + " " + x.attr + ")"
	case *getitemExpr:
		return "(getitem " + dumpExpr(x.obj) + " " + dumpExpr(x.arg) + ")"
	case *sliceExpr:
		return "(slice " + dumpExpr(x.start) + " " + dumpExpr(x.stop) + " " + dumpExpr(x.step) + ")"
	case *callExpr:
		return "(call " + dumpExpr(x.fn) + dumpArgs(x.args) + ")"
	case *filterExpr:
		return "(filter " + x.name + " " + dumpExpr(x.node) + dumpArgs(x.args) + ")"
	case *testExpr:
		return "(test " + x.name + " " + dumpExpr(x.node) + dumpArgs(x.args) + ")"
	case *condExpr:
		return "(cond " + dumpExpr(x.test) + " " + dumpExpr(x.expr1) + " " + dumpExpr(x.expr2) + ")"
	case *binExpr:
		return "(" + x.op + " " + dumpExpr(x.left) + " " + dumpExpr(x.right) + ")"
	case *andExpr:
		return "(and " + dumpExpr(x.left) + " " + dumpExpr(x.right) + ")"
	case *orExpr:
		return "(or " + dumpExpr(x.left) + " " + dumpExpr(x.right) + ")"
	case *notExpr:
		return "(not " + dumpExpr(x.e) + ")"
	case *negExpr:
		return "(neg " + dumpExpr(x.e) + ")"
	case *posExpr:
		return "(pos " + dumpExpr(x.e) + ")"
	case *concatExpr:
		return "(concat" + prefixed(x.items) + ")"
	case *compareExpr:
		var b strings.Builder
		for i, op := range x.ops {
			b.WriteString(" (" + op + " " + dumpExpr(x.operands[i]) + ")")
		}
		return "(compare " + dumpExpr(x.first) + b.String() + ")"
	}
	return fmt.Sprintf("(unknown %T)", e)
}

func prefixed(es []expr) string {
	var b strings.Builder
	for _, e := range es {
		b.WriteString(" " + dumpExpr(e))
	}
	return b.String()
}
