package jinja

import (
	"fmt"
	"html"
	"math"
	"math/big"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
)

// A filter receiving the filtered value and the call's arguments
type filterFunc func(r *renderer, v Value, args []Value, kw *Dict) (Value, error)

var builtinFilters map[string]filterFunc

func init() {
	builtinFilters = map[string]filterFunc{
		"abs":            filterAbs,
		"attr":           filterAttr,
		"batch":          filterBatch,
		"capitalize":     strFilter("do_capitalize", pyCapitalize),
		"center":         filterCenter,
		"count":          filterLength,
		"d":              filterDefault,
		"default":        filterDefault,
		"dictsort":       filterDictsort,
		"e":              filterEscape,
		"escape":         filterEscape,
		"filesizeformat": filterFilesizeformat,
		"first":          filterFirst,
		"float":          filterFloat,
		"forceescape":    filterForceescape,
		"format":         filterFormat,
		"groupby":        filterGroupby,
		"indent":         filterIndent,
		"int":            filterInt,
		"items":          filterItems,
		"join":           filterJoin,
		"last":           filterLast,
		"length":         filterLength,
		"list":           filterList,
		"lower":          strFilter("do_lower", pyLower),
		"map":            filterMap,
		"max":            minMax(false),
		"min":            minMax(true),
		"pprint":         filterPprint,
		"random":         filterRandom,
		"reject":         selectFilter(false, false),
		"rejectattr":     selectFilter(false, true),
		"replace":        filterReplace,
		"reverse":        filterReverse,
		"round":          filterRound,
		"safe":           filterSafe,
		"select":         selectFilter(true, false),
		"selectattr":     selectFilter(true, true),
		"slice":          filterSlice,
		"sort":           filterSort,
		"string":         filterString,
		"striptags":      filterStriptags,
		"sum":            filterSum,
		"title":          filterTitle,
		"tojson":         filterTojson,
		"trim":           filterTrim,
		"truncate":       filterTruncate,
		"unique":         filterUnique,
		"upper":          strFilter("do_upper", pyUpper),
		"urlencode":      filterUrlencode,
		"urlize":         filterUrlize,
		"wordcount":      filterWordcount,
		"wordwrap":       filterWordwrap,
		"xmlattr":        filterXmlattr,
	}
}

// Calls a filter by name, failing at render time for names jinja2 lets through compilation
func (r *renderer) callFilter(name string, v Value, args []Value, kw *Dict) (Value, error) {
	f, ok := builtinFilters[name]
	if !ok {
		return nil, errorf("TemplateRuntimeError", "No filter named %s.", strRepr(name))
	}
	key := builtinCallKey(name, true, v, args, kw)
	pulls := 0
	if l, ok := v.(*LoopContext); ok {
		depth := lengthHintDepth(name, r.autoescape, kw, args)
		if name == "length" || name == "count" {
			depth = 2
		}
		if depth > 0 {
			var err error
			if pulls, err = r.loopPulls(l, func() error { return l.lengthPulls(depth) }); err != nil {
				return nil, err
			}
			pulls = max(pulls, depth)
		}
	}
	if err := r.measured(key, max(pulls, r.opFrames(key, v, args, kw))); err != nil {
		return nil, err
	}
	out, err := f(r, v, args, kw)
	if it, ok := out.(*Iterator); ok && it.kind == "generator" && it.producer == "" {
		it.producer = key
		it.made = &filterCall{name: name, v: v, args: args, kw: kw}
		switch src := v.(type) {
		case *Iterator:
			it.made.skip = src.taken
		case *LoopContext:
			it.made.skip = src.consumed
		}
	}
	return out, err
}

// Binds a filter's arguments after its value to a Python signature
func filterArgs(fname string, v Value, params []param, args []Value, kw *Dict) ([]Value, error) {
	return bindArgs(fname, params, append([]Value{v}, args...), kw)
}

// soft_str, the value itself when it is a string and its str() otherwise
func softStr(v Value) (Value, error) {
	if _, ok := asStr(v); ok {
		return v, nil
	}
	return pyStr(v)
}

// A filter applying a str method to soft_str of its value, Markup staying Markup as markupsafe's methods return
func strFilter(fname string, f func(string) string) filterFunc {
	return func(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
		a, err := filterArgs(fname, v, []param{req("s")}, args, kw)
		if err != nil {
			return nil, err
		}
		s, err := pyStr(a[0])
		if err != nil {
			return nil, err
		}
		if isMarkup(a[0]) {
			return Markup(f(s)), nil
		}
		return f(s), nil
	}
}

func filterAbs(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	if len(args) > 0 || len(kw.keys) > 0 {
		return nil, typeError("abs() takes exactly one argument (%d given)", len(args)+len(kw.keys)+1)
	}
	switch x := v.(type) {
	case float64:
		return math.Abs(x), nil
	case complex128:
		return complexAbs(x)
	case bool, int64, *big.Int:
		return normInt(new(big.Int).Abs(toBig(x))), nil
	}
	return nil, typeError("bad operand type for abs(): %s", strRepr(typeName(v)))
}

func filterAttr(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_attr", v, []param{req("obj"), req("name")}, args, kw)
	if err != nil {
		return nil, err
	}
	name, ok := asStr(a[1])
	if !ok {
		return nil, typeError("attribute name must be string, not %s", strRepr(typeName(a[1])))
	}
	if u, isU := a[0].(*Undefined); isU {
		return nil, u.fail()
	}
	if _, found, err := r.attribute(a[0], name); err != nil || !found {
		return undefinedAttr(a[0], name), err
	}
	return r.getattr(a[0], name)
}

// A single pass iterator over any iterable, lazy for iterators
func pullOf(v Value) (func() (Value, bool, error), error) {
	switch x := v.(type) {
	case *Iterator:
		return x.pull, nil
	case *LoopContext:
		return x.puller()
	}
	items, err := iterValues(v)
	if err != nil {
		return nil, err
	}
	i := 0
	return func() (Value, bool, error) {
		if i >= len(items) {
			return nil, false, nil
		}
		i++
		return items[i-1], true, nil
	}, nil
}

// A generator of the named jinja2 function whose body starts on first pull, as Python generators do
func generator(name string, start func() (func() (Value, bool, error), error)) *Iterator {
	var next func() (Value, bool, error)
	return &Iterator{kind: "generator", name: name, next: func() (Value, bool, error) {
		if next == nil {
			n, err := start()
			if err != nil {
				return nil, false, err
			}
			next = n
		}
		return next()
	}}
}

func emptyPull() (Value, bool, error) { return nil, false, nil }

func filterBatch(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_batch", v, []param{req("value"), req("linecount"), opt("fill_with", none)}, args, kw)
	if err != nil {
		return nil, err
	}
	return generator("do_batch", func() (func() (Value, bool, error), error) {
		src, err := pullOf(a[0])
		if err != nil {
			return nil, err
		}
		// Whether the pending batch has reached linecount, compared as Python's == compares them
		full := func(pending []Value) bool { return pyEq(int64(len(pending)), a[1]) }
		done := false
		var pending []Value
		return func() (Value, bool, error) {
			if done {
				return nil, false, nil
			}
			for {
				it, ok, err := src()
				if err != nil {
					return nil, false, err
				}
				if !ok {
					done = true
					if len(pending) == 0 {
						return nil, false, nil
					}
					if _, isNone := a[2].(NoneType); !isNone {
						short, err := compareOp("lt", int64(len(pending)), a[1])
						if err != nil {
							return nil, false, err
						}
						if short {
							missing, err := binaryOp("sub", a[1], int64(len(pending)))
							if err != nil {
								return nil, false, err
							}
							fill, err := binaryOp("mul", &List{items: []Value{a[2]}}, missing)
							if err != nil {
								return nil, false, err
							}
							pending = append(pending, fill.(*List).items...)
						}
					}
					out := &List{items: pending}
					return out, true, nil
				}
				if full(pending) {
					out := &List{items: pending}
					pending = []Value{it}
					return out, true, nil
				}
				pending = append(pending, it)
			}
		}, nil
	}), nil
}

func filterCenter(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_center", v, []param{req("value"), opt("width", int64(80))}, args, kw)
	if err != nil {
		return nil, err
	}
	w, err := argSsize(a[1])
	if err != nil {
		return nil, err
	}
	s, err := pyStr(a[0])
	if err != nil {
		return nil, err
	}
	if err := checkFill(w, ' '); err != nil {
		return nil, err
	}
	return pyCenter(s, w, ' '), nil
}

func filterLength(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	if len(args) > 0 || len(kw.keys) > 0 {
		return nil, typeError("len() takes exactly one argument (%d given)", len(args)+len(kw.keys)+1)
	}
	n, err := pyLen(v)
	return int64(n), err
}

func filterDefault(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_default", v, []param{req("value"), opt("default_value", ""), opt("boolean", false)}, args, kw)
	if err != nil {
		return nil, err
	}
	if _, undef := a[0].(*Undefined); undef || truthy(a[2]) && !truthy(a[0]) {
		return a[1], nil
	}
	return a[0], nil
}

// Lowercases strings so sorting and grouping ignore case
func ignoreCase(v Value) Value {
	if s, ok := v.(string); ok {
		return pyLower(s)
	}
	if m, ok := v.(Markup); ok {
		return Markup(pyLower(string(m)))
	}
	return v
}

func filterDictsort(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_dictsort", v, []param{req("value"), opt("case_sensitive", false), opt("by", "key"), opt("reverse", false)}, args, kw)
	if err != nil {
		return nil, err
	}
	pos := 0
	switch a[2] {
	case "key":
	case "value":
		pos = 1
	default:
		return nil, errorf("FilterArgumentError", `You can only sort by either "key" or "value"`)
	}
	d, ok := a[0].(*Dict)
	if !ok {
		return nil, itemsError(a[0])
	}
	items := make([]Value, len(d.keys))
	keys := make([]Value, len(d.keys))
	for i, k := range d.keys {
		items[i] = &Tuple{items: []Value{k, d.vals[i]}}
		keys[i] = []Value{k, d.vals[i]}[pos]
		if !truthy(a[1]) {
			keys[i] = ignoreCase(keys[i])
		}
	}
	if err := pySort(items, keys, truthy(a[3])); err != nil {
		return nil, err
	}
	return &List{items: items}, nil
}

func filterEscape(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	if len(args) > 0 || len(kw.keys) > 0 {
		return nil, typeError("escape() takes exactly 1 positional argument (%d given)", len(args)+1)
	}
	return escapeValue(v)
}

func filterForceescape(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_forceescape", v, []param{req("value")}, args, kw)
	if err != nil {
		return nil, err
	}
	s, err := pyStr(a[0])
	return Markup(htmlEscape(s)), err
}

func filterFilesizeformat(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_filesizeformat", v, []param{req("value"), opt("binary", false)}, args, kw)
	if err != nil {
		return nil, err
	}
	b, err := pyFloatConvert(a[0])
	if err != nil {
		return nil, err
	}
	base := 1000.0
	prefixes := []string{"kB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB"}
	if truthy(a[1]) {
		base = 1024
		prefixes = []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB", "ZiB", "YiB"}
	}
	if b == 1 {
		return "1 Byte", nil
	}
	if b < base {
		return fmt.Sprintf("%d Bytes", int64(b)), nil
	}
	var unit float64
	var prefix string
	for i, p := range prefixes {
		unit = math.Pow(base, float64(i+2))
		prefix = p
		if b < unit {
			break
		}
	}
	return pyFloatFormat(base*b/unit, 'f', 1, false, false, false, false, false) + " " + prefix, nil
}

func filterFirst(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("sync_do_first", v, []param{req("seq")}, args, kw)
	if err != nil {
		return nil, err
	}
	src, err := pullOf(a[0])
	if err != nil {
		return nil, err
	}
	it, ok, err := src()
	if err != nil {
		return nil, err
	}
	if !ok {
		return &Undefined{hint: "No first item, sequence was empty."}, nil
	}
	return it, nil
}

func filterLast(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_last", v, []param{req("seq")}, args, kw)
	if err != nil {
		return nil, err
	}
	switch a[0].(type) {
	case *Undefined:
		return &Undefined{hint: "No last item, sequence was empty."}, nil
	case *Iterator, NoneType, bool, int64, *big.Int, float64, *Namespace, *Macro, *Builtin, *Cycler, *Joiner:
		return nil, typeError("%s object is not reversible", strRepr(typeName(a[0])))
	}
	items, err := iterValues(a[0])
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return &Undefined{hint: "No last item, sequence was empty."}, nil
	}
	if _, markup := a[0].(Markup); markup {
		return Markup(items[len(items)-1].(string)), nil
	}
	return items[len(items)-1], nil
}

// Python's float() of a value, failing as Python does
func pyFloatConvert(v Value) (float64, error) {
	switch x := v.(type) {
	case bool, int64, *big.Int, float64:
		return toFloat(x)
	case string, Markup:
		xs, _ := asStr(x)
		if f, ok := parsePyFloat(strings.TrimFunc(xs, isPySpace)); ok {
			return f, nil
		}
		quoted, _ := pyRepr(x)
		return 0, errorf("ValueError", "could not convert string to float: %s", quoted)
	case *Undefined:
		return 0, x.fail()
	}
	return 0, typeError("float() argument must be a string or a real number, not %s", strRepr(typeName(v)))
}

var pyFloatRe = regexp.MustCompile(`^[+-]?((\d(_?\d)*)?\.?\d(_?\d)*([eE][+-]?\d(_?\d)*)?|\d(_?\d)*\.|(inf|infinity|nan))$`)

// Parses a string the way Python's float() accepts it, underscores and inf or nan included
func parsePyFloat(s string) (float64, bool) {
	lower := strings.ToLower(normalizeDigits(s))
	if !pyFloatRe.MatchString(lower) {
		return 0, false
	}
	clean := strings.ReplaceAll(lower, "_", "")
	switch strings.TrimLeft(clean, "+-") {
	case "inf", "infinity":
		if strings.HasPrefix(clean, "-") {
			return math.Inf(-1), true
		}
		return math.Inf(1), true
	case "nan":
		return math.NaN(), true
	}
	f, err := strconv.ParseFloat(clean, 64)
	if err != nil && !isRangeErr(err) {
		return 0, false
	}
	return f, true
}

func filterFloat(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_float", v, []param{req("value"), opt("default", 0.0)}, args, kw)
	if err != nil {
		return nil, err
	}
	f, err := pyFloatConvert(a[0])
	if err != nil {
		if e, ok := err.(*Error); ok && (e.Kind == "TypeError" || e.Kind == "ValueError") {
			return a[1], nil
		}
		return nil, err
	}
	return f, nil
}

// Python's int() of a value with an optional base, failing as Python does
func pyIntConvert(v Value, base Value) (Value, error) {
	switch x := v.(type) {
	case string, Markup:
		b, err := argInt(base)
		if err != nil {
			return nil, err
		}
		if b != 0 && (b < 2 || b > 36) {
			return nil, errorf("ValueError", "int() base must be >= 2 and <= 36, or 0")
		}
		xs, _ := asStr(x)
		n, ok, err := parsePyInt(strings.TrimFunc(xs, isPySpace), b)
		if err != nil {
			return nil, err
		}
		if !ok {
			quoted, _ := pyRepr(x)
			return nil, errorf("ValueError", "invalid literal for int() with base %d: %s", b, truncRunes(quoted, 200))
		}
		return n, nil
	case bool, int64, *big.Int:
		return normInt(toBig(x)), nil
	case float64:
		if math.IsInf(x, 0) {
			return nil, errorf("OverflowError", "cannot convert float infinity to integer")
		}
		if math.IsNaN(x) {
			return nil, errorf("ValueError", "cannot convert float NaN to integer")
		}
		n, _ := new(big.Float).SetFloat64(math.Trunc(x)).Int(nil)
		return normInt(n), nil
	case *Undefined:
		return nil, x.fail()
	}
	return nil, typeError("int() argument must be a string, a bytes-like object or a real number, not %s", strRepr(typeName(v)))
}

// Parses an integer literal as Python's int(s, base) does, refusing more decimal digits than CPython converts
func parsePyInt(s string, base int) (Value, bool, error) {
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	lower := strings.ToLower(s)
	if base == 0 {
		switch {
		case strings.HasPrefix(lower, "0x"):
			base, s = 16, s[2:]
		case strings.HasPrefix(lower, "0o"):
			base, s = 8, s[2:]
		case strings.HasPrefix(lower, "0b"):
			base, s = 2, s[2:]
		default:
			base = 10
			if len(s) > 1 && strings.Trim(s, "0_") != "" && s[0] == '0' {
				return nil, false, nil
			}
		}
		s = strings.TrimPrefix(s, "_")
	} else if len(lower) > 2 && lower[0] == '0' && (base == 16 && lower[1] == 'x' || base == 8 && lower[1] == 'o' || base == 2 && lower[1] == 'b') {
		s = strings.TrimPrefix(s[2:], "_")
	}
	if s == "" || strings.HasPrefix(s, "_") || strings.HasSuffix(s, "_") || strings.Contains(s, "__") {
		return nil, false, nil
	}
	digits := strings.ReplaceAll(s, "_", "")
	for _, r := range digits {
		if r > 0x7f {
			if !isDecimalRune(r) {
				return nil, false, nil
			}
		}
	}
	digits = normalizeDigits(digits)
	n, ok := new(big.Int).SetString(digits, base)
	if !ok {
		return nil, false, nil
	}
	if base&(base-1) != 0 {
		if err := checkParseDigits(len(digits)); err != nil {
			return nil, false, err
		}
	}
	if neg {
		n.Neg(n)
	}
	return normInt(n), true, nil
}

// Maps Unicode decimal digits to ASCII digits as Python's int() accepts them
func normalizeDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r > 0x7f && isDecimalRune(r) {
			start := r
			for isDecimalRune(start - 1) {
				start--
			}
			b.WriteByte(byte('0' + (r-start)%10))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func filterInt(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_int", v, []param{req("value"), opt("default", int64(0)), opt("base", int64(10))}, args, kw)
	if err != nil {
		return nil, err
	}
	var n Value
	if _, isStr := asStr(a[0]); isStr {
		n, err = pyIntConvert(a[0], a[2])
	} else {
		n, err = pyIntConvert(a[0], int64(10))
	}
	if err == nil {
		return n, nil
	}
	if e, ok := err.(*Error); !ok || (e.Kind != "TypeError" && e.Kind != "ValueError") {
		return nil, err
	}
	f, ferr := pyFloatConvert(a[0])
	if ferr != nil {
		if e, ok := ferr.(*Error); ok && (e.Kind == "TypeError" || e.Kind == "ValueError") {
			return a[1], nil
		}
		return nil, ferr
	}
	n, err = pyIntConvert(f, int64(10))
	if err != nil {
		if e, ok := err.(*Error); ok && (e.Kind == "TypeError" || e.Kind == "ValueError" || e.Kind == "OverflowError") {
			return a[1], nil
		}
		return nil, err
	}
	return n, nil
}

func filterFormat(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	if len(args) > 0 && len(kw.keys) > 0 {
		return nil, errorf("FilterArgumentError", "can't handle positional and keyword arguments at the same time")
	}
	var fargs Value = &Tuple{items: args}
	if len(kw.keys) > 0 {
		fargs = kw
	}
	s, err := softStr(v)
	if err != nil {
		return nil, err
	}
	if m, ok := s.(Markup); ok {
		return markupMod(string(m), fargs)
	}
	return printfFormat(s.(string), fargs)
}

// An attribute getter as make_attrgetter builds it, dotted paths and numeric parts
func (r *renderer) attrGetter(attribute Value, post func(Value) Value, def Value) (func(Value) (Value, error), error) {
	parts, err := attributeParts(attribute)
	if err != nil {
		return nil, err
	}
	return func(item Value) (Value, error) {
		for _, p := range parts {
			var err error
			if item, err = r.getitem(item, p); err != nil {
				return nil, err
			}
			if _, isNone := def.(NoneType); !isNone {
				if _, undef := item.(*Undefined); undef {
					item = def
				}
			}
		}
		if post != nil {
			item = post(item)
		}
		return item, nil
	}, nil
}

// The path make_attrgetter walks, dotted string parts of digits converted with int() as Python converts them
func attributeParts(attribute Value) ([]Value, error) {
	switch a := attribute.(type) {
	case NoneType:
		return nil, nil
	case string:
		var parts []Value
		for _, p := range strings.Split(a, ".") {
			if p != "" && allRunes(p, isDigitRune) {
				n, err := pyIntConvert(p, int64(10))
				if err != nil {
					return nil, err
				}
				parts = append(parts, n)
				continue
			}
			parts = append(parts, p)
		}
		return parts, nil
	}
	return []Value{attribute}, nil
}

// A getter of several comma separated attributes returning their values as a list
func (r *renderer) multiAttrGetter(attribute Value, post func(Value) Value) (func(Value) (Value, error), error) {
	var groups []Value
	if s, ok := attribute.(string); ok {
		for _, part := range strings.Split(s, ",") {
			groups = append(groups, part)
		}
	} else {
		groups = []Value{attribute}
	}
	getters := make([]func(Value) (Value, error), len(groups))
	for i, g := range groups {
		var err error
		if getters[i], err = r.attrGetter(g, post, none); err != nil {
			return nil, err
		}
	}
	return func(item Value) (Value, error) {
		out := make([]Value, len(getters))
		for i, g := range getters {
			v, err := g(item)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return &List{items: out}, nil
	}, nil
}

func filterGroupby(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("sync_do_groupby", v, []param{req("value"), req("attribute"), opt("default", none), opt("case_sensitive", false)}, args, kw)
	if err != nil {
		return nil, err
	}
	var post func(Value) Value
	if !truthy(a[3]) {
		post = ignoreCase
	}
	expr, err := r.attrGetter(a[1], post, a[2])
	if err != nil {
		return nil, err
	}
	items, err := iterValues(a[0])
	if err != nil {
		return nil, err
	}
	keys := make([]Value, len(items))
	for i, it := range items {
		if keys[i], err = expr(it); err != nil {
			return nil, err
		}
	}
	if err := pySort(items, keys, false); err != nil {
		return nil, err
	}
	var out []Value
	output, err := r.attrGetter(a[1], nil, a[2])
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(items); {
		j := i + 1
		for j < len(items) && pyEq(keys[j], keys[i]) {
			j++
		}
		grouper := keys[i]
		if !truthy(a[3]) {
			if grouper, err = output(items[i]); err != nil {
				return nil, err
			}
		}
		out = append(out, &Tuple{items: []Value{grouper, &List{items: append([]Value(nil), items[i:j]...)}}, names: []string{"grouper", "list"}})
		i = j
	}
	return &List{items: out}, nil
}

// Splits like str.splitlines keeping Markup when the input is Markup
// The error jinja2's s += newline raises for a value that is not a string
func indentAppendError(v Value) error {
	switch x := v.(type) {
	case *Undefined:
		return x.fail()
	case *List:
		return errorf("AttributeError", "'list' object has no attribute 'splitlines'")
	case *Tuple:
		return typeError(`can only concatenate tuple (not "str") to tuple`)
	}
	return typeError("unsupported operand type(s) for +=: %s and 'str'", strRepr(typeName(v)))
}

func filterIndent(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_indent", v, []param{req("s"), opt("width", int64(4)), opt("first", false), opt("blank", false)}, args, kw)
	if err != nil {
		return nil, err
	}
	var indention string
	if s, ok := asStr(a[1]); ok {
		indention = s
	} else {
		if u, isU := a[1].(*Undefined); isU {
			return nil, u.fail()
		}
		if !isIntegral(a[1]) {
			return nil, typeError("can't multiply sequence by non-int of type %s", strRepr(typeName(a[1])))
		}
		spaces, err := repeatSeq(" ", toBig(a[1]))
		if err != nil {
			return nil, err
		}
		indention = spaces.(string)
	}
	s, ok := asStr(a[0])
	if !ok {
		return nil, indentAppendError(a[0])
	}
	_, markup := a[0].(Markup)
	s += "\n"
	lines := pySplitlines(s, false)
	if err := checkBytes(len(s) + len(lines)*len(indention)); err != nil {
		return nil, err
	}
	var rv string
	if truthy(a[3]) {
		rv = strings.Join(lines, "\n"+indention)
	} else {
		rv = lines[0]
		if len(lines) > 1 {
			rest := make([]string, len(lines)-1)
			for i, l := range lines[1:] {
				if l != "" {
					l = indention + l
				}
				rest[i] = l
			}
			rv += "\n" + strings.Join(rest, "\n")
		}
	}
	if truthy(a[2]) {
		rv = indention + rv
	}
	if markup {
		return Markup(rv), nil
	}
	return rv, nil
}

func filterItems(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_items", v, []param{req("value")}, args, kw)
	if err != nil {
		return nil, err
	}
	return generator("do_items", func() (func() (Value, bool, error), error) {
		if _, undef := a[0].(*Undefined); undef {
			return emptyPull, nil
		}
		d, ok := a[0].(*Dict)
		if !ok {
			return nil, typeError("Can only get item pairs from a mapping.")
		}
		return pullOf(&DictView{kind: "items", dict: d})
	}), nil
}

func filterJoin(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("sync_do_join", v, []param{req("value"), opt("d", ""), opt("attribute", none)}, args, kw)
	if err != nil {
		return nil, err
	}
	items, err := iterValues(a[0])
	if err != nil {
		return nil, err
	}
	if _, isNone := a[2].(NoneType); !isNone {
		get, err := r.attrGetter(a[2], nil, none)
		if err != nil {
			return nil, err
		}
		for i, it := range items {
			if items[i], err = get(it); err != nil {
				return nil, err
			}
		}
	}
	sep, err := pyStr(a[1])
	if err != nil {
		return nil, err
	}
	_, safeSep := a[1].(Markup)
	escape := r.autoescape && safeSep
	for _, it := range items {
		if _, ok := it.(Markup); ok && r.autoescape {
			escape = true
		}
	}
	parts := make([]string, len(items))
	for i, it := range items {
		var s Value = it
		if escape {
			s, err = escapeValue(it)
		} else {
			s, err = pyStr(it)
		}
		if err != nil {
			return nil, err
		}
		parts[i], _ = asStr(s)
	}
	if escape && !safeSep {
		sep = htmlEscape(sep)
	}
	if err := checkJoin(parts, sep); err != nil {
		return nil, err
	}
	if !escape {
		return strings.Join(parts, sep), nil
	}
	return Markup(strings.Join(parts, sep)), nil
}

func filterList(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("sync_do_list", v, []param{req("value")}, args, kw)
	if err != nil {
		return nil, err
	}
	items, err := iterValues(a[0])
	if err != nil {
		return nil, err
	}
	return &List{items: items}, nil
}

func filterMap(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	return generator("sync_do_map", func() (func() (Value, bool, error), error) {
		if ok, err := r.truth(v); err != nil || !ok {
			return emptyPull, err
		}
		var f func(Value) (Value, error)
		if len(args) == 0 {
			if attr, ok := kw.lookup("attribute"); ok {
				def, _ := kw.lookup("default")
				if def == nil {
					def = none
				}
				for _, k := range kw.keys {
					if k != "attribute" && k != "default" {
						return nil, errorf("FilterArgumentError", "Unexpected keyword argument %s", strRepr(k.(string)))
					}
				}
				var err error
				if f, err = r.attrGetter(attr, nil, def); err != nil {
					return nil, err
				}
			}
		}
		if f == nil {
			if len(args) == 0 {
				return nil, errorf("FilterArgumentError", "map requires a filter argument")
			}
			name, err := pyStr(args[0])
			if err != nil {
				return nil, err
			}
			rest := args[1:]
			f = func(item Value) (Value, error) { return r.callFilter(name, item, rest, kw) }
		}
		src, err := pullOf(v)
		if err != nil {
			return nil, err
		}
		return func() (Value, bool, error) {
			it, ok, err := src()
			if err != nil || !ok {
				return nil, false, err
			}
			out, err := f(it)
			return out, err == nil, err
		}, nil
	}), nil
}

func minMax(isMin bool) filterFunc {
	fname := "do_max"
	if isMin {
		fname = "do_min"
	}
	return func(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
		a, err := filterArgs(fname, v, []param{req("value"), opt("case_sensitive", false), opt("attribute", none)}, args, kw)
		if err != nil {
			return nil, err
		}
		items, err := iterValues(a[0])
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return &Undefined{hint: "No aggregated item, sequence was empty."}, nil
		}
		var post func(Value) Value
		if !truthy(a[1]) {
			post = ignoreCase
		}
		key, err := r.attrGetter(a[2], post, none)
		if err != nil {
			return nil, err
		}
		best := items[0]
		bestKey, err := key(best)
		if err != nil {
			return nil, err
		}
		for _, it := range items[1:] {
			k, err := key(it)
			if err != nil {
				return nil, err
			}
			op := ">"
			if isMin {
				op = "<"
			}
			better, err := pyOrder(op, k, bestKey)
			if err != nil {
				return nil, err
			}
			if better {
				best, bestKey = it, k
			}
		}
		return best, nil
	}
}

func filterRandom(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_random", v, []param{req("seq")}, args, kw)
	if err != nil {
		return nil, err
	}
	n, err := pyLen(a[0])
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return &Undefined{hint: "No random item, sequence was empty."}, nil
	}
	v, found, err := itemOf(a[0], int64(rand.IntN(n)))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errorf("KeyError", "%d", n)
	}
	return v, nil
}

// select, reject, selectattr and rejectattr as lazy generators
func selectFilter(keep, lookupAttr bool) filterFunc {
	return func(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
		return generator("select_or_reject", func() (func() (Value, bool, error), error) {
			if ok, err := r.truth(v); err != nil || !ok {
				return emptyPull, err
			}
			trans := func(x Value) (Value, error) { return x, nil }
			off := 0
			if lookupAttr {
				if len(args) == 0 {
					return nil, errorf("FilterArgumentError", "Missing parameter for attribute name")
				}
				var err error
				if trans, err = r.attrGetter(args[0], nil, none); err != nil {
					return nil, err
				}
				off = 1
			}
			test := func(x Value) (bool, error) { return truthy(x), nil }
			if len(args) > off {
				name, err := pyStr(args[off])
				if err != nil {
					return nil, err
				}
				rest := args[off+1:]
				test = func(x Value) (bool, error) { return r.callTest(name, x, rest, kw) }
			}
			src, err := pullOf(v)
			if err != nil {
				return nil, err
			}
			return func() (Value, bool, error) {
				for {
					it, ok, err := src()
					if err != nil || !ok {
						return nil, false, err
					}
					tv, err := trans(it)
					if err != nil {
						return nil, false, err
					}
					pass, err := test(tv)
					if err != nil {
						return nil, false, err
					}
					if pass == keep {
						return it, true, nil
					}
				}
			}, nil
		}), nil
	}
}

func filterReplace(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_replace", v, []param{req("s"), req("old"), req("new"), opt("count", none)}, args, kw)
	if err != nil {
		return nil, err
	}
	count := -1
	if _, isNone := a[3].(NoneType); !isNone {
		if count, err = argSsize(a[3]); err != nil {
			return nil, err
		}
	}
	texts := make([]string, 3)
	for i := range texts {
		if texts[i], err = pyStr(a[i]); err != nil {
			return nil, err
		}
	}
	if !r.autoescape {
		return replaceChecked(texts[0], texts[1], texts[2], count)
	}
	_, oldSafe := a[1].(Markup)
	_, newSafe := a[2].(Markup)
	_, sSafe := a[0].(Markup)
	s := texts[0]
	if !sSafe && (oldSafe || newSafe) {
		s = htmlEscape(s)
	}
	old, repl := texts[1], texts[2]
	if oldSafe || newSafe && !sSafe || sSafe {
		if !oldSafe {
			old = htmlEscape(old)
		}
		if !newSafe {
			repl = htmlEscape(repl)
		}
		out, err := replaceChecked(s, old, repl, count)
		return Markup(out), err
	}
	return replaceChecked(s, old, repl, count)
}

func filterReverse(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_reverse", v, []param{req("value")}, args, kw)
	if err != nil {
		return nil, err
	}
	switch x := a[0].(type) {
	case string, Markup:
		xs, _ := asStr(x)
		rs := []rune(xs)
		for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
			rs[i], rs[j] = rs[j], rs[i]
		}
		if _, ok := x.(Markup); ok {
			return Markup(string(rs)), nil
		}
		return string(rs), nil
	case *List, *Tuple, *Dict, *Range, *DictView:
		items, _ := iterValues(x)
		kind := map[string]string{"list": "list_reverseiterator", "tuple": "reversed", "dict": "dict_reversekeyiterator", "range": "range_iterator"}[typeName(x)]
		if dv, ok := x.(*DictView); ok {
			kind = "dict_reverse" + strings.TrimSuffix(dv.kind, "s") + "iterator"
		}
		i := len(items)
		rest := func(from int) []Value {
			out := make([]Value, 0, len(items))
			for k := len(items) - 1 - from; k >= 0; k-- {
				out = append(out, items[k])
			}
			return out
		}
		return &Iterator{kind: kind, rest: rest, next: func() (Value, bool, error) {
			if i == 0 {
				return nil, false, nil
			}
			i--
			return items[i], true, nil
		}}, nil
	case *Undefined:
		return &Iterator{kind: "reversed", rest: func(int) []Value { return nil }, next: emptyPull}, nil
	}
	items, err := iterValues(a[0])
	if err != nil {
		return nil, errorf("FilterArgumentError", "argument must be iterable")
	}
	out := make([]Value, len(items))
	for i, it := range items {
		out[len(items)-1-i] = it
	}
	return &List{items: out}, nil
}

func filterRound(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_round", v, []param{req("value"), opt("precision", int64(0)), opt("method", "common")}, args, kw)
	if err != nil {
		return nil, err
	}
	method, _ := a[2].(string)
	if method != "common" && method != "ceil" && method != "floor" {
		return nil, errorf("FilterArgumentError", "method must be common, ceil or floor")
	}
	if method == "common" {
		switch a[0].(type) {
		case bool, int64, *big.Int, float64:
		default:
			return nil, typeError("type %s doesn't define __round__ method", typeName(a[0]))
		}
		prec, err := argInt(a[1])
		if err != nil {
			return nil, err
		}
		return pyRound(a[0], prec)
	}
	scale, err := binaryOp("pow", int64(10), a[1])
	if err != nil {
		return nil, err
	}
	scaled, err := binaryOp("mul", a[0], scale)
	if err != nil {
		return nil, err
	}
	whole, err := floorCeil(method, scaled)
	if err != nil {
		return nil, err
	}
	return binaryOp("div", whole, scale)
}

// Python's math.floor or math.ceil, an exact integer for any finite real number
func floorCeil(method string, v Value) (Value, error) {
	switch x := v.(type) {
	case bool, int64, *big.Int:
		return normInt(new(big.Int).Set(toBig(x))), nil
	case float64:
		if math.IsInf(x, 0) {
			return nil, errorf("OverflowError", "cannot convert float infinity to integer")
		}
		if math.IsNaN(x) {
			return nil, errorf("ValueError", "cannot convert float NaN to integer")
		}
		if method == "ceil" {
			x = math.Ceil(x)
		} else {
			x = math.Floor(x)
		}
		n, _ := new(big.Float).SetFloat64(x).Int(nil)
		return normInt(n), nil
	}
	return nil, typeError("must be real number, not %s", typeName(v))
}

// Python's round(number, ndigits), exact decimal rounding with ties to even
func pyRound(v Value, ndigits int) (Value, error) {
	switch x := v.(type) {
	case bool, int64, *big.Int:
		n := toBig(x)
		if ndigits >= 0 {
			return normInt(n), nil
		}
		if -ndigits > n.BitLen() {
			return int64(0), nil
		}
		pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-ndigits)), nil)
		q, m := new(big.Int).DivMod(n, pow, new(big.Int))
		twice := new(big.Int).Mul(m, big.NewInt(2))
		if c := twice.Cmp(pow); c > 0 || c == 0 && q.Bit(0) == 1 {
			q.Add(q, big.NewInt(1))
		}
		return normInt(q.Mul(q, pow)), nil
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) || x == 0 {
			return x, nil
		}
		if ndigits > 323 {
			return x, nil
		}
		if ndigits < -308 {
			return math.Copysign(0, x), nil
		}
		if ndigits >= 0 {
			s := strconv.FormatFloat(x, 'f', ndigits, 64)
			f, _ := strconv.ParseFloat(s, 64)
			return f, nil
		}
		r := new(big.Rat)
		r.SetFloat64(x)
		pow := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-ndigits)), nil))
		q := new(big.Rat).Quo(r, pow)
		n := roundHalfEven(q)
		res, _ := new(big.Rat).Mul(new(big.Rat).SetInt(n), pow).Float64()
		if math.IsInf(res, 0) {
			return nil, errorf("OverflowError", "rounded value too large to represent")
		}
		if res == 0 {
			return math.Copysign(0, x), nil
		}
		return res, nil
	}
	return nil, typeError("type %s doesn't define __round__ method", typeName(v))
}

func roundHalfEven(q *big.Rat) *big.Int {
	num, den := q.Num(), q.Denom()
	quo, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	twice := new(big.Int).Mul(new(big.Int).Abs(rem), big.NewInt(2))
	c := twice.Cmp(den)
	if c > 0 || c == 0 && quo.Bit(0) == 1 {
		if num.Sign() < 0 {
			quo.Sub(quo, big.NewInt(1))
		} else {
			quo.Add(quo, big.NewInt(1))
		}
	}
	return quo
}

func filterSafe(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_mark_safe", v, []param{req("value")}, args, kw)
	if err != nil {
		return nil, err
	}
	s, err := pyStr(a[0])
	return Markup(s), err
}

func filterSlice(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("sync_do_slice", v, []param{req("value"), req("slices"), opt("fill_with", none)}, args, kw)
	if err != nil {
		return nil, err
	}
	return generator("sync_do_slice", func() (func() (Value, bool, error), error) {
		seq, err := iterValues(a[0])
		if err != nil {
			return nil, err
		}
		slices, err := argInt(a[1])
		if err != nil {
			return nil, err
		}
		if slices == 0 {
			return nil, errorf("ZeroDivisionError", "division by zero")
		}
		length := len(seq)
		per := pyFloorDiv(length, slices)
		extra := pyMod(length, slices)
		offset := 0
		n := 0
		return func() (Value, bool, error) {
			if n >= slices {
				return nil, false, nil
			}
			start := offset + n*per
			if n < extra {
				offset++
			}
			end := offset + (n+1)*per
			if err := r.charge(scopeBytes); err != nil {
				return nil, false, err
			}
			tmp := &List{items: pick(seq, rangeInts(start, end, length))}
			if _, isNone := a[2].(NoneType); !isNone && n >= extra {
				tmp.items = append(tmp.items, a[2])
			}
			n++
			return tmp, true, nil
		}, nil
	}), nil
}

func pyFloorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func pyMod(a, b int) int { return a - pyFloorDiv(a, b)*b }

// Positions start up to end clamped to a sequence length, as a step one slice selects them
func rangeInts(start, end, n int) []int {
	s := &sliceValue{start: int64(start), stop: int64(end), step: none}
	pos, _ := s.positions(n)
	return pos
}

func filterSort(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_sort", v, []param{req("value"), opt("reverse", false), opt("case_sensitive", false), opt("attribute", none)}, args, kw)
	if err != nil {
		return nil, err
	}
	items, err := iterValues(a[0])
	if err != nil {
		return nil, err
	}
	var post func(Value) Value
	if !truthy(a[2]) {
		post = ignoreCase
	}
	key, err := r.multiAttrGetter(a[3], post)
	if err != nil {
		return nil, err
	}
	keys := make([]Value, len(items))
	for i, it := range items {
		if keys[i], err = key(it); err != nil {
			return nil, err
		}
	}
	if err := pySort(items, keys, truthy(a[1])); err != nil {
		return nil, err
	}
	return &List{items: items}, nil
}

func filterString(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	if len(args) > 0 || len(kw.keys) > 0 {
		return nil, typeError("soft_str() takes 1 positional argument but %d were given", len(args)+1)
	}
	return softStr(v)
}

func filterStriptags(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_striptags", v, []param{req("value")}, args, kw)
	if err != nil {
		return nil, err
	}
	s, err := pyStr(a[0])
	return stripTags(s), err
}

// Markup.striptags, removing comments and tags, collapsing whitespace and unescaping entities
func stripTags(value string) string { return htmlUnescape(stripTagsText(value)) }

// Markup.striptags before unescaping, comments and tags removed and whitespace collapsed
func stripTagsText(value string) string {
	for {
		start := strings.Index(value, "<!--")
		if start < 0 {
			break
		}
		end := strings.Index(value[start:], "-->")
		if end < 0 {
			break
		}
		value = value[:start] + value[start+end+3:]
	}
	for {
		start := strings.Index(value, "<")
		if start < 0 {
			break
		}
		end := strings.Index(value[start:], ">")
		if end < 0 {
			break
		}
		value = value[:start] + value[start+end+1:]
	}
	parts, _ := pySplit(value, nil, -1)
	return strings.Join(parts, " ")
}

func htmlUnescape(s string) string { return html.UnescapeString(s) }

func filterSum(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("sync_do_sum", v, []param{req("iterable"), opt("attribute", none), opt("start", int64(0))}, args, kw)
	if err != nil {
		return nil, err
	}
	var get func(Value) (Value, error)
	if _, isNone := a[1].(NoneType); !isNone {
		if get, err = r.attrGetter(a[1], nil, none); err != nil {
			return nil, err
		}
	}
	pull, err := pullOf(a[0])
	if err != nil {
		return nil, err
	}
	if _, isStr := asStr(a[2]); isStr {
		return nil, typeError("sum() can't sum strings [use ''.join(seq) instead]")
	}
	var items []Value
	var stop error
	acc := a[2]
	for stop == nil {
		it, ok, err := pull()
		if err != nil || !ok {
			stop = err
			break
		}
		if get != nil {
			if it, stop = get(it); stop != nil {
				break
			}
		}
		items = append(items, it)
		if acc, err = binaryOp("add", acc, it); err != nil {
			break
		}
	}
	out, err := pySum(r, items, a[2])
	if err != nil {
		return nil, err
	}
	if stop != nil {
		return nil, stop
	}
	return out, nil
}

// Python's sum, exact over integers and compensated over floats as CPython adds them
func pySum(r *renderer, items []Value, start Value) (Value, error) {
	result := start
	i := 0
	if _, isBool := start.(bool); !isBool && isIntegral(start) {
		acc := new(big.Int).Set(toBig(start))
		for ; i < len(items) && isIntegral(items[i]); i++ {
			acc.Add(acc, toBig(items[i]))
		}
		result = normInt(acc)
		if i == len(items) {
			return result, nil
		}
		var err error
		if result, err = binaryOp("add", result, items[i]); err != nil {
			return nil, err
		}
		i++
	}
	if f, ok := result.(float64); ok {
		hi, lo := f, 0.0
		add := func(x float64) {
			t := hi + x
			if math.Abs(hi) >= math.Abs(x) {
				lo += (hi - t) + x
			} else {
				lo += (x - t) + hi
			}
			hi = t
		}
		total := func() float64 {
			if lo != 0 && !math.IsInf(lo, 0) && !math.IsNaN(lo) {
				return hi + lo
			}
			return hi
		}
	floats:
		for ; i < len(items); i++ {
			switch x := items[i].(type) {
			case float64:
				add(x)
			case int64, *big.Int:
				fx, err := toFloat(x)
				if err != nil {
					return nil, err
				}
				add(fx)
			default:
				break floats
			}
		}
		result = total()
		if i == len(items) {
			return result, nil
		}
	}
	for ; i < len(items); i++ {
		var err error
		if result, err = binaryOp("add", result, items[i]); err != nil {
			return nil, err
		}
		if err := r.chargeValue(result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func filterTitle(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_title", v, []param{req("s")}, args, kw)
	if err != nil {
		return nil, err
	}
	s, err := pyStr(a[0])
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	last := 0
	emit := func(item string) {
		if item == "" {
			return
		}
		rs := []rune(item)
		b.WriteString(pyUpper(string(rs[0])) + pyLower(string(rs[1:])))
	}
	for _, loc := range pySpaceAwareSplit(s) {
		emit(s[last:loc[0]])
		emit(s[loc[0]:loc[1]])
		last = loc[1]
	}
	emit(s[last:])
	return b.String(), nil
}

// Separator runs of jinja2's title filter, hyphens, brackets and Python whitespace
func pySpaceAwareSplit(s string) [][2]int {
	var out [][2]int
	in := false
	start := 0
	for i, r := range s {
		sep := r == '-' || r == '(' || r == '{' || r == '[' || r == '<' || isPySpace(r)
		if sep && !in {
			in, start = true, i
		} else if !sep && in {
			in = false
			out = append(out, [2]int{start, i})
		}
	}
	if in {
		out = append(out, [2]int{start, len(s)})
	}
	return out
}

func filterTojson(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("tojson", v, []param{req("x"), opt("ensure_ascii", false), opt("indent", none), opt("separators", none), opt("sort_keys", false)}, args, kw)
	if err != nil {
		return nil, err
	}
	opts := jsonOptions{ensureASCII: truthy(a[1]), sortKeys: truthy(a[4]), itemSep: ", ", keySep: ": "}
	switch ind := a[2].(type) {
	case NoneType:
	case string:
		opts.indent = &ind
	case bool, int64, *big.Int:
		spaces, err := repeatSeq(" ", toBig(ind))
		if err != nil {
			return nil, err
		}
		s := spaces.(string)
		opts.indent = &s
	default:
		return nil, typeError("can't multiply sequence by non-int of type %s", strRepr(typeName(ind)))
	}
	if opts.indent != nil {
		opts.itemSep = ","
	}
	if _, isNone := a[3].(NoneType); !isNone {
		seps, err := unpack(a[3], 2)
		if err != nil {
			return nil, err
		}
		item, ok1 := asStr(seps[0])
		key, ok2 := asStr(seps[1])
		if !ok1 || !ok2 {
			return nil, typeError("separators must be strings")
		}
		opts.itemSep, opts.keySep = item, key
	}
	return jsonDumps(a[0], opts)
}

func filterTrim(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_trim", v, []param{req("value"), opt("chars", none)}, args, kw)
	if err != nil {
		return nil, err
	}
	chars, err := argStrOrNone(a[1], "strip")
	if err != nil {
		return nil, err
	}
	s, err := softStr(a[0])
	if err != nil {
		return nil, err
	}
	text, _ := asStr(s)
	out := pyStrip(text, chars, true, true)
	if _, ok := s.(Markup); ok {
		return Markup(out), nil
	}
	return out, nil
}

func filterTruncate(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_truncate", v, []param{req("s"), opt("length", int64(255)), opt("killwords", false), opt("end", "..."), opt("leeway", none)}, args, kw)
	if err != nil {
		return nil, err
	}
	leeway := 5
	if _, isNone := a[4].(NoneType); !isNone {
		if leeway, err = argInt(a[4]); err != nil {
			return nil, err
		}
	}
	length, err := argInt(a[1])
	if err != nil {
		return nil, err
	}
	end, err := pyStr(a[3])
	if err != nil {
		return nil, err
	}
	if length < runeLen(end) {
		got, err := pyStr(a[1])
		if err != nil {
			return nil, err
		}
		return nil, errorf("AssertionError", "expected length >= %d, got %s", runeLen(end), got)
	}
	if leeway < 0 {
		got, err := pyStr(a[4])
		if err != nil {
			return nil, err
		}
		return nil, errorf("AssertionError", "expected leeway >= 0, got %s", got)
	}
	n, err := pyLen(a[0])
	if err != nil {
		return nil, err
	}
	if n <= length+leeway {
		return a[0], nil
	}
	s, ok := asStr(a[0])
	if !ok {
		return nil, typeError("%s object is not subscriptable", strRepr(typeName(a[0])))
	}
	cut := string([]rune(s)[:length-runeLen(end)])
	if truthy(a[2]) {
		return cut + end, nil
	}
	if k := strings.LastIndex(cut, " "); k >= 0 {
		cut = cut[:k]
	}
	return cut + end, nil
}

func filterUnique(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("sync_do_unique", v, []param{req("value"), opt("case_sensitive", false), opt("attribute", none)}, args, kw)
	if err != nil {
		return nil, err
	}
	return generator("sync_do_unique", func() (func() (Value, bool, error), error) {
		var post func(Value) Value
		if !truthy(a[1]) {
			post = ignoreCase
		}
		get, err := r.attrGetter(a[2], post, none)
		if err != nil {
			return nil, err
		}
		src, err := pullOf(a[0])
		if err != nil {
			return nil, err
		}
		seen := newDict()
		return func() (Value, bool, error) {
			for {
				it, ok, err := src()
				if err != nil || !ok {
					return nil, false, err
				}
				key, err := get(it)
				if err != nil {
					return nil, false, err
				}
				if _, err := hashKey(key); err != nil {
					return nil, false, unhashable("set element", key, err)
				}
				_, found, _ := seen.get(key)
				if !found {
					_ = seen.set(key, true)
					return it, true, nil
				}
			}
		}, nil
	}), nil
}

// Percent encodes bytes outside the unreserved set and the extra safe characters
func urlQuote(v Value, forQS bool) (string, error) {
	s, err := pyStr(v)
	if err != nil {
		return "", err
	}
	safe := "/"
	if forQS {
		safe = ""
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_.-~", c) >= 0 || strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	out := b.String()
	if forQS {
		out = strings.ReplaceAll(out, "%20", "+")
	}
	return out, nil
}

func filterUrlencode(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_urlencode", v, []param{req("value")}, args, kw)
	if err != nil {
		return nil, err
	}
	var pairs []Value
	switch x := a[0].(type) {
	case string, Markup, NoneType, bool, int64, *big.Int, float64, *Undefined, *Namespace, *Macro, *Builtin:
		return urlQuote(x, false)
	case *Dict:
		pairs, _ = iterValues(&DictView{kind: "items", dict: x})
	default:
		if pairs, err = iterValues(x); err != nil {
			return nil, err
		}
	}
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		kv, err := unpack(p, 2)
		if err != nil {
			return nil, err
		}
		k, err := urlQuote(kv[0], true)
		if err != nil {
			return nil, err
		}
		v, err := urlQuote(kv[1], true)
		if err != nil {
			return nil, err
		}
		parts[i] = k + "=" + v
	}
	if err := checkJoin(parts, "&"); err != nil {
		return nil, err
	}
	return strings.Join(parts, "&"), nil
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}_]+`)

func filterWordcount(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_wordcount", v, []param{req("s")}, args, kw)
	if err != nil {
		return nil, err
	}
	s, err := pyStr(a[0])
	return int64(len(wordRe.FindAllStringIndex(s, -1))), err
}

var attrKeyRe = regexp.MustCompile(`[\s/>=]`)

// The error value.items() raises for a value that is not a dict
func itemsError(v Value) error { return missingMethod(v, "items") }

// The error looking up a missing method raises, Undefined failing, Namespace naming only the attribute
func missingMethod(v Value, name string) error {
	switch x := v.(type) {
	case *Undefined:
		return x.fail()
	case *Namespace:
		return errorf("AttributeError", "%s", name)
	}
	return errorf("AttributeError", "%s object has no attribute %s", strRepr(typeName(v)), strRepr(name))
}

func filterXmlattr(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_xmlattr", v, []param{req("d"), opt("autospace", true)}, args, kw)
	if err != nil {
		return nil, err
	}
	d, ok := a[0].(*Dict)
	if !ok {
		return nil, itemsError(a[0])
	}
	var items []string
	for i, k := range d.keys {
		val := d.vals[i]
		if _, isNone := val.(NoneType); isNone {
			continue
		}
		if _, undef := val.(*Undefined); undef {
			continue
		}
		key, err := pyStr(k)
		if err != nil {
			return nil, err
		}
		if attrKeyRe.MatchString(key) {
			quoted, _ := pyRepr(k)
			return nil, errorf("ValueError", "Invalid character in attribute name: %s", quoted)
		}
		ek, err := escapeValue(k)
		if err != nil {
			return nil, err
		}
		ev, err := escapeValue(val)
		if err != nil {
			return nil, err
		}
		items = append(items, fmt.Sprintf(`%s="%s"`, ek, ev))
	}
	if err := checkJoin(items, " "); err != nil {
		return nil, err
	}
	rv := strings.Join(items, " ")
	if truthy(a[1]) && rv != "" {
		rv = " " + rv
	}
	if r.autoescape {
		return Markup(rv), nil
	}
	return rv, nil
}

// The first n runes of a string, as a %.nR conversion truncates
func truncRunes(s string, n int) string {
	if runeLen(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
