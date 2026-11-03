package jinja

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Parses JSON text into template values as Python's json.loads does, keeping key order
func jsonLoads(text string) (Value, error) {
	p := &jsonParser{src: text}
	p.skip()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.skip()
	if p.pos != len(p.src) {
		return nil, p.fail("Extra data")
	}
	return v, nil
}

type jsonParser struct {
	src string
	pos int
}

func (p *jsonParser) fail(msg string) error {
	line := strings.Count(p.src[:p.pos], "\n") + 1
	col := p.pos - strings.LastIndexByte(p.src[:p.pos], '\n')
	return errorf("JSONDecodeError", "%s: line %d column %d (char %d)", msg, line, col, utf8.RuneCountInString(p.src[:p.pos]))
}

func (p *jsonParser) skip() {
	for p.pos < len(p.src) && strings.IndexByte(" \t\n\r", p.src[p.pos]) >= 0 {
		p.pos++
	}
}

func (p *jsonParser) value() (Value, error) {
	if p.pos >= len(p.src) {
		return nil, p.fail("Expecting value")
	}
	switch c := p.src[p.pos]; {
	case c == '"':
		p.pos++
		return p.str()
	case c == '{':
		p.pos++
		return p.object()
	case c == '[':
		p.pos++
		return p.array()
	case strings.HasPrefix(p.src[p.pos:], "null"):
		p.pos += 4
		return none, nil
	case strings.HasPrefix(p.src[p.pos:], "true"):
		p.pos += 4
		return true, nil
	case strings.HasPrefix(p.src[p.pos:], "false"):
		p.pos += 5
		return false, nil
	case strings.HasPrefix(p.src[p.pos:], "NaN"):
		p.pos += 3
		return math.NaN(), nil
	case strings.HasPrefix(p.src[p.pos:], "Infinity"):
		p.pos += 8
		return math.Inf(1), nil
	case strings.HasPrefix(p.src[p.pos:], "-Infinity"):
		p.pos += 9
		return math.Inf(-1), nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	}
	return nil, p.fail("Expecting value")
}

// Reads a number by Python's json NUMBER_RE, an int without fraction or exponent
func (p *jsonParser) number() (Value, error) {
	start := p.pos
	if p.src[p.pos] == '-' {
		p.pos++
	}
	if p.pos >= len(p.src) || !isASCIIDigit(p.src[p.pos]) {
		p.pos = start
		return nil, p.fail("Expecting value")
	}
	if p.src[p.pos] == '0' {
		p.pos++
	} else {
		for p.pos < len(p.src) && isASCIIDigit(p.src[p.pos]) {
			p.pos++
		}
	}
	isFloat := false
	if p.pos+1 < len(p.src) && p.src[p.pos] == '.' && isASCIIDigit(p.src[p.pos+1]) {
		isFloat = true
		p.pos++
		for p.pos < len(p.src) && isASCIIDigit(p.src[p.pos]) {
			p.pos++
		}
	}
	if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
		q := p.pos + 1
		if q < len(p.src) && (p.src[q] == '+' || p.src[q] == '-') {
			q++
		}
		if q < len(p.src) && isASCIIDigit(p.src[q]) {
			isFloat = true
			for q < len(p.src) && isASCIIDigit(p.src[q]) {
				q++
			}
			p.pos = q
		}
	}
	text := p.src[start:p.pos]
	if isFloat {
		f, err := strconv.ParseFloat(text, 64)
		if err != nil && !isRangeErr(err) {
			return nil, p.fail("Expecting value")
		}
		return f, nil
	}
	n, _ := new(big.Int).SetString(text, 10)
	return normInt(n), nil
}

// Reads a string body after its opening quote, rejecting control characters as strict mode does
func (p *jsonParser) str() (string, error) {
	var b strings.Builder
	for {
		if p.pos >= len(p.src) {
			return "", p.fail("Unterminated string starting at")
		}
		c := p.src[p.pos]
		switch {
		case c == '"':
			p.pos++
			return b.String(), nil
		case c < 0x20:
			return "", p.fail("Invalid control character at")
		case c != '\\':
			b.WriteByte(c)
			p.pos++
			continue
		}
		p.pos++
		if p.pos >= len(p.src) {
			return "", p.fail("Unterminated string starting at")
		}
		e := p.src[p.pos]
		p.pos++
		switch e {
		case '"', '\\', '/':
			b.WriteByte(e)
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'u':
			r, err := p.hex4()
			if err != nil {
				return "", err
			}
			if r >= 0xd800 && r <= 0xdbff && strings.HasPrefix(p.src[p.pos:], "\\u") {
				save := p.pos
				p.pos += 2
				lo, err := p.hex4()
				if err == nil && lo >= 0xdc00 && lo <= 0xdfff {
					r = 0x10000 + (r-0xd800)<<10 + (lo - 0xdc00)
				} else {
					p.pos = save
				}
			}
			b.WriteString(encodeRune(r))
		default:
			p.pos -= 2
			return "", p.fail("Invalid \\escape")
		}
	}
}

func (p *jsonParser) hex4() (rune, error) {
	if p.pos+4 > len(p.src) {
		return 0, p.fail("Invalid \\uXXXX escape")
	}
	v, err := strconv.ParseUint(p.src[p.pos:p.pos+4], 16, 32)
	if err != nil || strings.ContainsAny(p.src[p.pos:p.pos+4], "+-_") {
		return 0, p.fail("Invalid \\uXXXX escape")
	}
	p.pos += 4
	return rune(v), nil
}

func (p *jsonParser) object() (Value, error) {
	d := newDict()
	p.skip()
	if p.pos < len(p.src) && p.src[p.pos] == '}' {
		p.pos++
		return d, nil
	}
	for {
		if p.pos >= len(p.src) || p.src[p.pos] != '"' {
			return nil, p.fail("Expecting property name enclosed in double quotes")
		}
		p.pos++
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		p.skip()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return nil, p.fail("Expecting ':' delimiter")
		}
		p.pos++
		p.skip()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		d.put(k, v)
		p.skip()
		if p.pos < len(p.src) && p.src[p.pos] == '}' {
			p.pos++
			return d, nil
		}
		if p.pos >= len(p.src) || p.src[p.pos] != ',' {
			return nil, p.fail("Expecting ',' delimiter")
		}
		p.pos++
		p.skip()
	}
}

func (p *jsonParser) array() (Value, error) {
	l := &List{}
	p.skip()
	if p.pos < len(p.src) && p.src[p.pos] == ']' {
		p.pos++
		return l, nil
	}
	for {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		l.items = append(l.items, v)
		p.skip()
		if p.pos < len(p.src) && p.src[p.pos] == ']' {
			p.pos++
			return l, nil
		}
		if p.pos >= len(p.src) || p.src[p.pos] != ',' {
			return nil, p.fail("Expecting ',' delimiter")
		}
		p.pos++
		p.skip()
	}
}

// The options of Python's json.dumps that transformers' tojson exposes
type jsonOptions struct {
	ensureASCII bool
	indent      *string
	itemSep     string
	keySep      string
	sortKeys    bool
}

// Serializes a value as Python's json.dumps does with the given options
func jsonDumps(v Value, opts jsonOptions) (string, error) {
	var b strings.Builder
	if err := jsonEncode(&b, v, opts, 0); err != nil {
		return "", err
	}
	return b.String(), nil
}

func jsonEncode(b *strings.Builder, v Value, opts jsonOptions, level int) error {
	if err := checkBytes(b.Len()); err != nil {
		return err
	}
	switch x := v.(type) {
	case NoneType:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int64, *big.Int:
		s, err := pyStr(x)
		if err != nil {
			return err
		}
		b.WriteString(s)
	case float64:
		b.WriteString(jsonFloat(x))
	case string:
		jsonString(b, x, opts.ensureASCII)
	case Markup:
		jsonString(b, string(x), opts.ensureASCII)
	case *List:
		return jsonArray(b, x.items, opts, level)
	case *Tuple:
		return jsonArray(b, x.items, opts, level)
	case *Dict:
		return jsonObject(b, x, opts, level)
	default:
		return typeError("Object of type %s is not JSON serializable", typeName(v))
	}
	return nil
}

func jsonFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return floatRepr(f)
}

// Writes the newline and indentation json.dumps puts before an item at a level
func jsonNewline(b *strings.Builder, opts jsonOptions, level int) {
	b.WriteByte('\n')
	b.WriteString(strings.Repeat(*opts.indent, level))
}

func jsonArray(b *strings.Builder, items []Value, opts jsonOptions, level int) error {
	if len(items) == 0 {
		b.WriteString("[]")
		return nil
	}
	b.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			b.WriteString(opts.itemSep)
		}
		if opts.indent != nil {
			jsonNewline(b, opts, level+1)
		}
		if err := jsonEncode(b, it, opts, level+1); err != nil {
			return err
		}
	}
	if opts.indent != nil {
		jsonNewline(b, opts, level)
	}
	b.WriteByte(']')
	return nil
}

func jsonObject(b *strings.Builder, d *Dict, opts jsonOptions, level int) error {
	if len(d.keys) == 0 {
		b.WriteString("{}")
		return nil
	}
	keys := append([]Value(nil), d.keys...)
	vals := append([]Value(nil), d.vals...)
	if opts.sortKeys {
		sortKeys := append([]Value(nil), keys...)
		order := make([]Value, len(keys))
		for i := range order {
			order[i] = int64(i)
		}
		if err := pySort(order, sortKeys, false); err != nil {
			return err
		}
		for i, o := range order {
			keys[i] = d.keys[o.(int64)]
			vals[i] = d.vals[o.(int64)]
		}
	}
	b.WriteByte('{')
	first := true
	for i, k := range keys {
		var ks string
		switch x := k.(type) {
		case string:
			ks = x
		case Markup:
			ks = string(x)
		case float64:
			ks = jsonFloat(x)
		case bool:
			ks = map[bool]string{true: "true", false: "false"}[x]
		case NoneType:
			ks = "null"
		case int64, *big.Int:
			var err error
			if ks, err = pyStr(x); err != nil {
				return err
			}
		default:
			return typeError("keys must be str, int, float, bool or None, not %s", typeName(k))
		}
		if !first {
			b.WriteString(opts.itemSep)
		}
		first = false
		if opts.indent != nil {
			jsonNewline(b, opts, level+1)
		}
		jsonString(b, ks, opts.ensureASCII)
		b.WriteString(opts.keySep)
		if err := jsonEncode(b, vals[i], opts, level+1); err != nil {
			return err
		}
	}
	if opts.indent != nil {
		jsonNewline(b, opts, level)
	}
	b.WriteByte('}')
	return nil
}

// Writes a JSON string escaped as json.dumps escapes it with or without ensure_ascii
func jsonString(b *strings.Builder, s string, ascii bool) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			if sr, ok := wtf8Surrogate(s[i:]); ok {
				r, size = sr, 3
			}
		}
		raw := s[i : i+size]
		i += size
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				b.WriteString(`\u` + leftPadHex(int64(r), 4))
			case ascii && r > 0x7e:
				if r >= 0x10000 {
					v := r - 0x10000
					b.WriteString(`\u` + leftPadHex(int64(0xd800|(v>>10)&0x3ff), 4))
					b.WriteString(`\u` + leftPadHex(int64(0xdc00|v&0x3ff), 4))
				} else {
					b.WriteString(`\u` + leftPadHex(int64(r), 4))
				}
			case ascii && r == 0x7f:
				b.WriteString(`\u007f`)
			default:
				b.WriteString(raw)
			}
		}
	}
	b.WriteByte('"')
}
