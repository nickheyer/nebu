package chat

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// Nesting past which a body is refused, the depth encoding/json also stops at
const maxDepth = 10000

// A body that is not JSON, or holds a value of the wrong type, with the offset reading stopped at
type syntaxError struct {
	msg string
	at  int
}

func (e *syntaxError) Error() string { return fmt.Sprintf("%s at offset %d", e.msg, e.at) }

// Reads JSON in one pass, wire structs filling mapped fields and keeping the rest raw as it goes
type scan struct {
	b     []byte
	i     int
	depth int
}

func (s *scan) fail(format string, args ...any) error {
	return &syntaxError{msg: fmt.Sprintf(format, args...), at: s.i}
}

func (s *scan) ws() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return
		}
	}
}

// The next byte past whitespace, zero at the end of the body
func (s *scan) peek() byte {
	s.ws()
	if s.i < len(s.b) {
		return s.b[s.i]
	}
	return 0
}

// Reads a body holding one value and nothing after it
func decode(b []byte, read func(*scan) error) error {
	s := &scan{b: b}
	if err := read(s); err != nil {
		return err
	}
	if s.ws(); s.i < len(s.b) {
		return s.fail("unexpected data after the value")
	}
	return nil
}

// Reads a request body, which must be one object and nothing after it
func decodeRequest(b []byte, read func(*scan) error) error {
	if s := (&scan{b: b}); s.peek() != '{' {
		return s.fail("the body must be a JSON object")
	}
	return decode(b, read)
}

// Consumes a fixed word such as null and reports whether it was there
func (s *scan) literal(word string) bool {
	if s.peek() == word[0] && len(s.b)-s.i >= len(word) && string(s.b[s.i:s.i+len(word)]) == word {
		s.i += len(word)
		return true
	}
	return false
}

func (s *scan) null() bool { return s.literal("null") }

// Reads each member in order, each call reading its key's value, null reading as none
func (s *scan) object(each func(key []byte) error) error {
	if s.null() {
		return nil
	}
	if s.peek() != '{' {
		return s.fail("want an object")
	}
	if s.depth++; s.depth > maxDepth {
		return s.fail("nested deeper than %d", maxDepth)
	}
	s.i++
	if s.peek() == '}' {
		s.i++
		s.depth--
		return nil
	}
	for {
		if s.peek() != '"' {
			return s.fail("want a key")
		}
		key, err := s.key()
		if err != nil {
			return err
		}
		if s.peek() != ':' {
			return s.fail("want a colon after a key")
		}
		s.i++
		s.ws()
		if err := each(key); err != nil {
			return err
		}
		switch s.peek() {
		case ',':
			s.i++
		case '}':
			s.i++
			s.depth--
			return nil
		default:
			return s.fail("want a comma or the end of an object")
		}
	}
}

// Reads each element in order, null reading as none
func (s *scan) array(each func() error) error {
	if s.null() {
		return nil
	}
	if s.peek() != '[' {
		return s.fail("want an array")
	}
	if s.depth++; s.depth > maxDepth {
		return s.fail("nested deeper than %d", maxDepth)
	}
	s.i++
	if s.peek() == ']' {
		s.i++
		s.depth--
		return nil
	}
	for {
		s.ws()
		if err := each(); err != nil {
			return err
		}
		switch s.peek() {
		case ',':
			s.i++
		case ']':
			s.i++
			s.depth--
			return nil
		default:
			return s.fail("want a comma or the end of an array")
		}
	}
}

// Bytes a string holds as they are, anything else needing a look
var plain = func() (t [256]bool) {
	for c := 0x20; c < utf8.RuneSelf; c++ {
		t[c] = c != '"' && c != '\\'
	}
	return t
}()

// The end of the string at i past its closing quote, and whether it holds escapes or bytes past ASCII
func strEnd(b []byte, i int) (end int, esc, high, ok bool) {
	for i++; i < len(b); {
		for i < len(b) && plain[b[i]] {
			i++
		}
		if i >= len(b) {
			break
		}
		switch c := b[i]; {
		case c == '"':
			return i + 1, esc, high, true
		case c == '\\':
			esc = true
			if i+1 >= len(b) {
				return i, esc, high, false
			}
			switch b[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				if i+6 > len(b) || !isHex4(b[i+2:i+6]) {
					return i, esc, high, false
				}
				i += 6
			default:
				return i, esc, high, false
			}
		case c < 0x20:
			return i, esc, high, false
		default:
			high = true
			i++
		}
	}
	return i, esc, high, false
}

func isHex4(b []byte) bool {
	for _, c := range b[:4] {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

func hex4(b []byte) rune {
	var r rune
	for _, c := range b[:4] {
		switch {
		case c <= '9':
			c -= '0'
		case c <= 'F':
			c -= 'A' - 10
		default:
			c -= 'a' - 10
		}
		r = r<<4 | rune(c)
	}
	return r
}

// Decodes a validated string body, bad UTF-8 and lone surrogates becoming U+FFFD as encoding/json does
func unquote(t []byte) []byte {
	out := make([]byte, 0, len(t))
	for i := 0; i < len(t); {
		c := t[i]
		switch {
		case c == '\\':
			switch t[i+1] {
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'u':
				r := hex4(t[i+2:])
				i += 6
				if utf16.IsSurrogate(r) {
					r2 := rune(-1)
					if i+6 <= len(t) && t[i] == '\\' && t[i+1] == 'u' {
						r2 = hex4(t[i+2:])
					}
					if dec := utf16.DecodeRune(r, r2); dec != utf8.RuneError {
						r = dec
						i += 6
					} else {
						r = utf8.RuneError
					}
				}
				out = utf8.AppendRune(out, r)
				continue
			default:
				out = append(out, t[i+1])
			}
			i += 2
		case c < utf8.RuneSelf:
			out = append(out, c)
			i++
		default:
			r, size := utf8.DecodeRune(t[i:])
			if r == utf8.RuneError && size == 1 {
				out = utf8.AppendRune(out, utf8.RuneError)
			} else {
				out = append(out, t[i:i+size]...)
			}
			i += size
		}
	}
	return out
}

// The bytes of the string at the cursor, decoded only when escapes or bad UTF-8 call for it
func (s *scan) strBytes() ([]byte, error) {
	if s.peek() != '"' {
		return nil, s.fail("want a string")
	}
	end, esc, high, ok := strEnd(s.b, s.i)
	if !ok {
		s.i = end
		return nil, s.fail("bad string")
	}
	t := s.b[s.i+1 : end-1]
	s.i = end
	if !esc && (!high || utf8.Valid(t)) {
		return t, nil
	}
	return unquote(t), nil
}

func (s *scan) key() ([]byte, error) { return s.strBytes() }

// Reads a string, null reading as empty
func (s *scan) str() (string, error) {
	if s.null() {
		return "", nil
	}
	t, err := s.strBytes()
	return string(t), err
}

// Reads a string that may be null, nil for null
func (s *scan) optStr() (*string, error) {
	if s.null() {
		return nil, nil
	}
	v, err := s.str()
	return &v, err
}

// Reads a string or a list of strings, empty strings and null reading as none
func (s *scan) strs() ([]string, error) {
	switch s.peek() {
	case '"':
		v, err := s.str()
		if err != nil || v == "" {
			return nil, err
		}
		return []string{v}, nil
	case 'n':
		s.null()
		return nil, nil
	}
	var out []string
	err := s.array(func() error {
		v, err := s.str()
		out = append(out, v)
		return err
	})
	return out, err
}

// Reads an array of strings, null reading as none
func (s *scan) list() ([]string, error) {
	var out []string
	err := s.array(func() error {
		v, err := s.str()
		out = append(out, v)
		return err
	})
	return out, err
}

func (s *scan) boolean() (bool, error) {
	switch {
	case s.literal("true"):
		return true, nil
	case s.literal("false"), s.null():
		return false, nil
	}
	return false, s.fail("want true or false")
}

// Reads a boolean that may be null, nil for null
func (s *scan) optBool() (*bool, error) {
	if s.null() {
		return nil, nil
	}
	v, err := s.boolean()
	return &v, err
}

// The end of the number at i, false when the bytes there are not a JSON number
func numEnd(b []byte, i int) (int, bool) {
	digits := func() bool {
		start := i
		for i < len(b) && '0' <= b[i] && b[i] <= '9' {
			i++
		}
		return i > start
	}
	if i < len(b) && b[i] == '-' {
		i++
	}
	switch {
	case i < len(b) && b[i] == '0':
		i++
	case !digits():
		return i, false
	}
	if i < len(b) && b[i] == '.' {
		i++
		if !digits() {
			return i, false
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if !digits() {
			return i, false
		}
	}
	return i, true
}

// The bytes of the JSON numeral at the cursor
func (s *scan) number() ([]byte, error) {
	s.ws()
	end, ok := numEnd(s.b, s.i)
	if !ok {
		return nil, s.fail("want a number")
	}
	lit := s.b[s.i:end]
	s.i = end
	return lit, nil
}

// Reads a float that may be null, nil for null
func (s *scan) optFloat() (*float64, error) {
	if s.null() {
		return nil, nil
	}
	lit, err := s.number()
	if err != nil {
		return nil, err
	}
	f, err := strconv.ParseFloat(string(lit), 64)
	if err != nil {
		return nil, s.fail("number %s is out of range", lit)
	}
	return &f, nil
}

// Reads a whole number, written plainly or as an integral float, null reading as zero
func (s *scan) integer() (int, error) {
	if s.null() {
		return 0, nil
	}
	lit, err := s.number()
	if err != nil {
		return 0, err
	}
	if n, err := strconv.Atoi(string(lit)); err == nil {
		return n, nil
	}
	f, err := strconv.ParseFloat(string(lit), 64)
	if err != nil || f != math.Trunc(f) || math.Abs(f) > math.MaxInt64/2 {
		return 0, s.fail("want a whole number, not %s", lit)
	}
	return int(f), nil
}

// Reads a whole number that may be null, nil for null
func (s *scan) optInt() (*int, error) {
	if s.null() {
		return nil, nil
	}
	v, err := s.integer()
	return &v, err
}

// Reads a list of numbers as float64, or a base64 string of little endian float32 as OpenAI encodes vectors
func (s *scan) vector() ([]float64, error) {
	if s.peek() == '"' {
		t, err := s.strBytes()
		if err != nil {
			return nil, err
		}
		raw, err := base64.StdEncoding.DecodeString(string(t))
		if err != nil || len(raw)%4 != 0 {
			return nil, s.fail("a base64 vector must hold whole float32 values")
		}
		out := make([]float64, len(raw)/4)
		for i := range out {
			out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:])))
		}
		return out, nil
	}
	out := []float64{}
	err := s.array(func() error {
		f, err := s.optFloat()
		if err != nil {
			return err
		}
		if f == nil {
			return s.fail("a vector holds numbers, not null")
		}
		out = append(out, *f)
		return nil
	})
	return out, err
}

// Reads a list of token ids
func (s *scan) tokens() ([]int, error) {
	out := []int{}
	err := s.array(func() error {
		n, err := s.integer()
		out = append(out, n)
		return err
	})
	return out, err
}

// Reads a string, strings, token ids, or lists of token ids, one part each
func (s *scan) prompts() ([]Part, error) {
	switch s.peek() {
	case 'n':
		s.null()
		return nil, nil
	case '"':
		v, err := s.str()
		return []Part{{Type: "text", Text: v}}, err
	}
	var out []Part
	var flat []int
	err := s.array(func() error {
		switch c := s.peek(); {
		case c == '"':
			if flat != nil {
				return s.fail("a prompt list mixes token ids with prompts")
			}
			v, err := s.str()
			out = append(out, Part{Type: "text", Text: v})
			return err
		case c == '[':
			if flat != nil {
				return s.fail("a prompt list mixes token ids with prompts")
			}
			ids, err := s.tokens()
			out = append(out, Part{Type: "tokens", Tokens: ids})
			return err
		default:
			if out != nil {
				return s.fail("a prompt list mixes token ids with prompts")
			}
			n, err := s.integer()
			flat = append(flat, n)
			return err
		}
	})
	if flat != nil {
		out = []Part{{Type: "tokens", Tokens: flat}}
	}
	return out, err
}

// Moves past one value, checking it is well formed
func (s *scan) skip() error {
	switch s.peek() {
	case '{':
		return s.object(func([]byte) error { return s.skip() })
	case '[':
		return s.array(s.skip)
	case '"':
		end, _, _, ok := strEnd(s.b, s.i)
		if !ok {
			s.i = end
			return s.fail("bad string")
		}
		s.i = end
		return nil
	case 't':
		if s.literal("true") {
			return nil
		}
	case 'f':
		if s.literal("false") {
			return nil
		}
	case 'n':
		if s.null() {
			return nil
		}
	default:
		_, err := s.number()
		return err
	}
	return s.fail("want a value")
}

// Reads one value as its JSON bytes, copied out so the body may be reused
func (s *scan) raw() (json.RawMessage, error) {
	s.ws()
	start := s.i
	if err := s.skip(); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.Clone(s.b[start:s.i])), nil
}

// Keeps a member no field maps, raw under its key
func (s *scan) extra(into *map[string]json.RawMessage, key []byte) error {
	v, err := s.raw()
	if err != nil {
		return err
	}
	if *into == nil {
		*into = make(map[string]json.RawMessage, 4)
	}
	(*into)[string(key)] = v
	return nil
}

// Whether raw JSON holds an object
func isObject(raw []byte) bool {
	s := &scan{b: raw}
	return s.peek() == '{' && s.skip() == nil && func() bool { s.ws(); return s.i == len(s.b) }()
}

const hexDigits = "0123456789abcdef"

// Appends a JSON string, control characters, bad UTF-8, and line separators escaped as encoding/json escapes them
func appendStr(b []byte, s string) []byte {
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if plain[c] {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '"', '\\':
				b = append(b, '\\', c)
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			b = append(b, `�`...)
			i++
			start = i
			continue
		}
		if r == ' ' || r == ' ' {
			b = append(b, s[start:i]...)
			b = append(b, '\\', 'u', '2', '0', '2', hexDigits[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}

// Appends a float as encoding/json writes one
func appendFloat(b []byte, f float64) []byte {
	format := byte('f')
	if abs := math.Abs(f); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	b = strconv.AppendFloat(b, f, format, -1, 64)
	if format == 'e' {
		if n := len(b); n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return b
}

// Writes one JSON object member by member, unmapped members last unless a mapped one took their key
type obj struct {
	b     *[]byte
	n     int
	wrote [24]string
}

func begin(b *[]byte) obj {
	*b = append(*b, '{')
	return obj{b: b}
}

func (o *obj) key(k string) {
	if o.n > 0 {
		*o.b = append(*o.b, ',')
	}
	if o.n < len(o.wrote) {
		o.wrote[o.n] = k
	}
	o.n++
	*o.b = appendStr(*o.b, k)
	*o.b = append(*o.b, ':')
}

func (o *obj) str(k, v string) {
	o.key(k)
	*o.b = appendStr(*o.b, v)
}

// Writes a string member unless it is empty
func (o *obj) strIf(k, v string) {
	if v != "" {
		o.str(k, v)
	}
}

func (o *obj) num(k string, v int) {
	o.key(k)
	*o.b = strconv.AppendInt(*o.b, int64(v), 10)
}

// Writes a number member unless it is zero
func (o *obj) numIf(k string, v int) {
	if v != 0 {
		o.num(k, v)
	}
}

// Writes a decimal member unless it is nil
func (o *obj) float(k string, v *float64) {
	if v != nil {
		o.key(k)
		*o.b = appendFloat(*o.b, *v)
	}
}

func (o *obj) boolean(k string, v bool) {
	o.key(k)
	*o.b = strconv.AppendBool(*o.b, v)
}

// Writes a member already in JSON unless it is empty
func (o *obj) raw(k string, v []byte) {
	if len(v) > 0 {
		o.key(k)
		*o.b = append(*o.b, v...)
	}
}

// Writes a list of strings unless it is empty
func (o *obj) strList(k string, v []string) {
	if len(v) == 0 {
		return
	}
	o.key(k)
	*o.b = append(*o.b, '[')
	for i, s := range v {
		if i > 0 {
			*o.b = append(*o.b, ',')
		}
		*o.b = appendStr(*o.b, s)
	}
	*o.b = append(*o.b, ']')
}

// Writes a member whose value is marshalled by encoding/json
func (o *obj) any(k string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	o.raw(k, data)
	return nil
}

// Whether a member under the key was written
func (o *obj) has(k string) bool {
	for _, w := range o.wrote[:min(o.n, len(o.wrote))] {
		if w == k {
			return true
		}
	}
	return false
}

// Writes the unmapped members in key order, leaving out keys a mapped member took, and closes the object
func (o *obj) close(extra map[string]json.RawMessage) {
	if len(extra) > 0 {
		keys := make([]string, 0, len(extra))
		for k := range extra {
			if !o.has(k) {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			o.raw(k, extra[k])
		}
	}
	*o.b = append(*o.b, '}')
}

// Appends a list, each element by its writer
func appendList[T any](b *[]byte, list []T, each func(*T, *[]byte)) {
	*b = append(*b, '[')
	for i := range list {
		if i > 0 {
			*b = append(*b, ',')
		}
		each(&list[i], b)
	}
	*b = append(*b, ']')
}

// Appends prompts, a string for one text prompt and a list of strings or token id lists otherwise
func appendPrompts(b *[]byte, parts []Part) {
	if len(parts) == 1 && parts[0].Type == "text" {
		*b = appendStr(*b, parts[0].Text)
		return
	}
	if len(parts) == 1 {
		appendTokens(b, parts[0].Tokens)
		return
	}
	appendList(b, parts, func(p *Part, b *[]byte) {
		if p.Type == "tokens" {
			appendTokens(b, p.Tokens)
			return
		}
		*b = appendStr(*b, p.Text)
	})
}

func appendTokens(b *[]byte, ids []int) {
	*b = append(*b, '[')
	for i, id := range ids {
		if i > 0 {
			*b = append(*b, ',')
		}
		*b = strconv.AppendInt(*b, int64(id), 10)
	}
	*b = append(*b, ']')
}
