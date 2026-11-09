package tokenizer

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The members of the Unicode White_Space property as a character class body
const whiteSpaceClass = `\t\n\x0B\f\r \x{85}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}`

// The Rust regex word class, alphabetic, mark, decimal digit, connector punctuation and join controls
var wordClass = `\p{L}\p{M}\p{Nd}\p{Nl}\p{Pc}\x{200C}\x{200D}` + rangesOf(unicode.Other_Alphabetic)

// The code points of a range table spelled as a character class body
func rangesOf(t *unicode.RangeTable) string {
	var b strings.Builder
	for _, r := range t.R16 {
		for lo := rune(r.Lo); lo <= rune(r.Hi); lo += rune(r.Stride) {
			fmt.Fprintf(&b, `\x{%X}`, lo)
		}
	}
	for _, r := range t.R32 {
		for lo := rune(r.Lo); lo <= rune(r.Hi); lo += rune(r.Stride) {
			fmt.Fprintf(&b, `\x{%X}`, lo)
		}
	}
	return b.String()
}

// Whether a rune has the Unicode White_Space property
func isWhiteSpace(r rune) bool {
	return unicode.IsSpace(r) || r == 0x85
}

// A Rust flavored regex split into its top level alternatives, a trailing (?!\S) handled natively
type pattern struct {
	src  string
	alts []*alternative
	// The whole pattern without lookaheads, used for the fast scan when every alternative compiles
	whole *regexp.Regexp
	// The index of the alternative whose trailing lookahead refuses following non whitespace, -1 for none
	lookahead int
}

type alternative struct {
	re        *regexp.Regexp
	lookahead bool
}

// Compiles a pattern, refusing constructs outside the trailing not followed by non whitespace lookahead
func compilePattern(src string) (*pattern, error) {
	parts, err := splitAlternatives(src)
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %w", src, err)
	}
	p := &pattern{src: src, lookahead: -1}
	var stripped []string
	for i, part := range parts {
		look := false
		if strings.HasSuffix(part, `(?!\S)`) {
			part = strings.TrimSuffix(part, `(?!\S)`)
			look = true
			if p.lookahead >= 0 {
				return nil, fmt.Errorf("pattern %q: two alternatives end in a lookahead", src)
			}
			p.lookahead = i
		}
		if strings.Contains(part, "(?!") || strings.Contains(part, "(?=") || strings.Contains(part, "(?<") {
			return nil, fmt.Errorf("pattern %q: lookaround is only supported as a trailing (?!\\S)", src)
		}
		translated, err := translateClasses(part)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", src, err)
		}
		re, err := regexp.Compile(`^(?:` + translated + `)`)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", src, err)
		}
		p.alts = append(p.alts, &alternative{re: re, lookahead: look})
		stripped = append(stripped, "(?:"+translated+")")
	}
	if p.whole, err = regexp.Compile(strings.Join(stripped, "|")); err != nil {
		return nil, fmt.Errorf("pattern %q: %w", src, err)
	}
	return p, nil
}

// Splits a pattern on its top level bars, honoring escapes, groups and bracket classes
func splitAlternatives(src string) ([]string, error) {
	var parts []string
	depth, inClass, classStart := 0, false, 0
	last := 0
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '\\':
			i++
			if i >= len(src) {
				return nil, errors.New("trailing backslash")
			}
		case inClass:
			if c == '[' && i+1 < len(src) && src[i+1] == ':' {
				end := strings.Index(src[i:], ":]")
				if end > 0 {
					i += end + 1
				}
			} else if c == ']' && i > classStart {
				inClass = false
			}
		case c == '[':
			inClass = true
			classStart = i + 1
			if classStart < len(src) && src[classStart] == '^' {
				classStart++
			}
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth < 0 {
				return nil, errors.New("unbalanced parenthesis")
			}
		case c == '|' && depth == 0:
			parts = append(parts, src[last:i])
			last = i + 1
		}
	}
	if inClass || depth != 0 {
		return nil, errors.New("unbalanced class or group")
	}
	return append(parts, src[last:]), nil
}

// Rewrites the Perl classes to their Unicode meanings, refusing negated ones inside a bracket class
func translateClasses(src string) (string, error) {
	var b strings.Builder
	inClass := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == '\\' && i+1 < len(src) {
			n := src[i+1]
			i++
			switch {
			case n == 's' && inClass:
				b.WriteString(whiteSpaceClass)
			case n == 's':
				b.WriteString("[" + whiteSpaceClass + "]")
			case n == 'S' && inClass:
				return "", errors.New(`\S inside a bracket class is not supported`)
			case n == 'S':
				b.WriteString("[^" + whiteSpaceClass + "]")
			case n == 'w' && inClass:
				b.WriteString(wordClass)
			case n == 'w':
				b.WriteString("[" + wordClass + "]")
			case n == 'W' && inClass:
				return "", errors.New(`\W inside a bracket class is not supported`)
			case n == 'W':
				b.WriteString("[^" + wordClass + "]")
			case n == 'd' && inClass:
				b.WriteString(`\p{Nd}`)
			case n == 'd':
				b.WriteString(`\p{Nd}`)
			case n == 'D' && inClass:
				return "", errors.New(`\D inside a bracket class is not supported`)
			case n == 'D':
				b.WriteString(`\P{Nd}`)
			default:
				b.WriteByte('\\')
				b.WriteByte(n)
			}
			continue
		}
		if c == '[' && !inClass {
			inClass = true
		} else if c == ']' && inClass {
			inClass = false
		}
		b.WriteByte(c)
	}
	return b.String(), nil
}

// Every non overlapping match in leftmost first order, whitespace matches re-tried alternative by alternative
func (p *pattern) findAll(text string) [][2]int {
	var out [][2]int
	pos := 0
	for pos < len(text) {
		loc := p.whole.FindStringIndex(text[pos:])
		if loc == nil {
			break
		}
		start, end := pos+loc[0], pos+loc[1]
		if start == end {
			_, n := utf8.DecodeRuneInString(text[start:])
			pos = start + n
			continue
		}
		if p.lookahead >= 0 && allWhiteSpace(text[start:end]) {
			start, end = p.matchAt(text, start)
			if end <= start {
				_, n := utf8.DecodeRuneInString(text[start:])
				pos = start + n
				continue
			}
		}
		out = append(out, [2]int{start, end})
		pos = end
	}
	return out
}

// The match at a position by trying alternatives in order, the lookahead one backing off a final space
func (p *pattern) matchAt(text string, pos int) (int, int) {
	for _, alt := range p.alts {
		loc := alt.re.FindStringIndex(text[pos:])
		if loc == nil || loc[1] == 0 {
			continue
		}
		end := pos + loc[1]
		if alt.lookahead && end < len(text) {
			if next, _ := utf8.DecodeRuneInString(text[end:]); !isWhiteSpace(next) {
				_, n := utf8.DecodeLastRuneInString(text[pos:end])
				end -= n
				if end == pos {
					continue
				}
			}
		}
		return pos, end
	}
	return pos, pos
}

// Whether every rune of the text is white space
func allWhiteSpace(s string) bool {
	for _, r := range s {
		if !isWhiteSpace(r) {
			return false
		}
	}
	return true
}
