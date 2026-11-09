package jinja

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

var objectDunders = splitSet("__class__ __delattr__ __dir__ __doc__ __eq__ __format__ __ge__ __getattribute__ __getstate__ __gt__ __hash__ __init__ __init_subclass__ __le__ __lt__ __ne__ __new__ __reduce__ __reduce_ex__ __repr__ __setattr__ __sizeof__ __str__ __subclasshook__")

var typeDunders = map[string]map[string]bool{
	"TemplateReference":          splitSet("__dict__ __firstlineno__ __getitem__ __module__ __static_attributes__ __weakref__ _TemplateReference__context"),
	"BlockReference":             splitSet("__call__ __dict__ __firstlineno__ __module__ __static_attributes__ __weakref__ _async_call _context _depth _stack"),
	"str":                        splitSet("__add__ __contains__ __getitem__ __getnewargs__ __iter__ __len__ __mod__ __mul__ __rmod__ __rmul__"),
	"list":                       splitSet("__add__ __class_getitem__ __contains__ __delitem__ __getitem__ __iadd__ __imul__ __iter__ __len__ __mul__ __reversed__ __rmul__ __setitem__"),
	"tuple":                      splitSet("__add__ __class_getitem__ __contains__ __getitem__ __getnewargs__ __iter__ __len__ __mul__ __rmul__"),
	"dict":                       splitSet("__class_getitem__ __contains__ __delitem__ __getitem__ __ior__ __iter__ __len__ __or__ __reversed__ __ror__ __setitem__"),
	"int":                        splitSet("__abs__ __add__ __and__ __bool__ __ceil__ __divmod__ __float__ __floor__ __floordiv__ __getnewargs__ __index__ __int__ __invert__ __lshift__ __mod__ __mul__ __neg__ __or__ __pos__ __pow__ __radd__ __rand__ __rdivmod__ __rfloordiv__ __rlshift__ __rmod__ __rmul__ __ror__ __round__ __rpow__ __rrshift__ __rshift__ __rsub__ __rtruediv__ __rxor__ __sub__ __truediv__ __trunc__ __xor__"),
	"float":                      splitSet("__abs__ __add__ __bool__ __ceil__ __divmod__ __float__ __floor__ __floordiv__ __getformat__ __getnewargs__ __int__ __mod__ __mul__ __neg__ __pos__ __pow__ __radd__ __rdivmod__ __rfloordiv__ __rmod__ __rmul__ __round__ __rpow__ __rsub__ __rtruediv__ __sub__ __truediv__ __trunc__"),
	"complex":                    splitSet("__abs__ __add__ __bool__ __complex__ __getnewargs__ __mul__ __neg__ __pos__ __pow__ __radd__ __rmul__ __rpow__ __rsub__ __rtruediv__ __sub__ __truediv__"),
	"NoneType":                   splitSet("__bool__"),
	"range":                      splitSet("__bool__ __contains__ __getitem__ __iter__ __len__ __reversed__"),
	"Markup":                     splitSet("__add__ __contains__ __firstlineno__ __getitem__ __getnewargs__ __html__ __html_format__ __iter__ __len__ __mod__ __module__ __mul__ __radd__ __rmod__ __rmul__ __slots__ __static_attributes__"),
	"Namespace":                  splitSet("__dict__ __firstlineno__ __module__ __setitem__ __static_attributes__ __weakref__"),
	"Undefined":                  splitSet("__add__ __aiter__ __bool__ __call__ __complex__ __div__ __firstlineno__ __float__ __floordiv__ __getattr__ __getitem__ __int__ __iter__ __len__ __mod__ __module__ __mul__ __neg__ __pos__ __pow__ __radd__ __rdiv__ __rfloordiv__ __rmod__ __rmul__ __rpow__ __rsub__ __rtruediv__ __slots__ __static_attributes__ __sub__ __truediv__ _fail_with_undefined_error _undefined_exception _undefined_hint _undefined_message _undefined_name _undefined_obj"),
	"Cycler":                     splitSet("__dict__ __firstlineno__ __module__ __next__ __static_attributes__ __weakref__"),
	"Joiner":                     splitSet("__call__ __dict__ __firstlineno__ __module__ __static_attributes__ __weakref__"),
	"generator":                  splitSet("__class_getitem__ __del__ __iter__ __name__ __next__ __qualname__"),
	"list_reverseiterator":       splitSet("__iter__ __length_hint__ __next__ __setstate__"),
	"range_iterator":             splitSet("__iter__ __length_hint__ __next__ __setstate__"),
	"reversed":                   splitSet("__iter__ __length_hint__ __next__ __setstate__"),
	"dict_reversekeyiterator":    splitSet("__iter__ __length_hint__ __next__"),
	"dict_reversevalueiterator":  splitSet("__iter__ __length_hint__ __next__"),
	"dict_reverseitemiterator":   splitSet("__iter__ __length_hint__ __next__"),
	"dict_keys":                  splitSet("__and__ __contains__ __iter__ __len__ __or__ __rand__ __reversed__ __ror__ __rsub__ __rxor__ __sub__ __xor__"),
	"dict_values":                splitSet("__iter__ __len__ __reversed__"),
	"dict_items":                 splitSet("__and__ __contains__ __iter__ __len__ __or__ __rand__ __reversed__ __ror__ __rsub__ __rxor__ __sub__ __xor__"),
	"function":                   splitSet("__annotate__ __annotations__ __builtins__ __call__ __closure__ __code__ __defaults__ __dict__ __get__ __globals__ __kwdefaults__ __module__ __name__ __qualname__ __type_params__"),
	"builtin_function_or_method": splitSet("__call__ __module__ __name__ __qualname__ __self__ __text_signature__"),
	"LoopContext":                splitSet("__annotate_func__ __call__ __dict__ __firstlineno__ __iter__ __len__ __module__ __next__ __static_attributes__ __weakref__ _after _before _current _iterable _iterator _last_changed_value _length _peek_next _recurse _to_iterator _undefined"),
	"Macro":                      splitSet("__call__ __dict__ __firstlineno__ __module__ __static_attributes__ __weakref__ _argument_count _async_invoke _default_autoescape _environment _func _invoke"),
	"type":                       splitSet("__annotate__ __annotations__ __base__ __bases__ __basicsize__ __call__ __dict__ __dictoffset__ __flags__ __instancecheck__ __itemsize__ __module__ __mro__ __name__ __or__ __prepare__ __qualname__ __ror__ __subclasscheck__ __subclasses__ __text_signature__ __type_params__ __weakrefoffset__"),
}

func splitSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.Fields(s) {
		out[f] = true
	}
	return out
}

// Whether a private attribute name exists on the value's Python type
func hasPrivateAttr(obj Value, name string) bool {
	if objectDunders[name] {
		return true
	}
	t := typeName(obj)
	switch x := obj.(type) {
	case bool, int64, *big.Int:
		t = "int"
	case *Builtin:
		if x.class != "" {
			return typeDunders["type"][name] || typeDunders[x.instance][name]
		}
	}
	return typeDunders[t][name]
}

// A bound method that binds its arguments to a Python signature before running
func method(self Value, name string, params []param, fn func(a []Value) (Value, error)) *Builtin {
	tname := typeName(self)
	return &Builtin{name: name, self: self, fn: func(r *renderer, args []Value, kw *Dict) (Value, error) {
		a, err := bindArgs(tname+"."+name, params, args, kw)
		if err != nil {
			return nil, err
		}
		return fn(a)
	}}
}

func posParams(params ...param) []param {
	for i := range params {
		params[i].posOnly = true
	}
	return params
}

func req(name string) param { return param{name: name} }

// A Python attribute of a value and whether the type has it
func (r *renderer) attribute(obj Value, name string) (Value, bool, error) {
	if strings.HasPrefix(name, "_") {
		if ns, ok := obj.(*Namespace); ok {
			if v, found := ns.attrs.lookup(name); found {
				return v, true, nil
			}
		}
		return nil, hasPrivateAttr(obj, name), nil
	}
	switch x := obj.(type) {
	case string:
		return r.strAttr(x, false, name)
	case Markup:
		return r.strAttr(string(x), true, name)
	case *List:
		return listAttr(x, name)
	case *Tuple:
		return tupleAttr(x, name)
	case *Dict:
		return dictAttr(x, name)
	case bool, int64, *big.Int:
		return intAttr(x, name)
	case float64:
		return floatAttr(x, name)
	case complex128:
		return complexAttr(x, name)
	case *Range:
		return rangeAttr(x, name)
	case *Namespace:
		v, ok := x.attrs.lookup(name)
		return v, ok, nil
	case *BlockRef:
		v, ok := blockRefAttr(x, name)
		return v, ok, nil
	case *LoopContext:
		return loopAttr(x, name)
	case *Macro:
		return macroAttr(x, name)
	case *Cycler:
		return cyclerAttr(x, name)
	case *Joiner:
		switch name {
		case "sep":
			return x.sep, true, nil
		case "used":
			return x.used, true, nil
		}
	case *DictView:
		if name == "isdisjoint" && x.kind != "values" {
			return method(x, name, posParams(req("other")), func(a []Value) (Value, error) {
				other, err := iterValues(a[0])
				if err != nil {
					return nil, err
				}
				for _, it := range other {
					in, err := pyContains(x, it)
					if err != nil {
						return nil, err
					}
					if in {
						return false, nil
					}
				}
				return true, nil
			}), true, nil
		}
	}
	return nil, false, nil
}

func argStr(v Value, what string) (string, error) {
	if s, ok := asStr(v); ok {
		return s, nil
	}
	return "", typeError("%s must be str, not %s", what, typeName(v))
}

// An integer argument, failing as Python does when it cannot be interpreted as one
func argInt(v Value) (int, error) {
	if !isIntegral(v) {
		return 0, typeError("%s object cannot be interpreted as an integer", strRepr(typeName(v)))
	}
	b := toBig(v)
	if !b.IsInt64() || b.Int64() > math.MaxInt32 || b.Int64() < math.MinInt32 {
		if b.Sign() > 0 {
			return math.MaxInt32, nil
		}
		return math.MinInt32, nil
	}
	return int(b.Int64()), nil
}

// An integer argument CPython converts to a C ssize_t, refusing values past 64 bits
func argSsize(v Value) (int, error) {
	if !isIntegral(v) {
		return 0, typeError("%s object cannot be interpreted as an integer", strRepr(typeName(v)))
	}
	b := toBig(v)
	if !b.IsInt64() {
		return 0, errorf("OverflowError", "Python int too large to convert to C ssize_t")
	}
	return int(b.Int64()), nil
}

// An integer argument CPython converts to a C int, refusing values past 32 bits
func argCInt(v Value) (int, error) {
	if !isIntegral(v) {
		return 0, typeError("%s object cannot be interpreted as an integer", strRepr(typeName(v)))
	}
	b := toBig(v)
	if !b.IsInt64() || b.Int64() > math.MaxInt32 || b.Int64() < math.MinInt32 {
		return 0, errorf("OverflowError", "Python int too large to convert to C int")
	}
	return int(b.Int64()), nil
}

// A slice index argument that may be None
func argIndex(v Value, def int) (int, error) {
	if _, ok := v.(NoneType); ok {
		return def, nil
	}
	if !isIntegral(v) {
		return 0, typeError("slice indices must be integers or None or have an __index__ method")
	}
	return argInt(v)
}

func argStrOrNone(v Value, what string) (*string, error) {
	if _, ok := v.(NoneType); ok {
		return nil, nil
	}
	s, ok := asStr(v)
	if !ok {
		return nil, typeError("%s arg must be None or str", what)
	}
	return &s, nil
}

// A string or tuple of strings argument of startswith and endswith
func argAffixes(v Value, fname string) ([]string, error) {
	if s, ok := asStr(v); ok {
		return []string{s}, nil
	}
	t, ok := v.(*Tuple)
	if !ok {
		return nil, typeError("%s first arg must be str or a tuple of str, not %s", fname, typeName(v))
	}
	out := make([]string, len(t.items))
	for i, it := range t.items {
		s, ok := asStr(it)
		if !ok {
			return nil, typeError("tuple for %s must only contain str, not %s", fname, typeName(it))
		}
		out[i] = s
	}
	return out, nil
}

// A single character fill argument of center, ljust and rjust
func argFill(v Value) (rune, error) {
	s, ok := asStr(v)
	if !ok || runeLen(s) != 1 {
		return 0, typeError("The fill character must be exactly one character long")
	}
	return []rune(s)[0], nil
}

// Methods of str, with Markup's escaping of arguments and wrapping of results when markup is set
func (r *renderer) strAttr(s string, markup bool, name string) (Value, bool, error) {
	self := Value(s)
	if markup {
		self = Markup(s)
	}
	wrap := func(out string) Value {
		if markup {
			return Markup(out)
		}
		return out
	}
	escArg := func(v Value) Value {
		if markup {
			if _, ok := v.(Markup); !ok {
				if s, isStr := v.(string); isStr {
					return Markup(htmlEscape(s))
				}
			}
		}
		return v
	}
	noArgs := func(f func() Value) (Value, bool, error) {
		return method(self, name, nil, func([]Value) (Value, error) { return f(), nil }), true, nil
	}
	pred := func(f func(string) bool) (Value, bool, error) {
		return noArgs(func() Value { return f(s) })
	}
	search := func(f func(sub string, start, end int) (Value, error)) (Value, bool, error) {
		return method(self, name, posParams(req("sub"), opt("start", none), opt("end", none)), func(a []Value) (Value, error) {
			sub, ok := asStr(a[0])
			if !ok {
				return nil, typeError("%s() argument 1 must be str, not %s", name, typeName(a[0]))
			}
			start, err := argIndex(a[1], 0)
			if err != nil {
				return nil, err
			}
			end, err := argIndex(a[2], math.MaxInt32)
			if err != nil {
				return nil, err
			}
			return f(sub, start, end)
		}), true, nil
	}
	strip := func(left, right bool) (Value, bool, error) {
		return method(self, name, posParams(opt("chars", none)), func(a []Value) (Value, error) {
			chars, err := argStrOrNone(escArg(a[0]), "strip")
			if err != nil {
				return nil, err
			}
			return wrap(pyStrip(s, chars, left, right)), nil
		}), true, nil
	}
	justify := func(f func(width int, fill rune) string) (Value, bool, error) {
		return method(self, name, posParams(req("width"), opt("fillchar", " ")), func(a []Value) (Value, error) {
			w, err := argSsize(a[0])
			if err != nil {
				return nil, err
			}
			fill, err := argFill(escArg(a[1]))
			if err != nil {
				return nil, err
			}
			err = checkFill(w, fill)
			if err != nil {
				return nil, err
			}
			return wrap(f(w, fill)), nil
		}), true, nil
	}
	split := func(right bool) (Value, bool, error) {
		return method(self, name, []param{opt("sep", none), opt("maxsplit", int64(-1))}, func(a []Value) (Value, error) {
			sep, err := argStrOrNone(escArg(a[0]), "must be str or None")
			if err != nil {
				return nil, typeError("must be str or None, not %s", typeName(a[0]))
			}
			maxsplit, err := argSsize(a[1])
			if err != nil {
				return nil, err
			}
			var parts []string
			if right {
				parts, err = pyRSplit(s, sep, maxsplit)
			} else {
				parts, err = pySplit(s, sep, maxsplit)
			}
			if err != nil {
				return nil, err
			}
			out := make([]Value, len(parts))
			for i, p := range parts {
				out[i] = wrap(p)
			}
			return &List{items: out}, nil
		}), true, nil
	}
	affix := func(suffix bool) (Value, bool, error) {
		return method(self, name, posParams(req("prefix"), opt("start", none), opt("end", none)), func(a []Value) (Value, error) {
			affixes, err := argAffixes(a[0], name)
			if err != nil {
				return nil, err
			}
			rs := []rune(s)
			start, err := argIndex(a[1], 0)
			if err != nil {
				return nil, err
			}
			end, err := argIndex(a[2], math.MaxInt32)
			if err != nil {
				return nil, err
			}
			adjustIndices(&start, &end, len(rs))
			for _, af := range affixes {
				if end-runeLen(af) < start {
					continue
				}
				hay := string(rs[start:end])
				if suffix && strings.HasSuffix(hay, af) || !suffix && strings.HasPrefix(hay, af) {
					return true, nil
				}
			}
			return false, nil
		}), true, nil
	}
	partition := func(right bool) (Value, bool, error) {
		return method(self, name, posParams(req("sep")), func(a []Value) (Value, error) {
			sep, err := argStr(escArg(a[0]), "sep")
			if err != nil {
				return nil, err
			}
			if sep == "" {
				return nil, errorf("ValueError", "empty separator")
			}
			var k int
			if right {
				k = strings.LastIndex(s, sep)
			} else {
				k = strings.Index(s, sep)
			}
			if k < 0 {
				if right {
					return &Tuple{items: []Value{wrap(""), wrap(""), wrap(s)}}, nil
				}
				return &Tuple{items: []Value{wrap(s), wrap(""), wrap("")}}, nil
			}
			return &Tuple{items: []Value{wrap(s[:k]), wrap(sep), wrap(s[k+len(sep):])}}, nil
		}), true, nil
	}
	switch name {
	case "capitalize":
		return noArgs(func() Value { return wrap(pyCapitalize(s)) })
	case "casefold":
		return noArgs(func() Value { return wrap(pyCasefold(s)) })
	case "lower":
		return noArgs(func() Value { return wrap(pyLower(s)) })
	case "upper":
		return noArgs(func() Value { return wrap(pyUpper(s)) })
	case "title":
		return noArgs(func() Value { return wrap(pyTitle(s)) })
	case "swapcase":
		return noArgs(func() Value { return wrap(pySwapcase(s)) })
	case "isalnum":
		return pred(func(s string) bool {
			return allRunes(s, func(r rune) bool { return isAlphaRune(r) || isNumericRune(r) })
		})
	case "isalpha":
		return pred(func(s string) bool { return allRunes(s, isAlphaRune) })
	case "isascii":
		return pred(func(s string) bool {
			for i := 0; i < len(s); i++ {
				if s[i] >= 0x80 {
					return false
				}
			}
			return true
		})
	case "isdecimal":
		return pred(func(s string) bool { return allRunes(s, isDecimalRune) })
	case "isdigit":
		return pred(func(s string) bool { return allRunes(s, isDigitRune) })
	case "isnumeric":
		return pred(func(s string) bool { return allRunes(s, isNumericRune) })
	case "isspace":
		return pred(func(s string) bool { return allRunes(s, isPySpace) })
	case "isprintable":
		return pred(func(s string) bool {
			for _, r := range s {
				if !isPrintable(r) {
					return false
				}
			}
			return true
		})
	case "isidentifier":
		return pred(func(s string) bool { return isIdentifier(s) && matchName(s, 0) == len(s) })
	case "islower":
		return pred(func(s string) bool { return pyIsCase(s, false) })
	case "isupper":
		return pred(func(s string) bool { return pyIsCase(s, true) })
	case "istitle":
		return pred(pyIsTitle)
	case "strip":
		return strip(true, true)
	case "lstrip":
		return strip(true, false)
	case "rstrip":
		return strip(false, true)
	case "split":
		return split(false)
	case "rsplit":
		return split(true)
	case "splitlines":
		return method(self, name, []param{opt("keepends", false)}, func(a []Value) (Value, error) {
			lines := pySplitlines(s, truthy(a[0]))
			out := make([]Value, len(lines))
			for i, l := range lines {
				out[i] = wrap(l)
			}
			return &List{items: out}, nil
		}), true, nil
	case "startswith":
		return affix(false)
	case "endswith":
		return affix(true)
	case "find":
		return search(func(sub string, start, end int) (Value, error) {
			return int64(pyFind(s, sub, start, end, false)), nil
		})
	case "rfind":
		return search(func(sub string, start, end int) (Value, error) {
			return int64(pyFind(s, sub, start, end, true)), nil
		})
	case "index", "rindex":
		return search(func(sub string, start, end int) (Value, error) {
			i := pyFind(s, sub, start, end, name == "rindex")
			if i < 0 {
				return nil, errorf("ValueError", "substring not found")
			}
			return int64(i), nil
		})
	case "count":
		return search(func(sub string, start, end int) (Value, error) {
			return int64(pyCount(s, sub, start, end)), nil
		})
	case "replace":
		return method(self, name, []param{{name: "old", posOnly: true}, {name: "new", posOnly: true}, opt("count", int64(-1))}, func(a []Value) (Value, error) {
			old, err := argStr(escArg(a[0]), "replace() argument 1")
			if err != nil {
				return nil, err
			}
			repl, err := argStr(escArg(a[1]), "replace() argument 2")
			if err != nil {
				return nil, err
			}
			n, err := argSsize(a[2])
			if err != nil {
				return nil, err
			}
			out, err := replaceChecked(s, old, repl, n)
			return wrap(out), err
		}), true, nil
	case "center":
		return justify(func(w int, fill rune) string { return pyCenter(s, w, fill) })
	case "ljust":
		return justify(func(w int, fill rune) string { return pyJustify(s, w, fill, true) })
	case "rjust":
		return justify(func(w int, fill rune) string { return pyJustify(s, w, fill, false) })
	case "zfill":
		return method(self, name, posParams(req("width")), func(a []Value) (Value, error) {
			w, err := argSsize(a[0])
			if err != nil {
				return nil, err
			}
			if err := checkAlloc(w); err != nil {
				return nil, err
			}
			return wrap(pyZfill(s, w)), nil
		}), true, nil
	case "expandtabs":
		return method(self, name, []param{opt("tabsize", int64(8))}, func(a []Value) (Value, error) {
			n, err := argCInt(a[0])
			if err != nil {
				return nil, err
			}
			if err := checkAlloc(expandedLen(s, n)); err != nil {
				return nil, err
			}
			return wrap(pyExpandtabs(s, n)), nil
		}), true, nil
	case "partition":
		return partition(false)
	case "rpartition":
		return partition(true)
	case "removeprefix", "removesuffix":
		return method(self, name, posParams(req("affix")), func(a []Value) (Value, error) {
			af, err := argStr(escArg(a[0]), name+"() argument")
			if err != nil {
				return nil, err
			}
			if name == "removeprefix" {
				return wrap(strings.TrimPrefix(s, af)), nil
			}
			return wrap(strings.TrimSuffix(s, af)), nil
		}), true, nil
	case "join":
		return method(self, name, posParams(req("iterable")), func(a []Value) (Value, error) {
			items, err := iterValues(a[0])
			if err != nil {
				return nil, typeError("can only join an iterable")
			}
			parts := make([]string, len(items))
			for i, it := range items {
				str, ok := asStr(escArg(it))
				if !ok {
					return nil, typeError("sequence item %d: expected str instance, %s found", i, typeName(it))
				}
				parts[i] = str
			}
			if err := checkJoin(parts, s); err != nil {
				return nil, err
			}
			return wrap(strings.Join(parts, s)), nil
		}), true, nil
	case "format":
		return &Builtin{name: name, self: self, fn: func(r *renderer, args []Value, kw *Dict) (Value, error) {
			out, err := r.strFormat(s, args, kw, markup)
			return wrap(out), err
		}}, true, nil
	case "format_map":
		return method(self, name, posParams(req("mapping")), func(a []Value) (Value, error) {
			d, ok := a[0].(*Dict)
			if !ok {
				return nil, typeError("%s object is not subscriptable", strRepr(typeName(a[0])))
			}
			out, err := r.strFormat(s, nil, d, markup)
			return wrap(out), err
		}), true, nil
	case "translate":
		return method(self, name, posParams(req("table")), func(a []Value) (Value, error) {
			return translate(s, a[0], wrap)
		}), true, nil
	case "maketrans":
		return &Builtin{name: name, self: self, fn: func(r *renderer, args []Value, kw *Dict) (Value, error) {
			if len(kw.keys) > 0 {
				return nil, typeError("str.maketrans() takes no keyword arguments")
			}
			return maketrans(args)
		}}, true, nil
	case "encode":
		return &Builtin{name: name, self: self, fn: func(*renderer, []Value, *Dict) (Value, error) {
			return nil, errorf("TypeError", "str.encode returns bytes, a type these templates never hold")
		}}, true, nil
	}
	if markup {
		switch name {
		case "striptags":
			return noArgs(func() Value { return stripTags(s) })
		case "unescape":
			return noArgs(func() Value { return htmlUnescape(s) })
		}
	}
	return nil, false, nil
}

// str.translate with a table mapping code points to strings, code points or None
func translate(s string, table Value, wrap func(string) Value) (Value, error) {
	d, ok := table.(*Dict)
	var b strings.Builder
	for _, r := range s {
		if !ok {
			b.WriteRune(r)
			continue
		}
		v, found, err := d.get(int64(r))
		if err != nil {
			return nil, err
		}
		if !found {
			b.WriteRune(r)
			continue
		}
		switch m := v.(type) {
		case NoneType:
		case string:
			b.WriteString(m)
		case Markup:
			b.WriteString(string(m))
		case int64:
			if m < 0 || m > 0x10ffff {
				return nil, errorf("ValueError", "character mapping must be in range(0x110000)")
			}
			b.WriteString(encodeRune(rune(m)))
		default:
			return nil, typeError("character mapping must return integer, None or str")
		}
	}
	if !ok {
		return nil, typeError("%s object is not subscriptable", strRepr(typeName(table)))
	}
	return wrap(b.String()), nil
}

// str.maketrans building a code point table from a dict or from equal length strings
func maketrans(args []Value) (Value, error) {
	out := newDict()
	switch len(args) {
	case 1:
		d, ok := args[0].(*Dict)
		if !ok {
			return nil, typeError("if you give only one argument to maketrans it must be a dict")
		}
		for i, k := range d.keys {
			switch key := k.(type) {
			case string:
				if runeLen(key) != 1 {
					return nil, errorf("ValueError", "string keys in translate table must be of length 1")
				}
				_ = out.set(int64([]rune(key)[0]), d.vals[i])
			case int64:
				_ = out.set(key, d.vals[i])
			default:
				return nil, typeError("keys in translate table must be strings or integers")
			}
		}
	case 2, 3:
		x, ok1 := args[0].(string)
		y, ok2 := args[1].(string)
		if !ok1 || !ok2 {
			return nil, typeError("maketrans() argument must be str")
		}
		xs, ys := []rune(x), []rune(y)
		if len(xs) != len(ys) {
			return nil, errorf("ValueError", "the first two maketrans arguments must have equal length")
		}
		for i := range xs {
			_ = out.set(int64(xs[i]), int64(ys[i]))
		}
		if len(args) == 3 {
			z, ok := args[2].(string)
			if !ok {
				return nil, typeError("maketrans() argument 3 must be str")
			}
			for _, r := range z {
				_ = out.set(int64(r), none)
			}
		}
	default:
		return nil, typeError("maketrans() takes from 1 to 3 positional arguments but %d were given", len(args))
	}
	return out, nil
}

// The index of the first item equal to a value within start and stop, as list.index finds it
func seqIndex(items []Value, a []Value, what string) (Value, error) {
	start, err := argIndex(a[1], 0)
	if err != nil {
		return nil, err
	}
	stop, err := argIndex(a[2], math.MaxInt32)
	if err != nil {
		return nil, err
	}
	adjustIndices(&start, &stop, len(items))
	for i := start; i < stop && i < len(items); i++ {
		if pyEq(items[i], a[0]) {
			return int64(i), nil
		}
	}
	return nil, errorf("ValueError", "%s", what)
}

func seqCount(items []Value, v Value) Value {
	n := int64(0)
	for _, it := range items {
		if pyEq(it, v) {
			n++
		}
	}
	return n
}

func listAttr(l *List, name string) (Value, bool, error) {
	switch name {
	case "copy":
		return method(l, name, nil, func([]Value) (Value, error) {
			return &List{items: append([]Value(nil), l.items...)}, nil
		}), true, nil
	case "count":
		return method(l, name, posParams(req("value")), func(a []Value) (Value, error) { return seqCount(l.items, a[0]), nil }), true, nil
	case "index":
		return method(l, name, posParams(req("value"), opt("start", int64(0)), opt("stop", int64(math.MaxInt32))), func(a []Value) (Value, error) {
			return seqIndex(l.items, a, "list.index(x): x not in list")
		}), true, nil
	case "append", "clear", "extend", "insert", "pop", "remove", "reverse", "sort":
		return method(l, name, nil, nil), true, nil
	}
	return nil, false, nil
}

func tupleAttr(t *Tuple, name string) (Value, bool, error) {
	for i, n := range t.names {
		if n == name {
			return t.items[i], true, nil
		}
	}
	switch name {
	case "count":
		return method(t, name, posParams(req("value")), func(a []Value) (Value, error) { return seqCount(t.items, a[0]), nil }), true, nil
	case "index":
		return method(t, name, posParams(req("value"), opt("start", int64(0)), opt("stop", int64(math.MaxInt32))), func(a []Value) (Value, error) {
			return seqIndex(t.items, a, "tuple.index(x): x not in tuple")
		}), true, nil
	}
	return nil, false, nil
}

func dictAttr(d *Dict, name string) (Value, bool, error) {
	switch name {
	case "keys", "values", "items":
		return method(d, name, nil, func([]Value) (Value, error) { return &DictView{kind: name, dict: d}, nil }), true, nil
	case "get":
		return method(d, name, posParams(req("key"), opt("default", none)), func(a []Value) (Value, error) {
			v, found, err := d.get(a[0])
			if err != nil {
				return nil, err
			}
			if !found {
				return a[1], nil
			}
			return v, nil
		}), true, nil
	case "copy":
		return method(d, name, nil, func([]Value) (Value, error) { return d.copy(), nil }), true, nil
	case "fromkeys":
		return method(d, name, posParams(req("iterable"), opt("value", none)), func(a []Value) (Value, error) {
			keys, err := iterValues(a[0])
			if err != nil {
				return nil, err
			}
			out := newDict()
			for _, k := range keys {
				if err := out.set(k, a[1]); err != nil {
					return nil, err
				}
			}
			return out, nil
		}), true, nil
	case "clear", "pop", "popitem", "setdefault", "update":
		return method(d, name, nil, nil), true, nil
	}
	return nil, false, nil
}

func intAttr(v Value, name string) (Value, bool, error) {
	n := toBig(v)
	self := normInt(n)
	switch name {
	case "real", "numerator":
		return self, true, nil
	case "imag":
		return int64(0), true, nil
	case "denominator":
		return int64(1), true, nil
	case "conjugate":
		return method(v, name, nil, func([]Value) (Value, error) { return self, nil }), true, nil
	case "bit_length":
		return method(v, name, nil, func([]Value) (Value, error) { return int64(n.BitLen()), nil }), true, nil
	case "bit_count":
		return method(v, name, nil, func([]Value) (Value, error) {
			c := int64(0)
			for _, w := range new(big.Int).Abs(n).Bits() {
				c += int64(popcount(uint64(w)))
			}
			return c, nil
		}), true, nil
	case "as_integer_ratio":
		return method(v, name, nil, func([]Value) (Value, error) { return &Tuple{items: []Value{self, int64(1)}}, nil }), true, nil
	case "is_integer":
		return method(v, name, nil, func([]Value) (Value, error) { return true, nil }), true, nil
	case "to_bytes", "from_bytes":
		return &Builtin{name: name, self: v, fn: func(*renderer, []Value, *Dict) (Value, error) {
			return nil, errorf("TypeError", "int.%s works on bytes, a type these templates never hold", name)
		}}, true, nil
	}
	return nil, false, nil
}

func popcount(x uint64) int {
	c := 0
	for x != 0 {
		x &= x - 1
		c++
	}
	return c
}

func floatAttr(f float64, name string) (Value, bool, error) {
	switch name {
	case "real":
		return f, true, nil
	case "imag":
		return 0.0, true, nil
	case "conjugate":
		return method(f, name, nil, func([]Value) (Value, error) { return f, nil }), true, nil
	case "is_integer":
		return method(f, name, nil, func([]Value) (Value, error) { return f == math.Trunc(f) && !math.IsInf(f, 0), nil }), true, nil
	case "as_integer_ratio":
		return method(f, name, nil, func([]Value) (Value, error) {
			if math.IsInf(f, 0) {
				return nil, errorf("OverflowError", "cannot convert Infinity to integer ratio")
			}
			if math.IsNaN(f) {
				return nil, errorf("ValueError", "cannot convert NaN to integer ratio")
			}
			rat := new(big.Rat)
			rat.SetFloat64(f)
			return &Tuple{items: []Value{normInt(new(big.Int).Set(rat.Num())), normInt(new(big.Int).Set(rat.Denom()))}}, nil
		}), true, nil
	case "hex":
		return method(f, name, nil, func([]Value) (Value, error) { return floatHex(f), nil }), true, nil
	case "fromhex":
		return method(f, name, posParams(req("string")), func(a []Value) (Value, error) {
			s, err := argStr(a[0], "fromhex() argument")
			if err != nil {
				return nil, err
			}
			v, perr := strconv.ParseFloat(strings.TrimFunc(s, isPySpace), 64)
			if perr != nil && !isRangeErr(perr) {
				return nil, errorf("ValueError", "invalid hexadecimal floating-point string")
			}
			return v, nil
		}), true, nil
	case "from_number":
		return method(f, name, posParams(req("number")), func(a []Value) (Value, error) {
			if !isNumber(a[0]) {
				return nil, typeError("must be real number, not %s", typeName(a[0]))
			}
			return toFloat(a[0])
		}), true, nil
	}
	return nil, false, nil
}

// Python's float.hex layout
func floatHex(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	sign := ""
	if math.Signbit(f) {
		sign = "-"
		f = -f
	}
	if f == 0 {
		return sign + "0x0.0p+0"
	}
	frac, exp := math.Frexp(f)
	mant := uint64(math.Ldexp(frac, 53))
	exp -= 1
	lead := mant >> 52
	rest := mant & (1<<52 - 1)
	if exp < -1022 {
		shift := -1022 - exp
		mant >>= uint(shift)
		lead = mant >> 52
		rest = mant & (1<<52 - 1)
		exp = -1022
	}
	esign := "+"
	if exp < 0 {
		esign = "-"
		exp = -exp
	}
	return fmt.Sprintf("%s0x%d.%013xp%s%d", sign, lead, rest, esign, exp)
}

func rangeAttr(rg *Range, name string) (Value, bool, error) {
	switch name {
	case "start":
		return normInt(new(big.Int).Set(rg.start)), true, nil
	case "stop":
		return normInt(new(big.Int).Set(rg.stop)), true, nil
	case "step":
		return normInt(new(big.Int).Set(rg.step)), true, nil
	case "count", "index":
		return method(rg, name, posParams(req("value")), func(a []Value) (Value, error) {
			items, _ := iterValues(rg)
			if name == "count" {
				return seqCount(items, a[0]), nil
			}
			for i, it := range items {
				if pyEq(it, a[0]) {
					return int64(i), nil
				}
			}
			if isExactInt(a[0]) {
				return nil, errorf("ValueError", "range.index(x): x not in range")
			}
			return nil, errorf("ValueError", "sequence.index(x): x not in sequence")
		}), true, nil
	}
	return nil, false, nil
}

func loopAttr(l *LoopContext, name string) (Value, bool, error) {
	n := l.length()
	switch name {
	case "index0":
		return int64(l.index0), true, nil
	case "index":
		return int64(l.index0 + 1), true, nil
	case "revindex":
		return int64(n - l.index0), true, nil
	case "revindex0":
		return int64(n - l.index0 - 1), true, nil
	case "first":
		return l.index0 == 0, true, nil
	case "last":
		return l.exhausted && l.consumed >= len(l.items), true, nil
	case "length":
		return int64(n), true, nil
	case "depth":
		return int64(l.depth0 + 1), true, nil
	case "depth0":
		return int64(l.depth0), true, nil
	case "previtem":
		if l.index0 == 0 {
			return &Undefined{hint: "there is no previous item"}, true, nil
		}
		return l.items[l.index0-1], true, nil
	case "nextitem":
		if l.consumed >= len(l.items) {
			return &Undefined{hint: "there is no next item"}, true, nil
		}
		return l.items[l.consumed], true, nil
	case "cycle":
		return &Builtin{name: name, self: l, fn: func(r *renderer, args []Value, kw *Dict) (Value, error) {
			if len(kw.keys) > 0 {
				return nil, typeError("LoopContext.cycle() got an unexpected keyword argument %s", strRepr(kw.keys[0].(string)))
			}
			if len(args) == 0 {
				return nil, typeError("no items for cycling given")
			}
			return args[l.index0%len(args)], nil
		}}, true, nil
	case "changed":
		return &Builtin{name: name, self: l, fn: func(r *renderer, args []Value, kw *Dict) (Value, error) {
			if len(kw.keys) > 0 {
				return nil, typeError("LoopContext.changed() got an unexpected keyword argument %s", strRepr(kw.keys[0].(string)))
			}
			cur := &Tuple{items: args}
			if l.lastChanged != nil && pyEq(l.lastChanged, cur) {
				return false, nil
			}
			l.lastChanged = cur
			return true, nil
		}}, true, nil
	}
	return nil, false, nil
}

func macroAttr(m *Macro, name string) (Value, bool, error) {
	switch name {
	case "name":
		if m.anonymous {
			return none, true, nil
		}
		return m.name, true, nil
	case "arguments":
		items := make([]Value, len(m.sig.params))
		for i, p := range m.sig.params {
			items[i] = p
		}
		return &Tuple{items: items}, true, nil
	case "catch_kwargs":
		return m.sig.usesKwargs, true, nil
	case "catch_varargs":
		return m.sig.usesVarargs, true, nil
	case "caller":
		return m.sig.usesCaller, true, nil
	}
	return nil, false, nil
}

// jinja2's cycler object
type Cycler struct {
	items []Value
	pos   int
}

// jinja2's joiner object
type Joiner struct {
	sep  Value
	used bool
}

func cyclerAttr(c *Cycler, name string) (Value, bool, error) {
	switch name {
	case "items":
		return &Tuple{items: c.items}, true, nil
	case "pos":
		return int64(c.pos), true, nil
	case "current":
		return c.items[c.pos], true, nil
	case "next":
		return method(c, name, nil, func([]Value) (Value, error) {
			v := c.items[c.pos]
			c.pos = (c.pos + 1) % len(c.items)
			return v, nil
		}), true, nil
	case "reset":
		return method(c, name, nil, func([]Value) (Value, error) {
			c.pos = 0
			return none, nil
		}), true, nil
	}
	return nil, false, nil
}
