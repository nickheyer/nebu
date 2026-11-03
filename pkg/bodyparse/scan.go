package bodyparse

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Deepest container nesting accepted, matching encoding/json
const maxDepth = 10000

const (
	lsb = 0x0101010101010101
	msb = 0x8080808080808080
)

// Bytes that may appear unescaped inside a JSON string
var plain = func() (t [256]bool) {
	for c := 0x20; c < 256; c++ {
		t[c] = c != '"' && c != '\\'
	}
	return t
}()

// Reports whether any byte of the word is a control byte, a quote or a backslash
func special(x uint64) bool {
	ctl := (x - lsb*0x20) & ^x & msb
	q := x ^ (lsb * '"')
	q = (q - lsb) & ^q & msb
	bs := x ^ (lsb * '\\')
	bs = (bs - lsb) & ^bs & msb
	return ctl|q|bs != 0
}

// Reports whether any byte of the word is a backslash or outside ASCII
func escapeOrHigh(x uint64) bool {
	bs := x ^ (lsb * '\\')
	return (x|(bs-lsb)&^bs)&msb != 0
}

func isWS(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func skipWS(b []byte, i int) int {
	for i < len(b) && isWS(b[i]) {
		i++
	}
	return i
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// Returns the index after the string opening at b[i], whether it holds escapes, and whether it is well formed
func scanString(b []byte, i int) (int, bool, bool) {
	n := len(b)
	i++
	escaped := false
	for {
		for i+8 <= n && !special(binary.LittleEndian.Uint64(b[i:])) {
			i += 8
		}
		for i < n && plain[b[i]] {
			i++
		}
		if i >= n {
			return i, escaped, false
		}
		switch b[i] {
		case '"':
			return i + 1, escaped, true
		case '\\':
			escaped = true
			if i+1 >= n {
				return i, escaped, false
			}
			switch b[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				if i+6 > n || !isHex(b[i+2]) || !isHex(b[i+3]) || !isHex(b[i+4]) || !isHex(b[i+5]) {
					return i, escaped, false
				}
				i += 6
			default:
				return i, escaped, false
			}
		default:
			return i, escaped, false
		}
	}
}

// Returns the index after the number starting at b[i] and whether it follows the JSON grammar
func scanNumber(b []byte, i int) (int, bool) {
	n := len(b)
	if i < n && b[i] == '-' {
		i++
	}
	switch {
	case i >= n:
		return i, false
	case b[i] == '0':
		i++
	case b[i] >= '1' && b[i] <= '9':
		for i++; i < n && isDigit(b[i]); i++ {
		}
	default:
		return i, false
	}
	if i < n && b[i] == '.' {
		i++
		if i >= n || !isDigit(b[i]) {
			return i, false
		}
		for i < n && isDigit(b[i]) {
			i++
		}
	}
	if i < n && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < n && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i >= n || !isDigit(b[i]) {
			return i, false
		}
		for i < n && isDigit(b[i]) {
			i++
		}
	}
	return i, true
}

// Returns the index after an object key and its colon, the key opening at b[i]
func scanKey(b []byte, i int) (int, bool) {
	if i >= len(b) || b[i] != '"' {
		return i, false
	}
	i, _, ok := scanString(b, i)
	if !ok {
		return i, false
	}
	i = skipWS(b, i)
	if i >= len(b) || b[i] != ':' {
		return i, false
	}
	return i + 1, true
}

// Returns the index after the value at b[i] inside depth open containers, validating it as encoding/json does
func scanValue(b []byte, i int, depth int) (int, bool) {
	var stackBuf [64]byte
	stack := stackBuf[:0]
	n := len(b)
	for {
		var ok bool
		i = skipWS(b, i)
		if i >= n {
			return i, false
		}
		switch b[i] {
		case '{', '[':
			open := b[i]
			if depth+len(stack)+1 > maxDepth {
				return i, false
			}
			i = skipWS(b, i+1)
			if i < n && b[i] == open+2 {
				i++
				break
			}
			stack = append(stack, open)
			if open == '{' {
				if i, ok = scanKey(b, i); !ok {
					return i, false
				}
			}
			continue
		case '"':
			if i, _, ok = scanString(b, i); !ok {
				return i, false
			}
		case 't':
			if i, ok = literal(b, i, "true"); !ok {
				return i, false
			}
		case 'f':
			if i, ok = literal(b, i, "false"); !ok {
				return i, false
			}
		case 'n':
			if i, ok = literal(b, i, "null"); !ok {
				return i, false
			}
		default:
			if i, ok = scanNumber(b, i); !ok {
				return i, false
			}
		}
		for {
			if len(stack) == 0 {
				return i, true
			}
			i = skipWS(b, i)
			if i >= n {
				return i, false
			}
			top := stack[len(stack)-1]
			if b[i] == ',' {
				i++
				if top == '{' {
					if i, ok = scanKey(b, skipWS(b, i)); !ok {
						return i, false
					}
				}
				break
			}
			if b[i] != top+2 {
				return i, false
			}
			stack = stack[:len(stack)-1]
			i++
		}
	}
}

func literal(b []byte, i int, lit string) (int, bool) {
	if len(b)-i < len(lit) || string(b[i:i+len(lit)]) != lit {
		return i, false
	}
	return i + len(lit), true
}

// Returns the index after the value at b[i] of a body already validated
func skip(b []byte, i int) int {
	switch b[i] {
	case '"':
		i, _, _ = scanString(b, i)
		return i
	case '{', '[':
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				i, _, _ = scanString(b, i)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
			i++
		}
		return i
	default:
		for i < len(b) && !isWS(b[i]) && b[i] != ',' && b[i] != '}' && b[i] != ']' {
			i++
		}
		return i
	}
}

// Iterates the members of a validated object
type members struct {
	b    []byte
	i    int
	done bool
}

func objectAt(b []byte, i int) members {
	return members{b: b, i: skipWS(b, i+1)}
}

// The next member's raw key, whether it needs decoding, and its value bounds, false after the last
func (m *members) next() (key []byte, esc bool, vs, ve int, ok bool) {
	if m.done || m.i >= len(m.b) || m.b[m.i] == '}' {
		m.done = true
		return nil, false, 0, 0, false
	}
	ke, esc, _ := scanString(m.b, m.i)
	key = m.b[m.i+1 : ke-1]
	esc = needsDecode(key, esc)
	vs = skipWS(m.b, skipWS(m.b, ke)+1)
	ve = skip(m.b, vs)
	m.i = skipWS(m.b, ve)
	if m.i < len(m.b) && m.b[m.i] == ',' {
		m.i = skipWS(m.b, m.i+1)
	} else {
		m.done = true
	}
	return key, esc, vs, ve, true
}

// Iterates the elements of a validated array
type elements struct {
	b    []byte
	i    int
	done bool
}

func arrayAt(b []byte, i int) elements {
	return elements{b: b, i: skipWS(b, i+1)}
}

// The next element's value bounds, false after the last
func (e *elements) next() (vs, ve int, ok bool) {
	if e.done || e.i >= len(e.b) || e.b[e.i] == ']' {
		e.done = true
		return 0, 0, false
	}
	vs = e.i
	ve = skip(e.b, vs)
	e.i = skipWS(e.b, ve)
	if e.i < len(e.b) && e.b[e.i] == ',' {
		e.i = skipWS(e.b, e.i+1)
	} else {
		e.done = true
	}
	return vs, ve, true
}

// Whether raw key bytes need decoding before they compare, holding escapes or bytes outside ASCII
func needsDecode(raw []byte, esc bool) bool {
	if esc {
		return true
	}
	for _, c := range raw {
		if c >= utf8.RuneSelf {
			return true
		}
	}
	return false
}

// Whether a raw validated key decodes to name, dec marking keys that need decoding first
func keyIs(raw []byte, dec bool, name string) bool {
	if !dec {
		return string(raw) == name
	}
	var sb strings.Builder
	sb.Grow(len(raw))
	writeUnquoted(&sb, raw)
	return sb.String() == name
}

func hex4(b []byte) rune {
	var r rune
	for _, c := range b[:4] {
		switch {
		case c >= '0' && c <= '9':
			c -= '0'
		case c >= 'a' && c <= 'f':
			c = c - 'a' + 10
		default:
			c = c - 'A' + 10
		}
		r = r<<4 | rune(c)
	}
	return r
}

// Writes the decoded text of validated raw string content, replacing invalid UTF-8 and lone surrogates
func writeUnquoted(sb *strings.Builder, raw []byte) {
	start := 0
	for i := 0; i < len(raw); {
		for i+8 <= len(raw) && !escapeOrHigh(binary.LittleEndian.Uint64(raw[i:])) {
			i += 8
		}
		if i >= len(raw) {
			break
		}
		c := raw[i]
		if c != '\\' && c < utf8.RuneSelf {
			i++
			continue
		}
		if c != '\\' {
			r, size := utf8.DecodeRune(raw[i:])
			if r == utf8.RuneError && size == 1 {
				sb.Write(raw[start:i])
				sb.WriteRune(utf8.RuneError)
				i++
				start = i
				continue
			}
			i += size
			continue
		}
		sb.Write(raw[start:i])
		switch raw[i+1] {
		case 'b':
			sb.WriteByte('\b')
		case 'f':
			sb.WriteByte('\f')
		case 'n':
			sb.WriteByte('\n')
		case 'r':
			sb.WriteByte('\r')
		case 't':
			sb.WriteByte('\t')
		case 'u':
			r := hex4(raw[i+2:])
			i += 6
			if utf16.IsSurrogate(r) {
				if i+6 <= len(raw) && raw[i] == '\\' && raw[i+1] == 'u' {
					if dec := utf16.DecodeRune(r, hex4(raw[i+2:])); dec != utf8.RuneError {
						sb.WriteRune(dec)
						i += 6
						start = i
						continue
					}
				}
				r = utf8.RuneError
			}
			sb.WriteRune(r)
			start = i
			continue
		default:
			sb.WriteByte(raw[i+1])
		}
		i += 2
		start = i
	}
	sb.Write(raw[start:])
}

// Decodes a validated JSON string literal including its quotes
func unquote(lit []byte) string {
	raw := lit[1 : len(lit)-1]
	var sb strings.Builder
	sb.Grow(len(raw))
	writeUnquoted(&sb, raw)
	return sb.String()
}

const hexDigits = "0123456789abcdef"

// Appends s as a JSON string literal, escaping control bytes, invalid UTF-8 and line separators
func appendQuoted(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch c {
			case '"', '\\':
				dst = append(dst, '\\', c)
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, `\ufffd`...)
			i++
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&0xf])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}
