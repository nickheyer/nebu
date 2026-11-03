package jinja

import (
	"math/big"
)

// A jinja2 test receiving the tested value and the call's arguments
type testFunc func(r *renderer, v Value, args []Value, kw *Dict) (bool, error)

var builtinTests map[string]testFunc

func init() {
	compare := func(op string) testFunc {
		return func(r *renderer, v Value, args []Value, kw *Dict) (bool, error) {
			a, err := bindArgs(op, posParams(req("a"), req("b")), append([]Value{v}, args...), kw)
			if err != nil {
				return false, err
			}
			return compareOp(op, a[0], a[1])
		}
	}
	builtinTests = map[string]testFunc{
		"odd":         modTest("test_odd", 1),
		"even":        modTest("test_even", 0),
		"divisibleby": testDivisibleby,
		"defined":     unary("test_defined", func(v Value) bool { _, u := v.(*Undefined); return !u }),
		"undefined":   unary("test_undefined", func(v Value) bool { _, u := v.(*Undefined); return u }),
		"filter": func(r *renderer, v Value, args []Value, kw *Dict) (bool, error) {
			a, err := filterArgs("test_filter", v, []param{req("value")}, args, kw)
			if err != nil {
				return false, err
			}
			if _, err := hashKey(a[0]); err != nil {
				return false, unhashable("dict key", a[0], err)
			}
			s, ok := asStr(a[0])
			_, found := builtinFilters[s]
			return ok && found, nil
		},
		"test": func(r *renderer, v Value, args []Value, kw *Dict) (bool, error) {
			a, err := filterArgs("test_test", v, []param{req("value")}, args, kw)
			if err != nil {
				return false, err
			}
			if _, err := hashKey(a[0]); err != nil {
				return false, unhashable("dict key", a[0], err)
			}
			s, ok := asStr(a[0])
			_, found := builtinTests[s]
			return ok && found, nil
		},
		"none":    unary("test_none", func(v Value) bool { _, ok := v.(NoneType); return ok }),
		"boolean": unary("test_boolean", func(v Value) bool { _, ok := v.(bool); return ok }),
		"false":   unary("test_false", func(v Value) bool { b, ok := v.(bool); return ok && !b }),
		"true":    unary("test_true", func(v Value) bool { b, ok := v.(bool); return ok && b }),
		"integer": unary("test_integer", func(v Value) bool {
			switch v.(type) {
			case int64, *big.Int:
				return true
			}
			return false
		}),
		"float":   unary("test_float", func(v Value) bool { _, ok := v.(float64); return ok }),
		"lower":   caseTest("test_lower", false),
		"upper":   caseTest("test_upper", true),
		"string":  unary("test_string", func(v Value) bool { _, ok := asStr(v); return ok }),
		"mapping": unary("test_mapping", func(v Value) bool { _, ok := v.(*Dict); return ok }),
		"number":  unary("test_number", func(v Value) bool { return isNumber(v) || isComplex(v) }),
		"sequence": unary("test_sequence", func(v Value) bool {
			switch v.(type) {
			case string, Markup, *List, *Tuple, *Dict, *Range, *Undefined:
				return true
			}
			return false
		}),
		"iterable": unary("test_iterable", func(v Value) bool {
			switch v.(type) {
			case string, Markup, *List, *Tuple, *Dict, *Range, *Undefined, *Iterator, *DictView, *LoopContext:
				return true
			}
			return false
		}),
		"callable": func(r *renderer, v Value, args []Value, kw *Dict) (bool, error) {
			if len(args) > 0 || len(kw.keys) > 0 {
				return false, typeError("callable() takes exactly one argument (%d given)", len(args)+1)
			}
			switch v.(type) {
			case *Macro, *Builtin, *LoopContext, *Joiner, *Undefined, *BlockRef:
				return true, nil
			}
			return false, nil
		},
		"sameas": func(r *renderer, v Value, args []Value, kw *Dict) (bool, error) {
			a, err := filterArgs("test_sameas", v, []param{req("value"), req("other")}, args, kw)
			if err != nil {
				return false, err
			}
			return sameObject(a[0], a[1]), nil
		},
		"escaped": unary("test_escaped", func(v Value) bool { _, ok := v.(Markup); return ok }),
		"in": func(r *renderer, v Value, args []Value, kw *Dict) (bool, error) {
			a, err := filterArgs("test_in", v, []param{req("value"), req("seq")}, args, kw)
			if err != nil {
				return false, err
			}
			return pyContains(a[1], a[0])
		},
		"==":          compare("eq"),
		"eq":          compare("eq"),
		"equalto":     compare("eq"),
		"!=":          compare("ne"),
		"ne":          compare("ne"),
		">":           compare("gt"),
		"gt":          compare("gt"),
		"greaterthan": compare("gt"),
		"ge":          compare("gteq"),
		">=":          compare("gteq"),
		"<":           compare("lt"),
		"lt":          compare("lt"),
		"lessthan":    compare("lt"),
		"<=":          compare("lteq"),
		"le":          compare("lteq"),
	}
}

// Calls a test by name, failing at render time for names jinja2 lets through compilation
func (r *renderer) callTest(name string, v Value, args []Value, kw *Dict) (bool, error) {
	t, ok := builtinTests[name]
	if !ok {
		return false, errorf("TemplateRuntimeError", "No test named %s.", strRepr(name))
	}
	key := builtinCallKey(name, false, v, args, kw)
	if err := r.measured(key, r.opFrames(key, v, args, kw)); err != nil {
		return false, err
	}
	return t(r, v, args, kw)
}

func unary(fname string, f func(Value) bool) testFunc {
	return func(r *renderer, v Value, args []Value, kw *Dict) (bool, error) {
		a, err := filterArgs(fname, v, []param{req("value")}, args, kw)
		if err != nil {
			return false, err
		}
		return f(a[0]), nil
	}
}

// test_lower or test_upper, the case of str(value)
func caseTest(fname string, upper bool) testFunc {
	return func(r *renderer, v Value, args []Value, kw *Dict) (bool, error) {
		a, err := filterArgs(fname, v, []param{req("value")}, args, kw)
		if err != nil {
			return false, err
		}
		s, err := pyStr(a[0])
		return pyIsCase(s, upper), err
	}
}

// Tests value % 2 against a remainder with Python's modulo
func modTest(fname string, want int64) testFunc {
	return func(r *renderer, v Value, args []Value, kw *Dict) (bool, error) {
		a, err := filterArgs(fname, v, []param{req("value")}, args, kw)
		if err != nil {
			return false, err
		}
		m, err := binaryOp("mod", a[0], int64(2))
		if err != nil {
			return false, err
		}
		return pyEq(m, want), nil
	}
}

func testDivisibleby(r *renderer, v Value, args []Value, kw *Dict) (bool, error) {
	a, err := filterArgs("test_divisibleby", v, []param{req("value"), req("num")}, args, kw)
	if err != nil {
		return false, err
	}
	m, err := binaryOp("mod", a[0], a[1])
	if err != nil {
		return false, err
	}
	return pyEq(m, int64(0)), nil
}

// Python's identity test for template values, cached small integers and singletons included
func sameObject(a, b Value) bool {
	switch x := a.(type) {
	case NoneType:
		_, ok := b.(NoneType)
		return ok
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case int64:
		y, ok := b.(int64)
		return ok && x == y && x >= -5 && x <= 256
	case string:
		y, ok := b.(string)
		return ok && x == y && runeLen(x) <= 1
	case float64, *big.Int, Markup:
		return false
	}
	return a == b
}
