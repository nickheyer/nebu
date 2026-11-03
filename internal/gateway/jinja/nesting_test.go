package jinja

import (
	"fmt"
	"strings"
	"testing"
)

// Compile outcomes CPython 3.14.8 with jinja2 3.1.6 gives at each depth where the outcome changes, measured at generate.py's call depth
func TestNestingThresholds(t *testing.T) {
	rep := func(open, inner, close string) func(int) string {
		return func(n int) string { return strings.Repeat(open, n) + inner + strings.Repeat(close, n) }
	}
	const (
		recursion = "RecursionError: maximum recursion depth exceeded"
		indent    = "IndentationError: too many levels of indentation (<template>, line %d)"
		parens    = "SyntaxError: too many nested parentheses (<template>, line %d)"
		blocks    = "SyntaxError: too many statically nested blocks (<template>, line %d)"
	)
	tests := []struct {
		name  string
		build func(int) string
		// Depths each followed by the outcome starting there, OK for a clean compile
		steps []any
	}{
		{"parentheses", func(n int) string { return "{{ " + rep("(", "x", ")")(n) + " }}" }, []any{69, "OK", 70, recursion}},
		{"brackets", func(n int) string { return "{{ " + rep("[", "x", "]")(n) + " }}" }, []any{74, "OK", 75, recursion}},
		{"braces", func(n int) string { return "{{ " + rep("{'a': ", "x", "}")(n) + " }}" }, []any{74, "OK", 75, recursion}},
		{"calls", func(n int) string { return "{{ " + rep("f(", "x", ")")(n) + " }}" }, []any{69, "OK", 70, recursion}},
		{"filters", func(n int) string { return "{{ x" + strings.Repeat("|trim", n) + " }}" },
			[]any{197, "OK", 198, fmt.Sprintf(parens, 18), 326, fmt.Sprintf(parens, 18), 327, recursion}},
		{"if expressions", func(n int) string { return "{{ " + strings.Repeat("x if y else ", n) + "z }}" },
			[]any{197, "OK", 198, fmt.Sprintf(parens, 14), 244, fmt.Sprintf(parens, 14), 245, recursion}},
		{"parenthesized if expressions", func(n int) string { return "{{ " + rep("(x if y else ", "z", ")")(n) + " }}" }, []any{64, "OK", 65, recursion}},
		{"additions", func(n int) string { return "{{ x" + strings.Repeat(" + x", n) + " }}" },
			[]any{197, "OK", 198, fmt.Sprintf(parens, 12), 326, fmt.Sprintf(parens, 12), 327, recursion}},
		{"nots", func(n int) string { return "{{ " + strings.Repeat("not ", n) + "x }}" },
			[]any{197, "OK", 198, fmt.Sprintf(parens, 12), 326, fmt.Sprintf(parens, 12), 327, recursion}},
		{"negations", func(n int) string { return "{{ " + strings.Repeat("-", n) + "x }}" },
			[]any{197, "OK", 198, fmt.Sprintf(parens, 12), 326, fmt.Sprintf(parens, 12), 327, recursion}},
		{"attributes", func(n int) string { return "{{ x" + strings.Repeat(".a", n) + " }}" },
			[]any{197, "OK", 198, fmt.Sprintf(parens, 12), 326, fmt.Sprintf(parens, 12), 327, recursion}},
		{"filters with arguments", func(n int) string { return "{{ x" + strings.Repeat("|replace(y, z)", n) + " }}" },
			[]any{197, "OK", 198, fmt.Sprintf(parens, 20), 325, fmt.Sprintf(parens, 20), 326, recursion}},
		{"folded filters", func(n int) string { return "{{ 'a'" + strings.Repeat("|upper", n) + " }}" }, []any{326, "OK", 327, recursion}},
		{"ifs", rep("{% if x %}", "y", "{% endif %}"), []any{98, "OK", 99, fmt.Sprintf(indent, 209), 243, fmt.Sprintf(indent, 209), 244, recursion}},
		{"fors", rep("{% for x in y %}", "z", "{% endfor %}"),
			[]any{20, "OK", 21, fmt.Sprintf(blocks, 72), 98, fmt.Sprintf(blocks, 72), 99, fmt.Sprintf(indent, 307), 243, fmt.Sprintf(indent, 307), 244, recursion}},
		{"fors around a block", rep("{% for x in y %}", "{% block b %}z{% endblock %}", "{% endfor %}"), []any{20, "OK", 21, fmt.Sprintf(blocks, 72)}},
		{"fors around an include ignoring missing", rep("{% for x in y %}", "{% include 'a' ignore missing %}", "{% endfor %}"),
			[]any{18, "OK", 19, fmt.Sprintf(blocks, 76), 20, fmt.Sprintf(blocks, 72), 95, fmt.Sprintf(blocks, 72), 96, fmt.Sprintf(indent, 308), 97, fmt.Sprintf(indent, 310), 98, fmt.Sprintf(indent, 307)}},
		{"fors around an include", rep("{% for x in y %}", "{% include 'a' %}", "{% endfor %}"),
			[]any{18, "OK", 19, fmt.Sprintf(blocks, 72), 20, fmt.Sprintf(blocks, 74), 21, fmt.Sprintf(blocks, 72)}},
		{"macros", rep("{% macro m() %}", "y", "{% endmacro %}"), []any{98, "OK", 99, fmt.Sprintf(indent, 405), 244, fmt.Sprintf(indent, 405), 245, recursion}},
		{"withs", rep("{% with a = 1 %}", "y", "{% endwith %}"), []any{243, "OK", 244, recursion}},
		{"filter blocks", rep("{% filter upper %}", "y", "{% endfilter %}"), []any{246, "OK", 247, recursion}},
		{"set blocks", rep("{% set a %}", "y", "{% endset %}"), []any{245, "OK", 246, recursion}},
		{"fors and ifs", rep("{% for x in y %}{% if x %}", "z", "{% endif %}{% endfor %}"),
			[]any{20, "OK", 21, fmt.Sprintf(blocks, 112), 49, fmt.Sprintf(blocks, 112), 50, fmt.Sprintf(indent, 258), 121, fmt.Sprintf(indent, 258), 122, recursion}},
		{"recursive fors", rep("{% for x in y recursive %}", "z", "{% endfor %}"), []any{49, "OK", 50, fmt.Sprintf(indent, 307), 243, fmt.Sprintf(indent, 307), 244, recursion}},
	}
	for _, tc := range tests {
		for i := 0; i < len(tc.steps); i += 2 {
			n, want := tc.steps[i].(int), tc.steps[i+1].(string)
			t.Run(fmt.Sprintf("%s/%d", tc.name, n), func(t *testing.T) {
				_, err := parse(tc.build(n))
				got := "OK"
				if err != nil {
					got = err.(*Error).Kind + ": " + err.(*Error).Msg
				}
				if got != want {
					t.Errorf("depth %d: %s, want %s", n, got, want)
				}
			})
		}
	}
}
