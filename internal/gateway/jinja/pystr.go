package jinja

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/runenames"
)

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// A rune as UTF-8, or as WTF-8 bytes for a lone surrogate Python strings can hold
func encodeRune(r rune) string {
	if r >= 0xd800 && r <= 0xdfff {
		return string([]byte{0xed, byte(0xa0 | (r>>6)&0x1f), byte(0x80 | r&0x3f)})
	}
	return string(r)
}

// The rune a Unicode character name names, matched without regard to case as Python's codec does
func unicodeByName(name string) (rune, bool) {
	upper := strings.ToUpper(name)
	if r, ok := nameAliases[upper]; ok {
		return r, true
	}
	for prefix, ranges := range algorithmicNames {
		if !strings.HasPrefix(upper, prefix) {
			continue
		}
		hex := upper[len(prefix):]
		if len(hex) != 4 && len(hex) != 5 || strings.Trim(hex, "0123456789ABCDEF") != "" {
			return 0, false
		}
		v, _ := strconv.ParseUint(hex, 16, 32)
		if inRanges(ranges, rune(v)) {
			return rune(v), true
		}
		return 0, false
	}
	if strings.HasPrefix(upper, "HANGUL SYLLABLE ") {
		return hangulByName(upper[len("HANGUL SYLLABLE "):])
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r == 0xd800 {
			r = 0xe000
		}
		if runenames.Name(r) == upper {
			return r, true
		}
	}
	return 0, false
}

// Composes a Hangul syllable from jamo short names, taking the longest match at each step as CPython does
func hangulByName(syl string) (rune, bool) {
	leads := []string{"G", "GG", "N", "D", "DD", "R", "M", "B", "BB", "S", "SS", "", "J", "JJ", "C", "K", "T", "P", "H"}
	vowels := []string{"A", "AE", "YA", "YAE", "EO", "E", "YEO", "YE", "O", "WA", "WAE", "OE", "YO", "U", "WEO", "WE", "WI", "YU", "EU", "YI", "I"}
	tails := []string{"", "G", "GG", "GS", "N", "NJ", "NH", "D", "L", "LG", "LM", "LB", "LS", "LT", "LP", "LH", "M", "B", "BS", "S", "SS", "NG", "J", "C", "K", "T", "P", "H"}
	longest := func(s string, names []string) (int, int) {
		best, size := -1, -1
		for i, n := range names {
			if len(n) > size && strings.HasPrefix(s, n) {
				best, size = i, len(n)
			}
		}
		return best, max(size, 0)
	}
	l, n := longest(syl, leads)
	syl = syl[n:]
	v, n := longest(syl, vowels)
	syl = syl[n:]
	t, n := longest(syl, tails)
	if l < 0 || v < 0 || t < 0 || n != len(syl) {
		return 0, false
	}
	return rune(0xac00 + (l*21+v)*28 + t), true
}

// Python's repr of a string, choosing quotes and escaping as CPython does
func strRepr(s string) string {
	quote := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			r = rune(s[i])
			if sr, ok := wtf8Surrogate(s[i:]); ok {
				r, size = sr, 3
			}
		}
		i += size
		switch {
		case r == rune(quote) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < ' ' || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case isPrintable(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// Decodes a WTF-8 encoded lone surrogate
func wtf8Surrogate(s string) (rune, bool) {
	if len(s) < 3 || s[0] != 0xed || s[1]&0xe0 != 0xa0 || s[2]&0xc0 != 0x80 {
		return 0, false
	}
	return 0xd000 | rune(s[1]&0x3f)<<6 | rune(s[2]&0x3f), true
}

// Python's str.isprintable for one rune
func isPrintable(r rune) bool {
	if r == ' ' {
		return true
	}
	if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Cs, unicode.Co, unicode.Zl, unicode.Zp, unicode.Zs) {
		return false
	}
	return unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S)
}

// Python's cased property, letters with case or the other lowercase and uppercase properties
func isCased(r rune) bool {
	return unicode.In(r, unicode.Lu, unicode.Ll, unicode.Lt, unicode.Other_Lowercase, unicode.Other_Uppercase)
}

// Python's case ignorable property
func isCaseIgnorable(r rune) bool {
	switch r {
	case '\'', '.', ':', '^', '`', 0xa8, 0xad, 0xaf, 0xb4, 0xb7, 0xb8, 0x387, 0x55f, 0x5f4, 0x2018, 0x2019, 0x2024, 0x2027, 0xfe13, 0xfe52, 0xfe55, 0xff07, 0xff0e, 0xff1a:
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}

func upperRune(r rune) string {
	if s, ok := upperSpecial[r]; ok {
		return s
	}
	return string(unicode.ToUpper(r))
}

func titleRune(r rune) string {
	if s, ok := titleSpecial[r]; ok {
		return s
	}
	return string(unicode.ToTitle(r))
}

// The lowercase of the rune at i, applying Python's final sigma rule
func lowerAt(rs []rune, i int) string {
	r := rs[i]
	if r == 0x3a3 {
		return string(finalSigma(rs, i))
	}
	if s, ok := lowerSpecial[r]; ok {
		return s
	}
	return string(unicode.ToLower(r))
}

func finalSigma(rs []rune, i int) rune {
	j := i - 1
	for j >= 0 && isCaseIgnorable(rs[j]) {
		j--
	}
	final := j >= 0 && isCased(rs[j])
	if final && i+1 < len(rs) {
		j = i + 1
		for j < len(rs) && isCaseIgnorable(rs[j]) {
			j++
		}
		final = j == len(rs) || !isCased(rs[j])
	}
	if final {
		return 0x3c2
	}
	return 0x3c3
}

func pyUpper(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteString(upperRune(r))
	}
	return b.String()
}

func pyLower(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i := range rs {
		b.WriteString(lowerAt(rs, i))
	}
	return b.String()
}

func pyCasefold(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		if f, ok := foldSpecial[r]; ok {
			b.WriteString(f)
			continue
		}
		b.WriteString(lowerAt(rs, i))
	}
	return b.String()
}

func pySwapcase(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		switch {
		case isUpperRune(r):
			b.WriteString(lowerAt(rs, i))
		case isLowerRune(r):
			b.WriteString(upperRune(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Titlecases each rune after an uncased one and lowercases the rest, as str.title does
func pyTitle(s string) string {
	rs := []rune(s)
	var b strings.Builder
	prevCased := false
	for i, r := range rs {
		if prevCased {
			b.WriteString(lowerAt(rs, i))
		} else {
			b.WriteString(titleRune(r))
		}
		prevCased = isCased(r)
	}
	return b.String()
}

// Titlecases the first rune and lowercases the rest, as str.capitalize does
func pyCapitalize(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		if i == 0 {
			b.WriteString(titleRune(r))
			continue
		}
		b.WriteString(lowerAt(rs, i))
	}
	return b.String()
}

// Strips whitespace, or any of the given characters, from the chosen ends
func pyStrip(s string, chars *string, left, right bool) string {
	in := isPySpace
	if chars != nil {
		set := *chars
		in = func(r rune) bool { return strings.ContainsRune(set, r) }
	}
	if left {
		s = strings.TrimLeftFunc(s, in)
	}
	if right {
		s = strings.TrimRightFunc(s, in)
	}
	return s
}

// Splits on runs of whitespace or on a separator, from the left, up to maxsplit times
func pySplit(s string, sep *string, maxsplit int) ([]string, error) {
	if sep == nil {
		var out []string
		rs := []rune(s)
		i := 0
		for {
			for i < len(rs) && isPySpace(rs[i]) {
				i++
			}
			if i >= len(rs) {
				return out, nil
			}
			if maxsplit >= 0 && len(out) == maxsplit {
				out = append(out, string(rs[i:]))
				return out, nil
			}
			j := i
			for j < len(rs) && !isPySpace(rs[j]) {
				j++
			}
			out = append(out, string(rs[i:j]))
			i = j
		}
	}
	if *sep == "" {
		return nil, errorf("ValueError", "empty separator")
	}
	return strings.SplitN(s, *sep, splitLimit(maxsplit)), nil
}

func splitLimit(maxsplit int) int {
	if maxsplit < 0 {
		return -1
	}
	return maxsplit + 1
}

// Splits from the right, the mirror of split
func pyRSplit(s string, sep *string, maxsplit int) ([]string, error) {
	if sep == nil {
		var out []string
		rs := []rune(s)
		i := len(rs)
		for {
			for i > 0 && isPySpace(rs[i-1]) {
				i--
			}
			if i <= 0 {
				break
			}
			if maxsplit >= 0 && len(out) == maxsplit {
				out = append(out, string(rs[:i]))
				break
			}
			j := i
			for j > 0 && !isPySpace(rs[j-1]) {
				j--
			}
			out = append(out, string(rs[j:i]))
			i = j
		}
		reverseStrings(out)
		return out, nil
	}
	if *sep == "" {
		return nil, errorf("ValueError", "empty separator")
	}
	var out []string
	rest := s
	for maxsplit < 0 || len(out) < maxsplit {
		k := strings.LastIndex(rest, *sep)
		if k < 0 {
			break
		}
		out = append(out, rest[k+len(*sep):])
		rest = rest[:k]
	}
	out = append(out, rest)
	reverseStrings(out)
	return out, nil
}

func reverseStrings(s []string) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// Splits at Python's line boundaries, keeping the line ends when asked
func pySplitlines(s string, keepends bool) []string {
	var out []string
	rs := []rune(s)
	start := 0
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch r {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		default:
			continue
		}
		end := i + 1
		if r == '\r' && i+1 < len(rs) && rs[i+1] == '\n' {
			end = i + 2
		}
		if keepends {
			out = append(out, string(rs[start:end]))
		} else {
			out = append(out, string(rs[start:i]))
		}
		start = end
		i = end - 1
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// Python's slice index adjustment for start and end arguments of search methods
func adjustIndices(start, end *int, n int) {
	if *end > n {
		*end = n
	} else if *end < 0 {
		*end += n
		if *end < 0 {
			*end = 0
		}
	}
	if *start < 0 {
		*start += n
		if *start < 0 {
			*start = 0
		}
	}
}

// The rune index of the first or last occurrence of sub between start and end, or -1
func pyFind(s, sub string, start, end int, last bool) int {
	rs := []rune(s)
	adjustIndices(&start, &end, len(rs))
	if start > end || start > len(rs) {
		return -1
	}
	hay := string(rs[start:end])
	var k int
	if last {
		k = strings.LastIndex(hay, sub)
	} else {
		k = strings.Index(hay, sub)
	}
	if k < 0 {
		return -1
	}
	return start + runeLen(hay[:k])
}

// Non overlapping occurrences of sub between start and end
func pyCount(s, sub string, start, end int) int {
	rs := []rune(s)
	adjustIndices(&start, &end, len(rs))
	if start > end || start > len(rs) {
		return 0
	}
	hay := string(rs[start:end])
	if sub == "" {
		return runeLen(hay) + 1
	}
	return strings.Count(hay, sub)
}

// Replaces up to count occurrences, inserting between runes when old is empty
func pyReplace(s, old, new string, count int) string {
	if count < 0 {
		count = -1
	}
	if old == "" {
		rs := []rune(s)
		var b strings.Builder
		n := 0
		for i := 0; i <= len(rs); i++ {
			if count < 0 || n < count {
				b.WriteString(new)
				n++
			}
			if i < len(rs) {
				b.WriteRune(rs[i])
			}
		}
		return b.String()
	}
	return strings.Replace(s, old, new, count)
}

// Pads to width the way str.center places the extra fill rune
func pyCenter(s string, width int, fill rune) string {
	n := runeLen(s)
	if width <= n {
		return s
	}
	marg := width - n
	left := marg/2 + (marg & width & 1)
	return strings.Repeat(string(fill), left) + s + strings.Repeat(string(fill), marg-left)
}

func pyJustify(s string, width int, fill rune, left bool) string {
	n := runeLen(s)
	if width <= n {
		return s
	}
	pad := strings.Repeat(string(fill), width-n)
	if left {
		return s + pad
	}
	return pad + s
}

func pyZfill(s string, width int) string {
	n := runeLen(s)
	if width <= n {
		return s
	}
	pad := strings.Repeat("0", width-n)
	if s != "" && (s[0] == '+' || s[0] == '-') {
		return s[:1] + pad + s[1:]
	}
	return pad + s
}

// The length expandtabs gives a string, counted without building it
func expandedLen(s string, tabsize int) int {
	n, col := 0, 0
	for _, r := range s {
		switch r {
		case '\t':
			if tabsize > 0 {
				pad := tabsize - col%tabsize
				n += pad
				col += pad
			}
		case '\n', '\r':
			n++
			col = 0
		default:
			n++
			col++
		}
		if n > maxValueBytes {
			return n
		}
	}
	return n
}

func pyExpandtabs(s string, tabsize int) string {
	var b strings.Builder
	col := 0
	for _, r := range s {
		switch r {
		case '\t':
			if tabsize > 0 {
				n := tabsize - col%tabsize
				b.WriteString(strings.Repeat(" ", n))
				col += n
			}
		case '\n', '\r':
			b.WriteRune(r)
			col = 0
		default:
			b.WriteRune(r)
			col++
		}
	}
	return b.String()
}

func isDecimalRune(r rune) bool { return unicode.Is(unicode.Nd, r) }

func isDigitRune(r rune) bool { return isDecimalRune(r) || inRanges(digitExtra, r) }

func isNumericRune(r rune) bool { return isDigitRune(r) || inRanges(numericExtra, r) }

func isAlphaRune(r rune) bool {
	return unicode.In(r, unicode.Lu, unicode.Ll, unicode.Lt, unicode.Lm, unicode.Lo)
}

func inRanges(table [][2]rune, r rune) bool {
	i := sort.Search(len(table), func(i int) bool { return table[i][1] >= r })
	return i < len(table) && table[i][0] <= r
}

// Whether every rune passes and the string is not empty
func allRunes(s string, f func(rune) bool) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !f(r) {
			return false
		}
	}
	return true
}

// Python's lowercase property, lowercase letters and the other lowercase property
func isLowerRune(r rune) bool { return unicode.In(r, unicode.Ll, unicode.Other_Lowercase) }

// Python's uppercase property, uppercase letters and the other uppercase property
func isUpperRune(r rune) bool { return unicode.In(r, unicode.Lu, unicode.Other_Uppercase) }

// str.islower and str.isupper, at least one cased rune and none of the other case
func pyIsCase(s string, upper bool) bool {
	cased := false
	for _, r := range s {
		if upper && (isLowerRune(r) || unicode.IsTitle(r)) || !upper && (isUpperRune(r) || unicode.IsTitle(r)) {
			return false
		}
		if upper && isUpperRune(r) || !upper && isLowerRune(r) {
			cased = true
		}
	}
	return cased
}

func pyIsTitle(s string) bool {
	cased, prevCased := false, false
	for _, r := range s {
		switch {
		case isUpperRune(r) || unicode.IsTitle(r):
			if prevCased {
				return false
			}
			prevCased, cased = true, true
		case isLowerRune(r):
			if !prevCased {
				return false
			}
			prevCased, cased = true, true
		default:
			prevCased = false
		}
	}
	return cased
}

// Python escape of text for HTML, markupsafe's escape
func htmlEscape(s string) string {
	return htmlEscaper.Replace(s)
}

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&#34;", "'", "&#39;")
