package jinja

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Python's \w, letters, numeric runes and underscore
func isWordRune(r rune) bool { return r == '_' || isAlphaRune(r) || isNumericRune(r) }

const pyWS = `\t\n\v\f\r \x{1c}\x{1d}\x{1e}\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

var (
	urlHTTPRe  = regexp.MustCompile(`(?i)^((https?://|www\.)(([\p{L}\p{N}_%-]+\.)+)?([a-z]{2,63}|xn--[\p{L}\p{N}_%]{2,59})|([\p{L}\p{N}_%-]{2,63}\.)+(com|net|int|edu|gov|org|info|mil)|(https?://)((([\p{Nd}]{1,3})(\.[\p{Nd}]{1,3}){3})|(\[([\p{Nd}a-f]{0,4}:){2}([\p{Nd}a-f]{0,4}:?){1,6}])))(?::[\p{Nd}]{1,5})?(?:[/?#][^` + pyWS + `]*)?$`)
	urlEmailRe = regexp.MustCompile(`^[^` + pyWS + `]+@[\p{L}\p{N}_][\p{L}\p{N}_.-]*\.[\p{L}\p{N}_]+$`)
	urlSplitRe = regexp.MustCompile(`[` + pyWS + `]+`)
	urlHeadRe  = regexp.MustCompile(`^([(<]|&lt;)+`)
	urlTailRe  = regexp.MustCompile(`([)>.,\n]|&gt;)+$`)
	schemeRe   = regexp.MustCompile(`^([\p{L}\p{N}_.+-]{2,}:(/){0,2})$`)
)

func filterUrlize(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_urlize", v, []param{req("value"), opt("trim_url_limit", none), opt("nofollow", false), opt("target", none), opt("rel", none), opt("extra_schemes", none)}, args, kw)
	if err != nil {
		return nil, err
	}
	relParts := map[string]bool{"noopener": true}
	if truthy(a[4]) {
		relText, err := pyStr(a[4])
		if err != nil {
			return nil, err
		}
		parts, _ := pySplit(relText, nil, -1)
		for _, p := range parts {
			relParts[p] = true
		}
	}
	if truthy(a[2]) {
		relParts["nofollow"] = true
	}
	var rels []string
	for p := range relParts {
		rels = append(rels, p)
	}
	sort.Strings(rels)
	rel := strings.Join(rels, " ")
	var schemes []string
	if _, isNone := a[5].(NoneType); !isNone {
		items, err := iterValues(a[5])
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			s, err := pyStr(it)
			if err != nil {
				return nil, err
			}
			if !schemeRe.MatchString(s) {
				quoted, err := pyRepr(it)
				if err != nil {
					return nil, err
				}
				return nil, errorf("FilterArgumentError", "%s is not a valid URI scheme prefix.", quoted)
			}
			schemes = append(schemes, s)
		}
	}
	var limit *int
	if _, isNone := a[1].(NoneType); !isNone {
		n, err := argInt(a[1])
		if err != nil {
			return nil, err
		}
		limit = &n
	}
	target := ""
	if _, isNone := a[3].(NoneType); !isNone && truthy(a[3]) {
		if target, err = pyStr(a[3]); err != nil {
			return nil, err
		}
	}
	text, err := escapeValue(a[0])
	if err != nil {
		return nil, err
	}
	words := 1
	for _, c := range string(text) {
		if isPySpace(c) {
			words++
		}
	}
	if err := checkBytes(2*len(text) + words*(len(rel)+len(target)+64)); err != nil {
		return nil, err
	}
	out := urlize(string(text), limit, rel, target, schemes)
	if r.autoescape {
		return Markup(out), nil
	}
	return out, nil
}

// jinja2's urlize over already escaped text
func urlize(text string, limit *int, rel, target string, schemes []string) string {
	trim := func(x string) string {
		rs := []rune(x)
		if limit == nil || len(rs) <= *limit {
			return x
		}
		end := *limit
		if end < 0 {
			end = max(len(rs)+end, 0)
		}
		return string(rs[:end]) + "..."
	}
	relAttr := ""
	if rel != "" {
		relAttr = fmt.Sprintf(` rel="%s"`, htmlEscape(rel))
	}
	targetAttr := ""
	if target != "" {
		targetAttr = fmt.Sprintf(` target="%s"`, htmlEscape(target))
	}
	var b strings.Builder
	last := 0
	process := func(word string) string {
		head, middle, tail := "", word, ""
		if m := urlHeadRe.FindString(middle); m != "" {
			head, middle = m, middle[len(m):]
		}
		for _, end := range []string{")", ">", ".", ",", "\n", "&gt;"} {
			if strings.HasSuffix(middle, end) {
				if loc := urlTailRe.FindStringIndex(middle); loc != nil {
					tail, middle = middle[loc[0]:], middle[:loc[0]]
				}
				break
			}
		}
		for _, pair := range [][2]string{{"(", ")"}, {"<", ">"}, {"&lt;", "&gt;"}} {
			startCount := strings.Count(middle, pair[0])
			if startCount <= strings.Count(middle, pair[1]) {
				continue
			}
			for n := min(startCount, strings.Count(tail, pair[1])); n > 0; n-- {
				end := strings.Index(tail, pair[1]) + len(pair[1])
				middle += tail[:end]
				tail = tail[end:]
			}
		}
		switch {
		case urlHTTPRe.MatchString(middle):
			if strings.HasPrefix(middle, "https://") || strings.HasPrefix(middle, "http://") {
				middle = fmt.Sprintf(`<a href="%s"%s%s>%s</a>`, middle, relAttr, targetAttr, trim(middle))
			} else {
				middle = fmt.Sprintf(`<a href="https://%s"%s%s>%s</a>`, middle, relAttr, targetAttr, trim(middle))
			}
		case strings.HasPrefix(middle, "mailto:") && urlEmailRe.MatchString(middle[7:]):
			middle = fmt.Sprintf(`<a href="%s">%s</a>`, middle, middle[7:])
		case strings.Contains(middle, "@") && !strings.HasPrefix(middle, "www.") && !strings.HasPrefix(middle, "@") && !strings.Contains(middle, ":") && urlEmailRe.MatchString(middle):
			middle = fmt.Sprintf(`<a href="mailto:%s">%s</a>`, middle, middle)
		default:
			for _, scheme := range schemes {
				if middle != scheme && strings.HasPrefix(middle, scheme) {
					middle = fmt.Sprintf(`<a href="%s"%s%s>%s</a>`, middle, relAttr, targetAttr, middle)
				}
			}
		}
		return head + middle + tail
	}
	for _, loc := range urlSplitRe.FindAllStringIndex(text, -1) {
		b.WriteString(process(text[last:loc[0]]))
		b.WriteString(text[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(process(text[last:]))
	return b.String()
}

func filterWordwrap(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_wordwrap", v, []param{req("s"), opt("width", int64(79)), opt("break_long_words", true), opt("wrapstring", none), opt("break_on_hyphens", true)}, args, kw)
	if err != nil {
		return nil, err
	}
	s, ok := asStr(a[0])
	if !ok {
		return nil, missingMethod(a[0], "splitlines")
	}
	wrapstring := "\n"
	if _, isNone := a[3].(NoneType); !isNone {
		if wrapstring, err = pyStr(a[3]); err != nil {
			return nil, err
		}
	}
	width, err := argInt(a[1])
	if err != nil {
		return nil, err
	}
	if width <= 0 {
		got, err := pyRepr(a[1])
		if err != nil {
			return nil, err
		}
		return nil, errorf("ValueError", "invalid width %s (must be > 0)", got)
	}
	var out []string
	for _, line := range pySplitlines(s, false) {
		parts, _ := textWrap(line, width, truthy(a[2]), truthy(a[4]))
		if err := checkJoin(parts, wrapstring); err != nil {
			return nil, err
		}
		out = append(out, strings.Join(parts, wrapstring))
	}
	if err := checkJoin(out, wrapstring); err != nil {
		return nil, err
	}
	return strings.Join(out, wrapstring), nil
}

func isWrapSpace(r rune) bool { return strings.ContainsRune("\t\n\v\f\r ", r) }

func isWordPunct(r rune) bool { return isWordRune(r) || strings.ContainsRune(`!"'&.,?`, r) }

func isWrapLetter(r rune) bool { return isWordRune(r) && !isDecimalRune(r) }

// Splits text into chunks as textwrap's wordsep_re or wordsep_simple_re splits it
func wrapChunks(text string, hyphens bool) []string {
	rs := []rune(text)
	at := func(i int) rune {
		if i < 0 || i >= len(rs) {
			return -1
		}
		return rs[i]
	}
	var chunks []string
	for p := 0; p < len(rs); {
		if isWrapSpace(rs[p]) {
			e := p
			for e < len(rs) && isWrapSpace(rs[e]) {
				e++
			}
			chunks = append(chunks, string(rs[p:e]))
			p = e
			continue
		}
		if !hyphens {
			e := p
			for e < len(rs) && !isWrapSpace(rs[e]) {
				e++
			}
			chunks = append(chunks, string(rs[p:e]))
			p = e
			continue
		}
		dashes := func(i int) int {
			n := 0
			for at(i+n) == '-' {
				n++
			}
			return n
		}
		if rs[p] == '-' && p > 0 && isWordPunct(rs[p-1]) {
			if n := dashes(p); n >= 2 && at(p+n) >= 0 && isWordRune(at(p+n)) {
				chunks = append(chunks, string(rs[p:p+n]))
				p += n
				continue
			}
		}
		end := -1
		for e := p + 1; e <= len(rs); e++ {
			if at(e) == '-' {
				behind := isWrapLetter(at(e-2)) && isWrapLetter(at(e-1)) || isWrapLetter(at(e-3)) && at(e-2) == '-' && isWrapLetter(at(e-1))
				ahead := isWrapLetter(at(e+1)) && (isWrapLetter(at(e+2)) || at(e+2) == '-' && isWrapLetter(at(e+3)))
				if behind && ahead && at(e+1) >= 0 {
					end = e + 1
					break
				}
			}
			if e == len(rs) || isWrapSpace(rs[e]) {
				end = e
				break
			}
			if isWordPunct(rs[e-1]) {
				if n := dashes(e); n >= 2 && at(e+n) >= 0 && isWordRune(at(e+n)) {
					end = e
					break
				}
			}
		}
		chunks = append(chunks, string(rs[p:end]))
		p = end
	}
	return chunks
}

// textwrap.wrap with jinja2's options, dropping whitespace and never expanding tabs
// textwrap.wrap's lines, and whether _handle_long_word scanned a chunk for a hyphen in a generator expression
func textWrap(text string, width int, breakLong, hyphens bool) ([]string, bool) {
	scanned := false
	chunks := wrapChunks(text, hyphens)
	for i, j := 0, len(chunks)-1; i < j; i, j = i+1, j-1 {
		chunks[i], chunks[j] = chunks[j], chunks[i]
	}
	blank := func(s string) bool { return strings.TrimFunc(s, isPySpace) == "" }
	var lines []string
	for len(chunks) > 0 {
		var cur []string
		curLen := 0
		if blank(chunks[len(chunks)-1]) && len(lines) > 0 {
			chunks = chunks[:len(chunks)-1]
		}
		for len(chunks) > 0 {
			l := runeLen(chunks[len(chunks)-1])
			if curLen+l > width {
				break
			}
			cur = append(cur, chunks[len(chunks)-1])
			chunks = chunks[:len(chunks)-1]
			curLen += l
		}
		if len(chunks) > 0 && runeLen(chunks[len(chunks)-1]) > width {
			spaceLeft := width - curLen
			if width < 1 {
				spaceLeft = 1
			}
			if breakLong {
				chunk := []rune(chunks[len(chunks)-1])
				end := spaceLeft
				if hyphens && len(chunk) > spaceLeft {
					hyphen := strings.LastIndex(string(chunk[:spaceLeft]), "-")
					if hyphen > 0 {
						scanned = true
						hyphen = runeLen(string(chunk[:spaceLeft])[:hyphen])
						if strings.Trim(string(chunk[:hyphen]), "-") != "" {
							end = hyphen + 1
						}
					}
				}
				cur = append(cur, string(chunk[:end]))
				chunks[len(chunks)-1] = string(chunk[end:])
			} else if len(cur) == 0 {
				cur = append(cur, chunks[len(chunks)-1])
				chunks = chunks[:len(chunks)-1]
			}
		}
		if len(cur) > 0 && blank(cur[len(cur)-1]) {
			cur = cur[:len(cur)-1]
		}
		if len(cur) > 0 {
			lines = append(lines, strings.Join(cur, ""))
		}
	}
	return lines, scanned
}

func filterPprint(r *renderer, v Value, args []Value, kw *Dict) (Value, error) {
	a, err := filterArgs("do_pprint", v, []param{req("value")}, args, kw)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	pp := &prettyPrinter{width: 80}
	if err := pp.format(&b, a[0], 0, 0, 0); err != nil {
		return nil, err
	}
	return b.String(), nil
}

// pprint.PrettyPrinter with its defaults, indent 1, width 80 and sorted dicts
type prettyPrinter struct{ width int }

// The repr pprint uses, sorting dict items
func (pp *prettyPrinter) repr(v Value) (string, error) {
	switch x := v.(type) {
	case *Dict:
		parts := make([]string, 0, 2*len(x.keys))
		for _, it := range sortedItems(x) {
			for _, kv := range it {
				s, err := pp.repr(kv)
				if err != nil {
					return "", err
				}
				parts = append(parts, s)
			}
		}
		if err := checkJoin(parts, ", "); err != nil {
			return "", err
		}
		var b strings.Builder
		for i := 0; i < len(parts); i += 2 {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(parts[i] + ": " + parts[i+1])
		}
		return "{" + b.String() + "}", nil
	case *List:
		parts, err := pp.reprs(x.items)
		if err == nil {
			err = checkJoin(parts, ", ")
		}
		return "[" + strings.Join(parts, ", ") + "]", err
	case *Tuple:
		if x.names != nil {
			return pyRepr(x)
		}
		parts, err := pp.reprs(x.items)
		if err == nil {
			err = checkJoin(parts, ", ")
		}
		if len(parts) == 1 {
			return "(" + parts[0] + ",)", err
		}
		return "(" + strings.Join(parts, ", ") + ")", err
	}
	return pyRepr(v)
}

func (pp *prettyPrinter) reprs(items []Value) ([]string, error) {
	parts := make([]string, len(items))
	for i, it := range items {
		s, err := pp.repr(it)
		if err != nil {
			return nil, err
		}
		parts[i] = s
	}
	return parts, nil
}

// Dict items ordered as pprint's _safe_tuple sorts them, type names breaking incomparable ties
func sortedItems(d *Dict) [][2]Value {
	items := make([][2]Value, len(d.keys))
	for i, k := range d.keys {
		items[i] = [2]Value{k, d.vals[i]}
	}
	safeLess := func(a, b Value) bool {
		lt, err := pyOrder("<", a, b)
		if err != nil {
			return "<class '"+typeName(a)+"'>" < "<class '"+typeName(b)+"'>"
		}
		return lt
	}
	sort.SliceStable(items, func(i, j int) bool {
		if safeLess(items[i][0], items[j][0]) {
			return true
		}
		if safeLess(items[j][0], items[i][0]) {
			return false
		}
		return safeLess(items[i][1], items[j][1])
	})
	return items
}

func (pp *prettyPrinter) format(b *strings.Builder, v Value, indent, allowance, level int) error {
	rep, err := pp.repr(v)
	if err != nil {
		return err
	}
	if utf8.RuneCountInString(rep) <= pp.width-indent-allowance {
		b.WriteString(rep)
		return nil
	}
	switch x := v.(type) {
	case *Dict:
		b.WriteByte('{')
		if len(x.keys) > 0 {
			items := sortedItems(x)
			ind := indent + 1
			for i, it := range items {
				last := i == len(items)-1
				krep, err := pp.repr(it[0])
				if err != nil {
					return err
				}
				b.WriteString(krep)
				b.WriteString(": ")
				al := 1
				if last {
					al = allowance + 1
				}
				if err := pp.format(b, it[1], ind+runeLen(krep)+2, al, level+1); err != nil {
					return err
				}
				if !last {
					b.WriteString(",\n" + strings.Repeat(" ", ind))
				}
			}
		}
		b.WriteByte('}')
		return nil
	case *List:
		b.WriteByte('[')
		if err := pp.items(b, x.items, indent, allowance+1, level+1); err != nil {
			return err
		}
		b.WriteByte(']')
		return nil
	case *Tuple:
		if x.names == nil {
			end := ")"
			if len(x.items) == 1 {
				end = ",)"
			}
			b.WriteByte('(')
			if err := pp.items(b, x.items, indent, allowance+len(end), level+1); err != nil {
				return err
			}
			b.WriteString(end)
			return nil
		}
	case string:
		pp.str(b, x, indent, allowance, level+1)
		return nil
	}
	b.WriteString(rep)
	return nil
}

func (pp *prettyPrinter) items(b *strings.Builder, items []Value, indent, allowance, level int) error {
	indent++
	for i, it := range items {
		if i > 0 {
			b.WriteString(",\n" + strings.Repeat(" ", indent))
		}
		al := 1
		if i == len(items)-1 {
			al = allowance
		}
		if err := pp.format(b, it, indent, al, level); err != nil {
			return err
		}
	}
	return nil
}

var nonSpaceThenSpace = regexp.MustCompile(`[^` + pyWS + `]*[` + pyWS + `]*`)

// pprint's string layout, splitting long lines into adjacent literals at whitespace
func (pp *prettyPrinter) str(b *strings.Builder, s string, indent, allowance, level int) {
	if s == "" {
		b.WriteString(strRepr(s))
		return
	}
	var chunks []string
	lines := pySplitlines(s, true)
	if level == 1 {
		indent++
		allowance++
	}
	maxWidth := pp.width - indent
	maxWidth1 := maxWidth
	var rep string
	for i, line := range lines {
		rep = strRepr(line)
		if i == len(lines)-1 {
			maxWidth1 -= allowance
		}
		if runeLen(rep) <= maxWidth1 {
			chunks = append(chunks, rep)
			continue
		}
		parts := nonSpaceThenSpace.FindAllString(line, -1)
		maxWidth2 := maxWidth
		current := ""
		for j, part := range parts {
			candidate := current + part
			if j == len(parts)-1 && i == len(lines)-1 {
				maxWidth2 -= allowance
			}
			if runeLen(strRepr(candidate)) > maxWidth2 {
				if current != "" {
					chunks = append(chunks, strRepr(current))
				}
				current = part
			} else {
				current = candidate
			}
		}
		if current != "" {
			chunks = append(chunks, strRepr(current))
		}
	}
	if len(chunks) == 1 {
		b.WriteString(rep)
		return
	}
	if level == 1 {
		b.WriteByte('(')
	}
	for i, c := range chunks {
		if i > 0 {
			b.WriteString("\n" + strings.Repeat(" ", indent))
		}
		b.WriteString(c)
	}
	if level == 1 {
		b.WriteByte(')')
	}
}
