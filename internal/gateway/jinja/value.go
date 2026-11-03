package jinja

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Any template value, modeled on the Python object it stands for
type Value = any

// Python None
type NoneType struct{}

var none = NoneType{}

// HTML safe text, jinja2's Markup
type Markup string

// A Python list
type List struct{ items []Value }

// A Python tuple, with field names when it is a named tuple
type Tuple struct {
	items []Value
	names []string
}

// A Python range object over arbitrary size integers with its length fixed at creation
type Range struct {
	start, stop, step *big.Int
	n                 int64
}

// A range over exact bounds, failing as len does when its length passes a C ssize_t
func newRange(start, stop, step *big.Int) (*Range, error) {
	length := new(big.Int)
	if step.Sign() > 0 && start.Cmp(stop) < 0 {
		length.Sub(stop, start).Add(length, step).Sub(length, big.NewInt(1)).Quo(length, step)
	} else if step.Sign() < 0 && start.Cmp(stop) > 0 {
		length.Sub(start, stop).Sub(length, step).Sub(length, big.NewInt(1)).Quo(length, new(big.Int).Neg(step))
	}
	if !length.IsInt64() {
		return nil, errorf("OverflowError", "Python int too large to convert to C ssize_t")
	}
	return &Range{start: start, stop: stop, step: step, n: length.Int64()}, nil
}

// The item at position i, which callers keep within the range's length
func (r *Range) at(i int64) Value {
	return normInt(new(big.Int).Add(r.start, new(big.Int).Mul(big.NewInt(i), r.step)))
}

// A single pass Python iterator such as a generator
type Iterator struct {
	kind string
	name string
	next func() (Value, bool, error)
	done bool
	// The frame cost key of the filter call that created a generator, which keys the calls consuming it
	producer string
	// The filter call that created a generator
	made *filterCall
	// Items pulled so far
	taken int
	// The items a reversed iterator yields from a position on, for frame models that must not consume it
	rest func(from int) []Value
}

// A dict keys, values or items view
type DictView struct {
	kind string
	dict *Dict
}

// jinja2's namespace object
type Namespace struct{ attrs *Dict }

// jinja2's Undefined, carrying what was looked up and why it failed
type Undefined struct {
	hint     string
	obj      Value
	hasObj   bool
	name     Value
	security bool
}

// A template syntax or render failure named by the Python exception it mirrors
type Error struct {
	Kind string
	Msg  string
	Line int
}

// The message alone, as transformers surfaces template errors
func (e *Error) Error() string { return e.Msg }

func errorf(kind, format string, args ...any) error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

func typeError(format string, args ...any) error { return errorf("TypeError", format, args...) }

// Fails the way using an undefined value fails in jinja2, or with the error building its message raised
func (u *Undefined) fail() error {
	msg, err := u.message()
	if err != nil {
		return err
	}
	if u.security {
		return &Error{Kind: "SecurityError", Msg: msg}
	}
	return &Error{Kind: "UndefinedError", Msg: msg}
}

// The message jinja2 builds for an undefined value from how it was accessed
func (u *Undefined) message() (string, error) {
	if u.hint != "" {
		return u.hint, nil
	}
	name, err := pyRepr(u.name)
	if err != nil {
		return "", err
	}
	if !u.hasObj {
		return name + " is undefined", nil
	}
	if _, ok := u.name.(string); !ok {
		return objectTypeRepr(u.obj) + " has no element " + name, nil
	}
	return strRepr(objectTypeRepr(u.obj)) + " has no attribute " + name, nil
}

// Modules of the non builtin classes templates see, as object_type_repr qualifies them
var typeModules = map[string]string{
	"Markup": "markupsafe", "Namespace": "jinja2.utils", "Cycler": "jinja2.utils", "Joiner": "jinja2.utils",
	"LoopContext": "jinja2.runtime", "Macro": "jinja2.runtime", "Undefined": "jinja2.runtime", "_GroupTuple": "jinja2.filters",
	"TemplateReference": "jinja2.runtime", "BlockReference": "jinja2.runtime",
}

// jinja2's object_type_repr, the class name qualified by its module unless it is a builtin
func objectTypeRepr(v Value) string {
	if _, ok := v.(NoneType); ok {
		return "None"
	}
	name := typeName(v)
	if mod, ok := typeModules[name]; ok {
		return mod + "." + name + " object"
	}
	return name + " object"
}

func undefinedName(name string) *Undefined { return &Undefined{name: name} }

func undefinedAttr(obj Value, name Value) *Undefined {
	return &Undefined{obj: obj, hasObj: true, name: name}
}

// An ordered Python dict
type Dict struct {
	keys []Value
	vals []Value
	idx  map[string]int
}

func newDict() *Dict { return &Dict{idx: map[string]int{}} }

// Sets a key, keeping the position of a key already present
func (d *Dict) set(k, v Value) error {
	h, err := hashKey(k)
	if err != nil {
		return unhashable("dict key", k, err)
	}
	if i, ok := d.idx[h]; ok {
		d.vals[i] = v
		return nil
	}
	d.idx[h] = len(d.keys)
	d.keys = append(d.keys, k)
	d.vals = append(d.vals, v)
	return nil
}

// Sets a string key, which is always hashable
func (d *Dict) put(k string, v Value) {
	_ = d.set(k, v)
}

// The value under a key and whether it is present
func (d *Dict) get(k Value) (Value, bool, error) {
	h, err := hashKey(k)
	if err != nil {
		return nil, false, unhashable("dict key", k, err)
	}
	i, ok := d.idx[h]
	if !ok {
		return nil, false, nil
	}
	return d.vals[i], true, nil
}

func (d *Dict) lookup(k string) (Value, bool) {
	i, ok := d.idx["s"+k]
	if !ok {
		return nil, false
	}
	return d.vals[i], true
}

func (d *Dict) copy() *Dict {
	out := newDict()
	for i, k := range d.keys {
		_ = out.set(k, d.vals[i])
	}
	return out
}

// A key that equal Python values share, failing for unhashable values
func hashKey(v Value) (string, error) {
	switch x := v.(type) {
	case string:
		return "s" + x, nil
	case Markup:
		return "s" + string(x), nil
	case NoneType:
		return "n", nil
	case bool:
		if x {
			return "i1", nil
		}
		return "i0", nil
	case int64:
		return "i" + strconv.FormatInt(x, 10), nil
	case *big.Int:
		return "i" + x.String(), nil
	case float64:
		if x == math.Trunc(x) && !math.IsInf(x, 0) {
			f := new(big.Float).SetFloat64(x)
			n, _ := f.Int(nil)
			return "i" + n.String(), nil
		}
		return "f" + strconv.FormatUint(math.Float64bits(x), 16), nil
	case complex128:
		if key, ok := complexHashKey(x); ok {
			return key, nil
		}
		return hashKey(real(x))
	case *Tuple:
		var b strings.Builder
		b.WriteString("t(")
		for _, it := range x.items {
			h, err := hashKey(it)
			if err != nil {
				return "", err
			}
			b.WriteString(strconv.Itoa(len(h)))
			b.WriteByte(':')
			b.WriteString(h)
		}
		b.WriteByte(')')
		return b.String(), nil
	case *Undefined:
		return "u", nil
	case *List, *Dict:
		return "", typeError("unhashable type: %s", strRepr(typeName(v)))
	case *DictView:
		if x.kind != "values" {
			return "", typeError("unhashable type: %s", strRepr(typeName(v)))
		}
	}
	return fmt.Sprintf("p%p", v), nil
}

// Normalizes a big integer to int64 when it fits
func normInt(n *big.Int) Value {
	if n.IsInt64() {
		return n.Int64()
	}
	return n
}

// The Python type name of a value
func typeName(v Value) string {
	switch x := v.(type) {
	case NoneType:
		return "NoneType"
	case bool:
		return "bool"
	case int64, *big.Int:
		return "int"
	case float64:
		return "float"
	case complex128:
		return "complex"
	case string:
		return "str"
	case Markup:
		return "Markup"
	case *List:
		return "list"
	case *Tuple:
		if x.names != nil {
			return "_GroupTuple"
		}
		return "tuple"
	case *Dict:
		return "dict"
	case *Range:
		return "range"
	case *Iterator:
		return x.kind
	case *DictView:
		return "dict_" + x.kind
	case *Namespace:
		return "Namespace"
	case *Undefined:
		return "Undefined"
	case *LoopContext:
		return "LoopContext"
	case *Macro:
		return "Macro"
	case *Builtin:
		if x.class != "" {
			return "type"
		}
		if x.self != nil {
			return "builtin_function_or_method"
		}
		return "function"
	case *Cycler:
		return "Cycler"
	case *Joiner:
		return "Joiner"
	case *TemplateRef:
		return "TemplateReference"
	case *BlockRef:
		return "BlockReference"
	}
	return fmt.Sprintf("%T", v)
}

// Python truth value
func truthy(v Value) bool {
	switch x := v.(type) {
	case NoneType:
		return false
	case *Undefined:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case *big.Int:
		return x.Sign() != 0
	case float64:
		return x != 0
	case complex128:
		return x != 0
	case string:
		return x != ""
	case Markup:
		return x != ""
	case *List:
		return len(x.items) > 0
	case *Tuple:
		return len(x.items) > 0
	case *Dict:
		return len(x.keys) > 0
	case *Range:
		return x.length() > 0
	case *DictView:
		return len(x.dict.keys) > 0
	case *Namespace:
		return true
	}
	return true
}

func (r *Range) length() int64 { return r.n }

// Python str() of a value, failing as CPython does on integers past its decimal digit limit
func pyStr(v Value) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case Markup:
		return string(x), nil
	case *Undefined:
		return "", nil
	case NoneType:
		return "None", nil
	case bool:
		if x {
			return "True", nil
		}
		return "False", nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case *big.Int:
		return decimalString(x)
	case float64:
		return floatRepr(x), nil
	case complex128:
		return complexRepr(x), nil
	}
	return pyRepr(v)
}

// Python repr() of a value, failing as CPython does on integers past its decimal digit limit
func pyRepr(v Value) (string, error) {
	switch x := v.(type) {
	case string:
		return strRepr(x), nil
	case Markup:
		return "Markup(" + strRepr(string(x)) + ")", nil
	case *Undefined:
		return "Undefined", nil
	case NoneType, bool, int64, *big.Int, float64, complex128:
		return pyStr(v)
	case *List:
		items, err := joinRepr(x.items)
		return "[" + items + "]", err
	case *Tuple:
		items, err := joinRepr(x.items)
		if len(x.items) == 1 {
			return "(" + items + ",)", err
		}
		return "(" + items + ")", err
	case *Dict:
		return dictRepr(x)
	case *Range:
		bounds := []Value{x.start, x.stop}
		if x.step.Cmp(big.NewInt(1)) != 0 {
			bounds = append(bounds, x.step)
		}
		s, err := joinRepr(bounds)
		return "range(" + s + ")", err
	case *DictView:
		var items []Value
		for i, k := range x.dict.keys {
			switch x.kind {
			case "keys":
				items = append(items, k)
			case "values":
				items = append(items, x.dict.vals[i])
			default:
				items = append(items, &Tuple{items: []Value{k, x.dict.vals[i]}})
			}
		}
		s, err := joinRepr(items)
		return "dict_" + x.kind + "([" + s + "])", err
	case *Namespace:
		s, err := dictRepr(x.attrs)
		return "<Namespace " + s + ">", err
	case *sliceValue:
		s, err := joinRepr([]Value{x.start, x.stop, x.step})
		return "slice(" + s + ")", err
	case *LoopContext:
		if err := x.lengthPulls(2); err != nil {
			return "", err
		}
		return fmt.Sprintf("<LoopContext %d/%d>", x.index0+1, x.length()), nil
	case *Macro:
		if x.name == "" {
			return "<Macro anonymous>", nil
		}
		if x.anonymous {
			return "<Macro anonymous>", nil
		}
		return "<Macro " + strRepr(x.name) + ">", nil
	case *Builtin:
		if x.class != "" {
			return "<class " + strRepr(x.class) + ">", nil
		}
		if x.self != nil {
			return fmt.Sprintf("<built-in method %s of %s object at %p>", x.name, typeName(x.self), x), nil
		}
		return fmt.Sprintf("<function %s at %p>", x.name, x), nil
	case *Iterator:
		if x.name != "" {
			return fmt.Sprintf("<%s object %s at %p>", x.kind, x.name, x), nil
		}
		return fmt.Sprintf("<%s object at %p>", x.kind, x), nil
	case *Cycler:
		return fmt.Sprintf("<jinja2.utils.Cycler object at %p>", x), nil
	case *Joiner:
		return fmt.Sprintf("<jinja2.utils.Joiner object at %p>", x), nil
	case *TemplateRef:
		return "<TemplateReference None>", nil
	case *BlockRef:
		return x.repr(), nil
	}
	return fmt.Sprintf("<%T>", v), nil
}

// The reprs of items joined by commas, stopping at the first that fails
func joinRepr(items []Value) (string, error) {
	if reprFloor(&List{items: items}) > maxValueBytes {
		return "", memoryError()
	}
	parts := make([]string, len(items))
	total := 0
	for i, it := range items {
		s, err := pyRepr(it)
		if err == nil {
			total += len(s) + 2
			err = checkBytes(total)
		}
		if err != nil {
			return "", err
		}
		parts[i] = s
	}
	return strings.Join(parts, ", "), nil
}

func dictRepr(d *Dict) (string, error) {
	parts := make([]string, len(d.keys))
	total := 0
	for i, k := range d.keys {
		ks, err := pyRepr(k)
		if err != nil {
			return "", err
		}
		vs, err := pyRepr(d.vals[i])
		if err != nil {
			return "", err
		}
		total += len(ks) + len(vs) + 4
		if err := checkBytes(total); err != nil {
			return "", err
		}
		parts[i] = ks + ": " + vs
	}
	return "{" + strings.Join(parts, ", ") + "}", nil
}

// Ten to the power of CPython's default sys.int_info.default_max_str_digits, the first integer it refuses to print
var intDigitLimit = new(big.Int).Exp(big.NewInt(10), big.NewInt(maxStrDigits), nil)

// CPython's default limit on decimal digits converted between int and str
const maxStrDigits = 4300

// Decimal digits of an integer, refused past CPython's digit limit with the ValueError str() raises
func decimalString(n *big.Int) (string, error) {
	if n.CmpAbs(intDigitLimit) >= 0 {
		return "", errorf("ValueError", "Exceeds the limit (%d digits) for integer string conversion; use sys.set_int_max_str_digits() to increase the limit", maxStrDigits)
	}
	return n.String(), nil
}

// Refuses a decimal digit string longer than CPython's limit with the ValueError int() raises
func checkParseDigits(digits int) error {
	if digits > maxStrDigits {
		return errorf("ValueError", "Exceeds the limit (%d digits) for integer string conversion: value has %d digits; use sys.set_int_max_str_digits() to increase the limit", maxStrDigits, digits)
	}
	return nil
}

// Python's repr of a float, shortest round trip digits in Python's layout
func floatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case f == 0:
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	sign := ""
	if f < 0 {
		sign = "-"
		f = -f
	}
	digits, decpt := shortestDigits(f)
	if decpt > -4 && decpt <= 16 {
		switch {
		case decpt <= 0:
			return sign + "0." + strings.Repeat("0", -decpt) + digits
		case decpt >= len(digits):
			return sign + digits + strings.Repeat("0", decpt-len(digits)) + ".0"
		default:
			return sign + digits[:decpt] + "." + digits[decpt:]
		}
	}
	mant := digits[:1]
	if len(digits) > 1 {
		mant += "." + digits[1:]
	}
	exp := decpt - 1
	esign := "+"
	if exp < 0 {
		esign = "-"
		exp = -exp
	}
	return fmt.Sprintf("%s%se%s%02d", sign, mant, esign, exp)
}

// The shortest round trip digits of a positive float and the decimal point position
func shortestDigits(f float64) (string, int) {
	s := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expPart, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(expPart)
	digits := strings.Replace(mant, ".", "", 1)
	digits = strings.TrimRight(digits, "0")
	if digits == "" {
		digits = "0"
	}
	return digits, exp + 1
}

// Python == between two values
func pyEq(a, b Value) bool {
	if c, ok := a.(complex128); ok {
		return complexEq(c, b)
	}
	if c, ok := b.(complex128); ok {
		return complexEq(c, a)
	}
	if isNumber(a) && isNumber(b) {
		c, ok := numCompare(a, b)
		return ok && c == 0
	}
	switch x := a.(type) {
	case NoneType:
		_, ok := b.(NoneType)
		return ok
	case *Undefined:
		_, ok := b.(*Undefined)
		return ok
	case string, Markup:
		s, ok := asStr(b)
		xs, _ := asStr(x)
		return ok && s == xs
	case *List:
		y, ok := b.(*List)
		return ok && seqEq(x.items, y.items)
	case *Tuple:
		y, ok := b.(*Tuple)
		return ok && seqEq(x.items, y.items)
	case *Dict:
		y, ok := b.(*Dict)
		if !ok || len(x.keys) != len(y.keys) {
			return false
		}
		for i, k := range x.keys {
			v, found, _ := y.get(k)
			if !found || !pyEq(x.vals[i], v) {
				return false
			}
		}
		return true
	case *Range:
		y, ok := b.(*Range)
		if !ok {
			return false
		}
		n := x.length()
		if n != y.length() {
			return false
		}
		return n == 0 || (x.start.Cmp(y.start) == 0 && (n == 1 || x.step.Cmp(y.step) == 0))
	case *DictView:
		y, ok := b.(*DictView)
		if !ok || x.kind == "values" || y.kind == "values" {
			return a == b
		}
		xs, _ := iterValues(x)
		ys, _ := iterValues(y)
		if len(xs) != len(ys) {
			return false
		}
		set := newDict()
		for _, it := range ys {
			if set.set(it, true) != nil {
				return false
			}
		}
		for _, it := range xs {
			if _, found, err := set.get(it); err != nil || !found {
				return false
			}
		}
		return true
	}
	return a == b
}

func seqEq(a, b []Value) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !pyEq(a[i], b[i]) {
			return false
		}
	}
	return true
}

// The text of a str or Markup value
func asStr(v Value) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case Markup:
		return string(x), true
	}
	return "", false
}

func isNumber(v Value) bool {
	switch v.(type) {
	case bool, int64, *big.Int, float64:
		return true
	}
	return false
}

func isIntegral(v Value) bool {
	switch v.(type) {
	case bool, int64, *big.Int:
		return true
	}
	return false
}

// An integral value as a big integer
func toBig(v Value) *big.Int {
	switch x := v.(type) {
	case bool:
		if x {
			return big.NewInt(1)
		}
		return big.NewInt(0)
	case int64:
		return big.NewInt(x)
	case *big.Int:
		return x
	}
	return nil
}

// A number as a float, failing the way Python does for integers too large for a float
func toFloat(v Value) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case int64:
		return float64(x), nil
	case *big.Int:
		f, acc := new(big.Float).SetInt(x).Float64()
		if math.IsInf(f, 0) || (acc != big.Exact && math.Abs(f) == math.MaxFloat64) {
			return 0, errorf("OverflowError", "int too large to convert to float")
		}
		return f, nil
	}
	return 0, typeError("must be real number, not %s", typeName(v))
}

// Compares two numbers exactly, mixing integers and floats as Python does
func numCompare(a, b Value) (int, bool) {
	fa, aFloat := a.(float64)
	fb, bFloat := b.(float64)
	if !aFloat && !bFloat {
		return toBig(a).Cmp(toBig(b)), true
	}
	if aFloat && bFloat {
		if math.IsNaN(fa) || math.IsNaN(fb) {
			return 0, false
		}
		switch {
		case fa < fb:
			return -1, true
		case fa > fb:
			return 1, true
		}
		return 0, true
	}
	if aFloat {
		c, ok := floatIntCompare(fa, toBig(b))
		return c, ok
	}
	c, ok := floatIntCompare(fb, toBig(a))
	return -c, ok
}

func floatIntCompare(f float64, n *big.Int) (int, bool) {
	if math.IsNaN(f) {
		return 0, false
	}
	if math.IsInf(f, 1) {
		return 1, true
	}
	if math.IsInf(f, -1) {
		return -1, true
	}
	return new(big.Float).SetFloat64(f).Cmp(new(big.Float).SetInt(n)), true
}

// Python ordering comparison for <, <=, > and >=
func pyOrder(op string, a, b Value) (bool, error) {
	if u, ok := a.(*Undefined); ok {
		return false, u.fail()
	}
	if u, ok := b.(*Undefined); ok {
		return false, u.fail()
	}
	c, err := orderCompare(op, a, b)
	if err != nil {
		return false, err
	}
	switch op {
	case "<":
		return c == -1, nil
	case "<=":
		return c == -1 || c == 0, nil
	case ">":
		return c == 1, nil
	}
	return c == 1 || c == 0, nil
}

// Three way comparison, 2 meaning unordered NaN, failing for types Python cannot order
func orderCompare(op string, a, b Value) (int, error) {
	if isComplex(a) || isComplex(b) {
		return 0, typeError("%s not supported between instances of %s and %s", strRepr(op), strRepr(typeName(a)), strRepr(typeName(b)))
	}
	if isNumber(a) && isNumber(b) {
		c, ok := numCompare(a, b)
		if !ok {
			return 2, nil
		}
		return c, nil
	}
	if sa, ok := asStr(a); ok {
		if sb, ok := asStr(b); ok {
			return strings.Compare(sa, sb), nil
		}
	}
	var xs, ys []Value
	switch x := a.(type) {
	case *List:
		if y, ok := b.(*List); ok {
			xs, ys = x.items, y.items
		}
	case *Tuple:
		if y, ok := b.(*Tuple); ok {
			xs, ys = x.items, y.items
		}
	}
	if xs == nil && ys == nil {
		_, la := a.(*List)
		_, ta := a.(*Tuple)
		_, lb := b.(*List)
		_, tb := b.(*Tuple)
		if !((la && lb) || (ta && tb)) {
			return 0, typeError("%s not supported between instances of %s and %s", strRepr(op), strRepr(typeName(a)), strRepr(typeName(b)))
		}
	}
	for i := 0; i < len(xs) && i < len(ys); i++ {
		x, y := xs[i], ys[i]
		if sameObject(x, y) || looseEqual(x, y) {
			continue
		}
		if u, ok := x.(*Undefined); ok {
			return 0, u.fail()
		}
		if u, ok := y.(*Undefined); ok {
			return 0, u.fail()
		}
		return orderCompare(op, x, y)
	}
	switch {
	case len(xs) < len(ys):
		return -1, nil
	case len(xs) > len(ys):
		return 1, nil
	}
	return 0, nil
}

// Iterates a value to a slice the way Python's iter() walks it, consuming iterators
func iterValues(v Value) ([]Value, error) {
	switch x := v.(type) {
	case *List:
		return append([]Value(nil), x.items...), nil
	case *Tuple:
		return append([]Value(nil), x.items...), nil
	case string, Markup:
		s, _ := asStr(x)
		if err := checkBytes(utf8.RuneCountInString(s) * slotBytes); err != nil {
			return nil, err
		}
		return strChars(s), nil
	case *Dict:
		return append([]Value(nil), x.keys...), nil
	case *Range:
		n := x.length()
		out := make([]Value, 0, n)
		for i := int64(0); i < n; i++ {
			out = append(out, x.at(i))
		}
		return out, nil
	case *DictView:
		out := make([]Value, len(x.dict.keys))
		for i, k := range x.dict.keys {
			switch x.kind {
			case "keys":
				out[i] = k
			case "values":
				out[i] = x.dict.vals[i]
			default:
				out[i] = &Tuple{items: []Value{k, x.dict.vals[i]}}
			}
		}
		return out, nil
	case *Iterator:
		var out []Value
		for {
			it, ok, err := x.pull()
			if err != nil {
				return nil, err
			}
			if !ok {
				return out, nil
			}
			if err := checkBytes((len(out) + 1) * slotBytes); err != nil {
				return nil, err
			}
			out = append(out, it)
		}
	case *Undefined:
		return nil, nil
	case *LoopContext:
		return x.drain()
	case *TemplateRef:
		return nil, &Error{Kind: "KeyError", Msg: "0"}
	}
	return nil, typeError("%s object is not iterable", strRepr(typeName(v)))
}

// The next item of an iterator, false once it is exhausted
func (it *Iterator) pull() (Value, bool, error) {
	if it.done {
		return nil, false, nil
	}
	v, ok, err := it.next()
	if err != nil || !ok {
		it.done = true
	}
	if ok {
		it.taken++
	}
	return v, ok, err
}

func strChars(s string) []Value {
	out := make([]Value, 0, len(s))
	for _, r := range s {
		out = append(out, string(r))
	}
	return out
}

// Python len()
func pyLen(v Value) (int, error) {
	switch x := v.(type) {
	case string:
		return runeLen(x), nil
	case Markup:
		return runeLen(string(x)), nil
	case *List:
		return len(x.items), nil
	case *Tuple:
		return len(x.items), nil
	case *Dict:
		return len(x.keys), nil
	case *Range:
		return int(x.length()), nil
	case *DictView:
		return len(x.dict.keys), nil
	case *Undefined:
		return 0, nil
	case *LoopContext:
		err := x.lengthPulls(2)
		return x.length(), err
	}
	return 0, typeError("object of type %s has no len()", strRepr(typeName(v)))
}

// Whether Python's iter accepts the value
func iterable(v Value) bool {
	switch v.(type) {
	case *List, *Tuple, string, Markup, *Dict, *Range, *DictView, *Iterator, *Undefined:
		return true
	}
	return false
}

// Python's unpacking of a value into n targets, with its exact errors
func unpack(v Value, n int) ([]Value, error) {
	if !iterable(v) {
		return nil, typeError("cannot unpack non-iterable %s object", typeName(v))
	}
	items, err := iterValues(v)
	if err != nil {
		return nil, err
	}
	if len(items) < n {
		return nil, errorf("ValueError", "not enough values to unpack (expected %d, got %d)", n, len(items))
	}
	if len(items) > n {
		switch v.(type) {
		case *List, *Tuple, *Dict:
			return nil, errorf("ValueError", "too many values to unpack (expected %d, got %d)", n, len(items))
		}
		return nil, errorf("ValueError", "too many values to unpack (expected %d)", n)
	}
	return items, nil
}

// Python's `in` operator
func pyContains(container, item Value) (bool, error) {
	switch c := container.(type) {
	case string, Markup:
		s, ok := asStr(item)
		if !ok {
			return false, typeError("'in <string>' requires string as left operand, not %s", typeName(item))
		}
		cs, _ := asStr(c)
		return strings.Contains(cs, s), nil
	case *Dict:
		_, found, err := c.get(item)
		return found, err
	case *DictView:
		if c.kind == "keys" {
			_, found, err := c.dict.get(item)
			return found, err
		}
	case *Range:
		if isIntegral(item) {
			i := toBig(item)
			if c.step.Sign() > 0 && (i.Cmp(c.start) < 0 || i.Cmp(c.stop) >= 0) || c.step.Sign() < 0 && (i.Cmp(c.start) > 0 || i.Cmp(c.stop) <= 0) {
				return false, nil
			}
			return new(big.Int).Rem(new(big.Int).Sub(i, c.start), c.step).Sign() == 0, nil
		}
	case NoneType, bool, int64, *big.Int, float64, *Namespace, *Macro, *Builtin, *Cycler, *Joiner:
		return false, typeError("argument of type %s is not a container or iterable", strRepr(typeName(container)))
	case *Iterator, *LoopContext:
		next, err := pullOf(container)
		if err != nil {
			return false, err
		}
		for {
			it, ok, err := next()
			if err != nil || !ok {
				return false, err
			}
			if pyEq(item, it) || pyEq(it, item) {
				return true, nil
			}
		}
	}
	items, err := iterValues(container)
	if err != nil {
		return false, err
	}
	for _, it := range items {
		if pyEq(item, it) || pyEq(it, item) {
			return true, nil
		}
	}
	return false, nil
}

// Sorts values by Python ordering, stable, failing on incomparable pairs
func pySort(items []Value, keys []Value, reverse bool) error {
	return pySortObserved(items, keys, reverse, nil)
}

// Sorts like pySort, passing the frames each comparison pushes to observe when it is set
func pySortObserved(items []Value, keys []Value, reverse bool, observe func(int)) error {
	order, err := listsort(len(keys), reverse, func(x, y int) (bool, error) {
		if observe != nil {
			f, _ := compareFrames("<", keys[x], keys[y])
			observe(f)
		}
		return pyOrder("<", keys[x], keys[y])
	})
	if err != nil {
		return err
	}
	sorted := make([]Value, len(items))
	sortedKeys := make([]Value, len(items))
	for i, j := range order {
		sorted[i] = items[j]
		sortedKeys[i] = keys[j]
	}
	copy(items, sorted)
	copy(keys, sortedKeys)
	return nil
}

// An exception holding one argument, its message what Python prints for it or the marker a failing str() leaves
func exceptionWith(kind string, arg Value) error {
	text := pyStr
	if kind == "KeyError" {
		text = pyRepr
	}
	msg, err := text(arg)
	if err != nil {
		msg = "<exception str() failed>"
	}
	return &Error{Kind: kind, Msg: msg}
}

// Whether a value is a Python int or bool, the types range takes its arithmetic fast path for
func isExactInt(v Value) bool {
	switch v.(type) {
	case bool, int64, *big.Int:
		return true
	}
	return false
}

// The TypeError CPython raises for an unhashable value used as a dict key or set element
func unhashable(role string, v Value, err error) error {
	return typeError("cannot use %s as a %s (%s)", strRepr(typeName(v)), role, err.Error())
}
