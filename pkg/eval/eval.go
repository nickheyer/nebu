// Package eval compiles expressions and templates used by spec files.
package eval

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// Byte unit constants exposed to every expression
var Constants = map[string]any{
	"KiB": 1024.0,
	"MiB": 1048576.0,
	"GiB": 1073741824.0,
	"TiB": 1099511627776.0,
}

// Functions exposed to every expression
var functions = []expr.Option{
	expr.Function("vercmp", func(args ...any) (any, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("vercmp needs two arguments")
		}
		return CompareVersions(fmt.Sprint(args[0]), fmt.Sprint(args[1])), nil
	}, new(func(string, string) int)),
	expr.Function("num", func(args ...any) (any, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("num needs one argument")
		}
		return Number(args[0])
	}, new(func(any) float64)),
}

// Compiled expression
type Expr struct {
	src  string
	prog *vm.Program
}

// Compiles an expression allowing undefined variables
func Compile(src string) (*Expr, error) {
	opts := append([]expr.Option{expr.AllowUndefinedVariables()}, functions...)
	prog, err := expr.Compile(src, opts...)
	if err != nil {
		return nil, fmt.Errorf("compile %q: %w", src, err)
	}
	return &Expr{src: src, prog: prog}, nil
}

// Returns expression source
func (e *Expr) Source() string { return e.src }

// Evaluates against merged constants and env
func (e *Expr) Eval(env map[string]any) (any, error) {
	out, err := expr.Run(e.prog, Env(env))
	if err != nil {
		return nil, fmt.Errorf("eval %q: %w", e.src, err)
	}
	return out, nil
}

// Evaluates and coerces to float
func (e *Expr) Float(env map[string]any) (float64, error) {
	out, err := e.Eval(env)
	if err != nil {
		return 0, err
	}
	return Number(out)
}

// Evaluates and coerces to bool
func (e *Expr) Bool(env map[string]any) (bool, error) {
	out, err := e.Eval(env)
	if err != nil {
		return false, err
	}
	switch v := out.(type) {
	case bool:
		return v, nil
	case nil:
		return false, nil
	}
	return false, fmt.Errorf("eval %q: %v is not a bool", e.src, out)
}

// Merges constants under the given env
func Env(env map[string]any) map[string]any {
	out := make(map[string]any, len(env)+len(Constants))
	for k, v := range Constants {
		out[k] = v
	}
	for k, v := range env {
		out[k] = v
	}
	return out
}

// Coerces any scalar to float
func Number(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case uint64:
		return float64(n), nil
	case uint32:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case bool:
		if n {
			return 1, nil
		}
		return 0, nil
	case string:
		return ParseNumber(n)
	case nil:
		return 0, fmt.Errorf("nil is not a number")
	}
	return 0, fmt.Errorf("%T is not a number", v)
}

// Parses a number, taking the max of a comma list
func ParseNumber(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty number")
	}
	if strings.Contains(s, ",") {
		best := 0.0
		for i, part := range strings.Split(s, ",") {
			n, err := ParseNumber(part)
			if err != nil {
				return 0, err
			}
			if i == 0 || n > best {
				best = n
			}
		}
		return best, nil
	}
	if b, err := strconv.ParseBool(s); err == nil && (s == "true" || s == "false") {
		if b {
			return 1, nil
		}
		return 0, nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("parse number %q: %w", s, err)
	}
	return n, nil
}

// Compares dotted numeric versions, ignoring non numeric tails
func CompareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(s string) []int {
	var parts []int
	for _, field := range strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' || r == '+' }) {
		digits := strings.TrimLeftFunc(field, func(r rune) bool { return r < '0' || r > '9' })
		digits = strings.TrimRightFunc(digits, func(r rune) bool { return r < '0' || r > '9' })
		n, err := strconv.Atoi(digits)
		if err != nil {
			break
		}
		parts = append(parts, n)
	}
	return parts
}

var numberPrefix = regexp.MustCompile(`^\s*([-+]?[0-9]*\.?[0-9]+(?:[eE][-+]?[0-9]+)?)\s*([A-Za-z]*)`)

// Parses a byte count with an inline or default unit
func Bytes(s, unit string) (uint64, error) {
	m := numberPrefix.FindStringSubmatch(strings.ReplaceAll(s, ",", ""))
	if m == nil {
		return 0, fmt.Errorf("parse bytes %q", s)
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	if m[2] != "" {
		unit = m[2]
	}
	mult, ok := unitMultiplier(unit)
	if !ok {
		return 0, fmt.Errorf("unknown unit %q", unit)
	}
	return uint64(f * mult), nil
}

func unitMultiplier(unit string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "", "b", "bytes":
		return 1, true
	case "k", "kb", "kib":
		return 1 << 10, true
	case "m", "mb", "mib":
		return 1 << 20, true
	case "g", "gb", "gib":
		return 1 << 30, true
	case "t", "tb", "tib":
		return 1 << 40, true
	}
	return 0, false
}

// Compiled text template
type Template struct {
	src string
	t   *template.Template
}

// Helpers every template can call
var templateFuncs = template.FuncMap{
	"join":       joinAny,
	"split":      func(sep, s string) []string { return strings.Split(s, sep) },
	"lower":      strings.ToLower,
	"upper":      strings.ToUpper,
	"trimSpace":  strings.TrimSpace,
	"trimPrefix": func(prefix, s string) string { return strings.TrimPrefix(s, prefix) },
	"trimSuffix": func(suffix, s string) string { return strings.TrimSuffix(s, suffix) },
	"replace":    func(old, new, s string) string { return strings.ReplaceAll(s, old, new) },
	"contains":   func(sub, s string) bool { return strings.Contains(s, sub) },
	"hasPrefix":  func(prefix, s string) bool { return strings.HasPrefix(s, prefix) },
	"hasSuffix":  func(suffix, s string) bool { return strings.HasSuffix(s, suffix) },
	"default":    defaultValue,
	"quote":      strconv.Quote,
	"list":       func(args ...any) []any { return args },
	"first":      firstOf,
	"num":        func(v any) float64 { n, _ := Number(v); return n },
}

// Joins a slice of any scalar type with sep
func joinAny(sep string, v any) string {
	switch t := v.(type) {
	case []string:
		return strings.Join(t, sep)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, fmt.Sprint(item))
		}
		return strings.Join(parts, sep)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// Returns def when v is nil or an empty string
func defaultValue(def, v any) any {
	switch t := v.(type) {
	case nil:
		return def
	case string:
		if t == "" {
			return def
		}
	}
	return v
}

// Returns the first element of a slice, nil when empty
func firstOf(v any) any {
	switch t := v.(type) {
	case []string:
		if len(t) > 0 {
			return t[0]
		}
	case []any:
		if len(t) > 0 {
			return t[0]
		}
	}
	return nil
}

// Compiles a template that errors on missing keys
func CompileTemplate(src string) (*Template, error) {
	t, err := template.New("").Funcs(templateFuncs).Option("missingkey=error").Parse(src)
	if err != nil {
		return nil, fmt.Errorf("compile template %q: %w", src, err)
	}
	return &Template{src: src, t: t}, nil
}

// Returns template source
func (t *Template) Source() string { return t.src }

// Renders the template against ctx
func (t *Template) Render(ctx any) (string, error) {
	var buf bytes.Buffer
	if err := t.t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("render %q: %w", t.src, err)
	}
	return buf.String(), nil
}

// Compiles every template in a map keyed by name
func CompileTemplates(src map[string]string) (map[string]*Template, error) {
	out := make(map[string]*Template, len(src))
	for k, v := range src {
		t, err := CompileTemplate(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = t
	}
	return out, nil
}

// Renders every template in a map keyed by name
func RenderTemplates(ts map[string]*Template, ctx any) (map[string]string, error) {
	out := make(map[string]string, len(ts))
	for k, t := range ts {
		v, err := t.Render(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = v
	}
	return out, nil
}
