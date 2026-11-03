package jinja

import (
	"math"
	"math/big"
	"strings"
	"unicode/utf8"
)

var opSymbols = map[string]string{"add": "+", "sub": "-", "mul": "*", "div": "/", "floordiv": "//", "mod": "%", "pow": "** or pow()"}

// A comparison of a compare chain
func compareOp(op string, a, b Value) (bool, error) {
	switch op {
	case "eq":
		return pyEq(a, b), nil
	case "ne":
		return !pyEq(a, b), nil
	case "lt":
		return pyOrder("<", a, b)
	case "lteq":
		return pyOrder("<=", a, b)
	case "gt":
		return pyOrder(">", a, b)
	case "gteq":
		return pyOrder(">=", a, b)
	case "in":
		return pyContains(b, a)
	}
	ok, err := pyContains(b, a)
	return !ok, err
}

func unsupported(op string, a, b Value) error {
	return typeError("unsupported operand type(s) for %s: %s and %s", opSymbols[op], strRepr(typeName(a)), strRepr(typeName(b)))
}

// Python's arithmetic operators, Markup escaping its plain string operands
func binaryOp(op string, a, b Value) (Value, error) {
	if u, ok := a.(*Undefined); ok {
		return nil, u.fail()
	}
	if u, ok := b.(*Undefined); ok {
		_, markup := a.(Markup)
		_, isStr := asStr(a)
		switch {
		case op == "mod" && isStr:
		case op == "mul" && markup:
		default:
			return nil, u.fail()
		}
	}
	if isComplex(a) && (isNumber(b) || isComplex(b)) || isComplex(b) && isNumber(a) {
		return complexOp(op, a, b)
	}
	if isNumber(a) && isNumber(b) {
		return numOp(op, a, b)
	}
	ma, aMarkup := a.(Markup)
	_, bMarkup := b.(Markup)
	switch op {
	case "add":
		sa, aStr := asStr(a)
		sb, bStr := asStr(b)
		switch {
		case aStr && bStr:
			if err := checkBytes(len(sa) + len(sb)); err != nil {
				return nil, err
			}
			if aMarkup || bMarkup {
				ea, err := escapeValue(a)
				if err != nil {
					return nil, err
				}
				eb, err := escapeValue(b)
				return ea + eb, err
			}
			return sa + sb, nil
		case aStr:
			if aMarkup {
				return nil, unsupported(op, a, b)
			}
			return nil, typeError("can only concatenate str (not %s) to str", quoteType(b))
		}
		switch x := a.(type) {
		case *List:
			if y, ok := b.(*List); ok {
				if err := checkBytes(sizeOf(x) + sizeOf(y)); err != nil {
					return nil, err
				}
				return &List{items: append(append([]Value(nil), x.items...), y.items...)}, nil
			}
			return nil, typeError("can only concatenate list (not %s) to list", quoteType(b))
		case *Tuple:
			if y, ok := b.(*Tuple); ok {
				if err := checkBytes(sizeOf(x) + sizeOf(y)); err != nil {
					return nil, err
				}
				return &Tuple{items: append(append([]Value(nil), x.items...), y.items...)}, nil
			}
			return nil, typeError("can only concatenate tuple (not %s) to tuple", quoteType(b))
		}
	case "mul":
		switch {
		case aMarkup && !isIntegral(b):
			return nil, typeError("'%s' object cannot be interpreted as an integer", typeName(b))
		case bMarkup && !isIntegral(a):
			return nil, typeError("'%s' object cannot be interpreted as an integer", typeName(a))
		case isSequence(a) && isIntegral(b):
			return repeatSeq(a, toBig(b))
		case isSequence(b) && isIntegral(a):
			return repeatSeq(b, toBig(a))
		case isSequence(a):
			return nil, typeError("can't multiply sequence by non-int of type %s", strRepr(typeName(b)))
		case isSequence(b):
			return nil, typeError("can't multiply sequence by non-int of type %s", strRepr(typeName(a)))
		}
	case "mod":
		if s, ok := asStr(a); ok {
			if aMarkup {
				return markupMod(string(ma), b)
			}
			return printfFormat(s, b)
		}
	}
	return nil, unsupported(op, a, b)
}

func quoteType(v Value) string { return `"` + typeName(v) + `"` }

// Whether the value repeats under multiplication by an int, as Python's sequences do
func isSequence(v Value) bool {
	switch v.(type) {
	case string, Markup, *List, *Tuple:
		return true
	}
	return false
}

// Python's seq * n, refusing counts and sizes as CPython does
func repeatSeq(seq Value, n *big.Int) (Value, error) {
	if !n.IsInt64() {
		return nil, errorf("OverflowError", "cannot fit 'int' into an index-sized integer")
	}
	count := max(n.Int64(), 0)
	var length int64
	switch x := seq.(type) {
	case string:
		length = int64(utf8.RuneCountInString(x))
	case Markup:
		length = int64(utf8.RuneCountInString(string(x)))
	case *List:
		length = int64(len(x.items))
	case *Tuple:
		length = int64(len(x.items))
	}
	if count > 0 && length > math.MaxInt64/count {
		if _, items := seq.(*List); items {
			return nil, errorf("MemoryError", "")
		}
		if _, items := seq.(*Tuple); items {
			return nil, errorf("MemoryError", "")
		}
		return nil, errorf("OverflowError", "repeated string is too long")
	}
	if count > 0 && int64(sizeOf(seq)) > int64(maxValueBytes)/count {
		return nil, memoryError()
	}
	if length == 0 || count == 0 {
		count = 0
	}
	switch x := seq.(type) {
	case string:
		return strings.Repeat(x, int(count)), nil
	case Markup:
		return Markup(strings.Repeat(string(x), int(count))), nil
	case *List:
		return &List{items: repeatItems(x.items, int(count))}, nil
	}
	return &Tuple{items: repeatItems(seq.(*Tuple).items, int(count))}, nil
}

func repeatItems(items []Value, n int) []Value {
	out := make([]Value, 0, len(items)*n)
	for i := 0; i < n; i++ {
		out = append(out, items...)
	}
	return out
}

// Markup's % operator, escaping each argument before formatting
func markupMod(format string, args Value) (Value, error) {
	switch a := args.(type) {
	case *Tuple:
		items := make([]Value, len(a.items))
		for i, it := range a.items {
			items[i] = &escapeHelper{obj: it}
		}
		args = &Tuple{items: items}
	default:
		if hasGetitem(a) {
			args = &escapeHelper{obj: a}
		} else {
			args = &Tuple{items: []Value{&escapeHelper{obj: a}}}
		}
	}
	s, err := printfFormat(format, args)
	return Markup(s), err
}

// Arithmetic between two numbers with Python's int and float rules
func numOp(op string, a, b Value) (Value, error) {
	_, af := a.(float64)
	_, bf := b.(float64)
	if !af && !bf && op != "div" {
		return intOp(op, toBig(a), toBig(b))
	}
	if op == "div" && !af && !bf {
		return intTrueDiv(toBig(a), toBig(b))
	}
	x, err := toFloat(a)
	if err != nil {
		return nil, err
	}
	y, err := toFloat(b)
	if err != nil {
		return nil, err
	}
	switch op {
	case "add":
		return x + y, nil
	case "sub":
		return x - y, nil
	case "mul":
		return x * y, nil
	case "div":
		if y == 0 {
			return nil, errorf("ZeroDivisionError", "division by zero")
		}
		return x / y, nil
	case "floordiv":
		if y == 0 {
			return nil, errorf("ZeroDivisionError", "division by zero")
		}
		div, _ := floatDivmod(x, y)
		return div, nil
	case "mod":
		if y == 0 {
			return nil, errorf("ZeroDivisionError", "division by zero")
		}
		_, mod := floatDivmod(x, y)
		return mod, nil
	case "pow":
		return floatPow(x, y)
	}
	return nil, unsupported(op, a, b)
}

// CPython's float floor division and modulo
func floatDivmod(vx, wx float64) (float64, float64) {
	mod := math.Mod(vx, wx)
	div := (vx - mod) / wx
	if mod != 0 {
		if (wx < 0) != (mod < 0) {
			mod += wx
			div -= 1
		}
	} else {
		mod = math.Copysign(0, wx)
	}
	var floordiv float64
	if div != 0 {
		floordiv = math.Floor(div)
		if div-floordiv > 0.5 {
			floordiv += 1
		}
	} else {
		floordiv = math.Copysign(0, vx/wx)
	}
	return floordiv, mod
}

// CPython's float_pow with its special cases and a correctly rounded power
func floatPow(iv, iw float64) (Value, error) {
	odd := func(f float64) bool { return math.Abs(math.Mod(f, 2)) == 1 }
	switch {
	case iw == 0:
		return 1.0, nil
	case math.IsNaN(iv):
		return iv, nil
	case math.IsNaN(iw):
		if iv == 1 {
			return 1.0, nil
		}
		return iw, nil
	case math.IsInf(iw, 0):
		iv = math.Abs(iv)
		switch {
		case iv == 1:
			return 1.0, nil
		case (iw > 0) == (iv > 1):
			return math.Abs(iw), nil
		}
		return 0.0, nil
	case math.IsInf(iv, 0):
		if iw > 0 {
			if odd(iw) {
				return iv, nil
			}
			return math.Abs(iv), nil
		}
		if odd(iw) {
			return math.Copysign(0, iv), nil
		}
		return 0.0, nil
	case iv == 0:
		if iw < 0 {
			return nil, errorf("ZeroDivisionError", "zero to a negative power")
		}
		if odd(iw) {
			return iv, nil
		}
		return 0.0, nil
	}
	negate := false
	if iv < 0 {
		if iw != math.Floor(iw) {
			return complexPow(complex(iv, 0), complex(iw, 0))
		}
		iv = -iv
		negate = odd(iw)
	}
	r := 1.0
	if iv != 1 {
		r = xPow(iv, iw)
	}
	if math.IsInf(r, 0) {
		return nil, errorf("OverflowError", "(34, 'Numerical result out of range')")
	}
	if negate {
		r = -r
	}
	return r, nil
}

func intOp(op string, x, y *big.Int) (Value, error) {
	z := new(big.Int)
	if op == "mul" && x.BitLen()+y.BitLen() > maxIntBits {
		return nil, memoryError()
	}
	switch op {
	case "add":
		return normInt(z.Add(x, y)), nil
	case "sub":
		return normInt(z.Sub(x, y)), nil
	case "mul":
		return normInt(z.Mul(x, y)), nil
	case "floordiv", "mod":
		if y.Sign() == 0 {
			return nil, errorf("ZeroDivisionError", "division by zero")
		}
		q, m := new(big.Int), new(big.Int)
		q.DivMod(x, y, m)
		if m.Sign() != 0 && y.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
			m.Add(m, y)
		}
		if op == "floordiv" {
			return normInt(q), nil
		}
		return normInt(m), nil
	case "pow":
		if y.Sign() < 0 {
			fx, err := toFloat(x)
			if err != nil {
				return nil, err
			}
			fy, err := toFloat(y)
			if err != nil {
				return nil, err
			}
			return floatPow(fx, fy)
		}
		if x.CmpAbs(big.NewInt(1)) > 0 && (!y.IsInt64() || y.Int64() > int64(maxIntBits/max(x.BitLen()-1, 1))) {
			return nil, memoryError()
		}
		return normInt(z.Exp(x, y, nil)), nil
	}
	return nil, unsupported(op, x, y)
}

// Python's correctly rounded true division of two integers
func intTrueDiv(x, y *big.Int) (Value, error) {
	if y.Sign() == 0 {
		return nil, errorf("ZeroDivisionError", "division by zero")
	}
	f, _ := new(big.Rat).SetFrac(x, y).Float64()
	if math.IsInf(f, 0) {
		return nil, errorf("OverflowError", "integer division result too large for a float")
	}
	if f == 0 && (x.Sign() < 0) != (y.Sign() < 0) {
		return math.Copysign(0, -1), nil
	}
	return f, nil
}

// Python's unary minus and plus
func unaryOp(op string, v Value) (Value, error) {
	switch x := v.(type) {
	case *Undefined:
		return nil, x.fail()
	case complex128:
		if op == "-" {
			return complex(-real(x), -imag(x)), nil
		}
		return x, nil
	case float64:
		if op == "-" {
			return -x, nil
		}
		return x, nil
	case bool, int64, *big.Int:
		if op == "-" {
			return normInt(new(big.Int).Neg(toBig(x))), nil
		}
		return normInt(toBig(x)), nil
	}
	return nil, typeError("bad operand type for unary %s: %s", op, strRepr(typeName(v)))
}

// The sandbox's attribute lookup, preferring attributes and falling back to items
func (r *renderer) getattr(obj Value, name string) (Value, error) {
	key := r.getattrKey(obj, name)
	peek, err := r.loopPeek(obj, name)
	if err != nil {
		return nil, err
	}
	if err := r.measured(key, max(peek, r.opFrames(key, obj, nil, nil))); err != nil {
		return nil, err
	}
	if u, ok := obj.(*Undefined); ok {
		if hasPrivateAttr(u, name) {
			return unsafeUndefined(obj, name), nil
		}
		return nil, u.fail()
	}
	v, found, err := r.attribute(obj, name)
	if err != nil {
		return nil, err
	}
	if found {
		if unsafeAttr(obj, name) {
			return unsafeUndefined(obj, name), nil
		}
		return v, nil
	}
	if d, ok := obj.(*Dict); ok {
		if v, found, err := d.get(name); err == nil && found {
			return v, nil
		}
	}
	if t, ok := obj.(*TemplateRef); ok {
		if v, found := t.item(name); found {
			return v, nil
		}
	}
	return undefinedAttr(obj, name), nil
}

// The sandbox's item lookup, preferring items and falling back to attributes for string keys
func (r *renderer) getitem(obj Value, key Value) (Value, error) {
	if k := r.getitemKey(obj, key); k != "" {
		name, _ := key.(string)
		peek, err := r.loopPeek(obj, name)
		if err != nil {
			return nil, err
		}
		if err := r.measured(k, max(peek, r.opFrames(k, obj, nil, nil))); err != nil {
			return nil, err
		}
	}
	if u, ok := obj.(*Undefined); ok {
		return nil, u.fail()
	}
	if s, ok := key.(*sliceValue); ok {
		v, err := sliceOf(obj, s)
		if e, isErr := err.(*Error); isErr && (e.Kind == "TypeError" || e.Kind == "KeyError" || e.Kind == "IndexError") {
			return undefinedAttr(obj, key), nil
		}
		return v, err
	}
	v, found, err := itemOf(obj, key)
	if err != nil {
		return nil, err
	}
	if found {
		return v, nil
	}
	if name, ok := key.(string); ok {
		v, found, err := r.attribute(obj, name)
		if err != nil {
			return nil, err
		}
		if found {
			if unsafeAttr(obj, name) {
				return unsafeUndefined(obj, name), nil
			}
			return v, nil
		}
	}
	return undefinedAttr(obj, key), nil
}

// Python's native slicing, which jinja2 compiles without its getitem fallback so failures raise
func sliceOf(obj Value, s *sliceValue) (Value, error) {
	switch x := obj.(type) {
	case *List, *Tuple, string, Markup:
		if _, _, _, err := s.indices(0); err != nil {
			return nil, err
		}
		v, _, err := itemOf(obj, s)
		return v, err
	case *Range:
		if _, _, _, err := s.indices(0); err != nil {
			return nil, err
		}
		return rangeSlice(x, s)
	case *Dict:
		return nil, exceptionWith("KeyError", s)
	}
	return nil, typeError("'%s' object is not subscriptable", typeName(obj))
}

// A range sliced as Python computes it, the slice's exact step scaling the range's own
func rangeSlice(x *Range, s *sliceValue) (Value, error) {
	start, stop, _, err := s.indices(int(x.length()))
	if err != nil {
		return nil, err
	}
	step := big.NewInt(1)
	if _, isNone := s.step.(NoneType); !isNone {
		step = toBig(s.step)
	}
	at := func(i int) *big.Int {
		return new(big.Int).Add(x.start, new(big.Int).Mul(big.NewInt(int64(i)), x.step))
	}
	return newRange(at(start), at(stop), new(big.Int).Mul(step, x.step))
}

func unsafeUndefined(obj Value, name string) *Undefined {
	return &Undefined{
		hint:     "access to attribute " + strRepr(name) + " of " + strRepr(typeName(obj)) + " object is unsafe.",
		obj:      obj,
		hasObj:   true,
		name:     name,
		security: true,
	}
}

// Python's obj[key], reporting lookup and type failures as not found and value errors as errors
func itemOf(obj Value, key Value) (Value, bool, error) {
	if t, ok := obj.(*TemplateRef); ok {
		v, found := t.item(key)
		return v, found, nil
	}
	if d, ok := obj.(*Dict); ok {
		v, found, err := d.get(key)
		if err != nil {
			return nil, false, nil
		}
		return v, found, nil
	}
	var n int
	var at func(i int) Value
	var slice func(pos []int) Value
	switch x := obj.(type) {
	case *List:
		n = len(x.items)
		at = func(i int) Value { return x.items[i] }
		slice = func(pos []int) Value { return &List{items: pick(x.items, pos)} }
	case *Tuple:
		n = len(x.items)
		at = func(i int) Value { return x.items[i] }
		slice = func(pos []int) Value { return &Tuple{items: pick(x.items, pos)} }
	case string, Markup:
		xs, _ := asStr(x)
		rs := []rune(xs)
		_, markup := x.(Markup)
		n = len(rs)
		wrap := func(s string) Value {
			if markup {
				return Markup(s)
			}
			return s
		}
		at = func(i int) Value { return wrap(string(rs[i])) }
		slice = func(pos []int) Value {
			out := make([]rune, len(pos))
			for i, p := range pos {
				out[i] = rs[p]
			}
			return wrap(string(out))
		}
	case *Range:
		n = int(x.length())
		at = func(i int) Value { return x.at(int64(i)) }
		if k, ok := key.(*sliceValue); ok {
			v, err := rangeSlice(x, k)
			return v, err == nil, err
		}
	default:
		return nil, false, nil
	}
	switch k := key.(type) {
	case *sliceValue:
		pos, err := k.positions(n)
		if err != nil {
			if e, ok := err.(*Error); ok && e.Kind == "ValueError" {
				return nil, false, err
			}
			return nil, false, nil
		}
		return slice(pos), true, nil
	case bool, int64, *big.Int:
		b := toBig(k)
		if !b.IsInt64() {
			return nil, false, nil
		}
		i := b.Int64()
		if i < 0 {
			i += int64(n)
		}
		if i < 0 || i >= int64(n) {
			return nil, false, nil
		}
		return at(int(i)), true, nil
	}
	return nil, false, nil
}

func pick(items []Value, pos []int) []Value {
	out := make([]Value, len(pos))
	for i, p := range pos {
		out[i] = items[p]
	}
	return out
}

// Whether the sandbox refuses an existing attribute, private names and methods mutating builtins
func unsafeAttr(obj Value, name string) bool {
	if strings.HasPrefix(name, "_") {
		return true
	}
	switch obj.(type) {
	case *List:
		switch name {
		case "append", "clear", "pop", "reverse", "insert", "sort", "extend", "remove":
			return true
		}
	case *Dict:
		switch name {
		case "clear", "pop", "popitem", "setdefault", "update":
			return true
		}
	}
	return false
}

// The frames the pulls a LoopContext attribute read makes reach above the template frame, its peek or length materializing items
func (r *renderer) loopPeek(obj Value, name string) (int, error) {
	l, ok := obj.(*LoopContext)
	if !ok {
		return 0, nil
	}
	return r.loopPulls(l, func() error { return l.peekFor(name) })
}

// The frames a LoopContext's pulls reach above the template frame while pull runs
func (r *renderer) loopPulls(l *LoopContext, pull func() error) (int, error) {
	base, outer := r.frames, r.peak
	r.peak = base
	err := pull()
	local := r.peak - base
	r.peak = max(outer, r.peak)
	return local, err
}
