package jinja

import (
	"math/big"
	"strings"
	"unicode/utf8"
)

// A shape dependent cost, offset plus step times a shape feature of the operation's subject, never below floor
type warmShapeRule struct {
	feature             string
	floor, offset, step int
}

// Shape features named as generate.py fits rules, each the frames a stdlib or jinja2 routine pushes on a value
var shapeFeatures map[string]func(Value) int

func init() {
	shapeFeatures = map[string]func(Value) int{
		"repr":   reprFrames,
		"str":    strFrames,
		"items":  itemsFrames,
		"json":   jsonFrames,
		"pprint": pprintFrames,
	}
}

// Frames repr() pushes below its caller, the Python __repr__ methods it reaches and what they call
func reprFrames(v Value) int {
	f, _ := reprWalk(v)
	return f
}

// The deepest frame repr() reaches below its caller in Python's order, and whether it finished without raising
func reprWalk(v Value) (int, bool) {
	switch x := v.(type) {
	case *big.Int:
		_, err := decimalString(x)
		return 0, err == nil
	case Markup, *Undefined, *Macro, *TemplateRef:
		return 1, true
	case *Namespace:
		f, ok := reprWalk(x.attrs)
		return max(2, 1+f), ok
	case *LoopContext:
		return 2, true
	case *Builtin:
		switch x.self.(type) {
		case *LoopContext, *Cycler, *Joiner:
			return reprWalk(x.self)
		}
		return 0, true
	case *List:
		return reprItems(x.items, 0)
	case *Tuple:
		if x.names != nil {
			return reprItems(x.items, 1)
		}
		return reprItems(x.items, 0)
	case *Dict:
		return reprPairs(x, 0)
	case *DictView:
		return reprPairs(x.dict, 0)
	}
	return 0, true
}

// The deepest frame repr() reaches over items in order below a depth, stopping at the first that raises
func reprItems(items []Value, depth int) (int, bool) {
	m := depth
	for _, it := range items {
		f, ok := reprWalk(it)
		m = max(m, depth+f)
		if !ok {
			return m, false
		}
	}
	return m, true
}

// The deepest frame repr() reaches over a dict's keys and values in insertion order, stopping at the first that raises
func reprPairs(d *Dict, depth int) (int, bool) {
	m := depth
	for i, k := range d.keys {
		for _, it := range []Value{k, d.vals[i]} {
			f, ok := reprWalk(it)
			m = max(m, depth+f)
			if !ok {
				return m, false
			}
		}
	}
	return m, true
}

// Frames str() pushes below its caller, __str__ where a type defines one and repr otherwise
func strFrames(v Value) int {
	f, _ := strWalk(v)
	return f
}

// The deepest frame str() reaches below its caller, and whether it finished without raising
func strWalk(v Value) (int, bool) {
	switch x := v.(type) {
	case string, Markup, NoneType, bool, int64, float64, complex128:
		return 0, true
	case *Undefined:
		return 1, true
	case *Tuple:
		if x.names != nil {
			f, ok := reprWalk(x)
			return 1 + f, ok
		}
	}
	return reprWalk(v)
}

// The most frames str() pushes on any item iterating a list, tuple, dict, string or range reaches
func itemsFrames(v Value) int {
	var items []Value
	switch x := v.(type) {
	case *List:
		items = x.items
	case *Tuple:
		items = x.items
	case *Dict:
		items = x.keys
	}
	m := 0
	for _, it := range items {
		m = max(m, strFrames(it))
	}
	return m
}

// Frames json.dumps pushes, encode returning strings at once, and iterencode calling default on the first value it cannot encode
func jsonFrames(v Value) int {
	switch v.(type) {
	case string, Markup:
		return 2
	}
	extra, _ := jsonDefault(v)
	return 3 + extra
}

// Frames JSONEncoder.default pushes for the first unencodable value in encoding order, and whether encoding stopped there
func jsonDefault(v Value) (int, bool) {
	switch x := v.(type) {
	case string, Markup, NoneType, bool, int64, float64:
		return 0, false
	case *big.Int:
		_, err := decimalString(x)
		return 0, err != nil
	case *List:
		return jsonItems(x.items)
	case *Tuple:
		return jsonItems(x.items)
	case *Dict:
		for i, k := range x.keys {
			switch k.(type) {
			case string, Markup, NoneType, bool, int64, *big.Int, float64:
			default:
				return 0, true
			}
			if extra, stop := jsonDefault(x.vals[i]); stop {
				return extra, true
			}
		}
		return 0, false
	case *Namespace:
		return 2, true
	}
	return 1, true
}

func jsonItems(items []Value) (int, bool) {
	for _, it := range items {
		if extra, stop := jsonDefault(it); stop {
			return extra, true
		}
	}
	return 0, false
}

// Frames pprint.pformat pushes, its PrettyPrinter calls down to _format and the recursion _format and _safe_repr make
func pprintFrames(v Value) int {
	pp := &prettyPrinter{width: 80}
	peak, _ := pp.formatFrames(v, 3, 0, 0, 0)
	return max(2, peak)
}

// The deepest frame PrettyPrinter._format reaches from depth d, failing where its repr raises
func (pp *prettyPrinter) formatFrames(v Value, d, indent, allowance, level int) (int, error) {
	peak, err := safeReprFrames(v, d+3)
	if err != nil {
		return peak, err
	}
	rep, err := pp.repr(v)
	if err != nil {
		return peak, err
	}
	if utf8.RuneCountInString(rep) <= pp.width-indent-allowance {
		return peak, nil
	}
	switch x := v.(type) {
	case *Dict:
		if len(x.keys) == 0 {
			return max(peak, d+1), nil
		}
		peak = max(peak, d+3)
		ind := indent + 1
		items := sortedItems(x)
		for i, it := range items {
			last := i == len(items)-1
			kp, err := safeReprFrames(it[0], d+5)
			peak = max(peak, kp)
			if err != nil {
				return peak, err
			}
			krep, err := pp.repr(it[0])
			if err != nil {
				return peak, err
			}
			al := 1
			if last {
				al = allowance + 1
			}
			vp, err := pp.formatFrames(it[1], d+3, ind+runeLen(krep)+2, al, level+1)
			peak = max(peak, vp)
			if err != nil {
				return peak, err
			}
		}
		return peak, nil
	case *List:
		return pp.itemsFrames(x.items, d, indent, allowance+1, level+1, peak)
	case *Tuple:
		if x.names == nil {
			end := 1
			if len(x.items) == 1 {
				end = 2
			}
			return pp.itemsFrames(x.items, d, indent, allowance+end, level+1, peak)
		}
	case string:
		if pp.strFindall(x, indent, allowance, level+1) {
			return max(peak, d+3), nil
		}
		return max(peak, d+1), nil
	}
	return max(peak, d+1), nil
}

// The deepest frame _pprint_list or _pprint_tuple reaches through _format_items from _format at depth d
func (pp *prettyPrinter) itemsFrames(items []Value, d, indent, allowance, level, peak int) (int, error) {
	peak = max(peak, d+2)
	indent++
	for i, it := range items {
		al := 1
		if i == len(items)-1 {
			al = allowance
		}
		p, err := pp.formatFrames(it, d+3, indent, al, level)
		peak = max(peak, p)
		if err != nil {
			return peak, err
		}
	}
	return peak, nil
}

// Whether _pprint_str splits a line at whitespace, calling re.findall, as a line's repr overflows its width
func (pp *prettyPrinter) strFindall(s string, indent, allowance, level int) bool {
	if s == "" {
		return false
	}
	lines := pySplitlines(s, true)
	if level == 1 {
		indent++
		allowance++
	}
	maxWidth1 := pp.width - indent
	for i, line := range lines {
		if i == len(lines)-1 {
			maxWidth1 -= allowance
		}
		if utf8.RuneCountInString(strRepr(line)) > maxWidth1 {
			return true
		}
	}
	return false
}

// The deepest frame PrettyPrinter._safe_repr reaches from depth s, scalars repr'd in C and containers recursing through format
func safeReprFrames(v Value, s int) (int, error) {
	switch x := v.(type) {
	case string, NoneType, bool, int64, float64, complex128:
		return s, nil
	case *big.Int:
		_, err := decimalString(x)
		return s, err
	case *Dict:
		if len(x.keys) == 0 {
			return s, nil
		}
		peak := s + 2
		for _, it := range sortedItems(x) {
			for _, kv := range it {
				p, err := safeReprFrames(kv, s+2)
				peak = max(peak, p)
				if err != nil {
					return peak, err
				}
			}
		}
		return peak, nil
	case *List:
		return safeReprItems(x.items, s)
	case *Tuple:
		if x.names == nil {
			return safeReprItems(x.items, s)
		}
	}
	if _, err := pyRepr(v); err != nil {
		return s, err
	}
	return s + reprFrames(v), nil
}

func safeReprItems(items []Value, s int) (int, error) {
	peak := s
	for _, it := range items {
		p, err := safeReprFrames(it, s+2)
		peak = max(peak, p)
		if err != nil {
			return peak, err
		}
	}
	return peak, nil
}

// Frames a measured operation pushes above its caller, by its shape rule, its exact key or the nearest broader key
func warmBuiltinFrames(key string, subject Value) int {
	if rule, ok := warmShapeRules[key]; ok {
		return max(rule.floor, rule.offset+rule.step*shapeFeatures[rule.feature](subject))
	}
	if k, ok := warmFrames[key]; ok {
		return k
	}
	parts := strings.Split(key, " ")
	for n := min(len(parts)-1, 3); n >= 1; n-- {
		if k, ok := warmFrameLevels[strings.Join(parts[:n], " ")]; ok {
			return k
		}
	}
	panic("jinja: no warm frame cost measured for " + key)
}
