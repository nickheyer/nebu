package jinja

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Token kinds the parser reads, named as jinja2 names them
const (
	tokData          = "data"
	tokVariableBegin = "variable_begin"
	tokVariableEnd   = "variable_end"
	tokBlockBegin    = "block_begin"
	tokBlockEnd      = "block_end"
	tokName          = "name"
	tokString        = "string"
	tokInteger       = "integer"
	tokFloat         = "float"
	tokEOF           = "eof"
	// The failure jinja2's lazy lexer raises once the parser reaches this point of the stream
	tokError = "error"
)

// Operator spellings mapped to token kinds, longest spellings first for matching
var operatorList = []struct{ text, kind string }{
	{"//", "floordiv"}, {"**", "pow"}, {"==", "eq"}, {"!=", "ne"}, {">=", "gteq"}, {"<=", "lteq"},
	{"+", "add"}, {"-", "sub"}, {"/", "div"}, {"*", "mul"}, {"%", "mod"}, {"~", "tilde"},
	{"[", "lbracket"}, {"]", "rbracket"}, {"(", "lparen"}, {")", "rparen"}, {"{", "lbrace"},
	{"}", "rbrace"}, {">", "gt"}, {"<", "lt"}, {"=", "assign"}, {".", "dot"}, {":", "colon"},
	{"|", "pipe"}, {",", "comma"}, {";", "semicolon"},
}

type token struct {
	kind  string
	value Value
	line  int
	// Frames above TokenStream.__next__ that producing the token needs, through wrap, tokeniter and their helpers
	peak int
}

// Whether the token is a name with the given text
func (t token) isName(name string) bool {
	return t.kind == tokName && t.value.(string) == name
}

// A jinja2 lexer configured as transformers configures it, trim_blocks and lstrip_blocks on
type lexer struct {
	src         string
	pos         int
	line        int
	toks        []token
	lineStart   bool
	balance     []byte
	newlineSkip int
	// Whether an OptionalLStrip rule matched since the last token, running its generator expression in tokeniter
	stripRule bool
	// Frames above TokenStream.__next__ that raising the lexing error needs
	errPeak int
}

// Frames above TokenStream.__next__ for producing a token, wrap and tokeniter or the Token constructor
const (
	tokenPeak = 2
	// tokeniter's generator expression reading an OptionalLStrip rule's whitespace sign
	stripRulePeak = 3
	// ast.literal_eval and the parse and _convert calls under it
	floatPeak = 3
	// TemplateSyntaxError raised in tokeniter, its two __init__ frames above tokeniter
	tokeniterErrorPeak = 4
	// A Failure rule's __call__ raising TemplateSyntaxError
	failureErrorPeak = 5
	// TemplateSyntaxError raised in wrap
	wrapErrorPeak = 3
)

// Splits template source into tokens as jinja2 does with trim_blocks and lstrip_blocks, ending in eof or the lexing error
func tokenize(source string) []token {
	lines := splitNewlines(source)
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	lx := &lexer{src: strings.Join(lines, "\n"), line: 1, lineStart: true}
	if err := lx.run(); err != nil {
		return append(lx.toks, token{kind: tokError, value: err, line: lx.line, peak: lx.peak(lx.errPeak)})
	}
	return append(lx.toks, token{kind: tokEOF, line: lx.line, peak: lx.peak(tokenPeak)})
}

// The source lines between \r\n, \r and \n breaks
func splitNewlines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			out = append(out, s[start:i])
			start = i + 1
		case '\r':
			out = append(out, s[start:i])
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func (lx *lexer) emit(kind string, value Value) {
	peak := tokenPeak
	if kind == tokFloat {
		peak = floatPeak
	}
	lx.toks = append(lx.toks, token{kind: kind, value: value, line: lx.line, peak: lx.peak(peak)})
}

// The production peak of the next token, raised by an OptionalLStrip match since the last one
func (lx *lexer) peak(own int) int {
	if lx.stripRule {
		lx.stripRule = false
		return max(own, stripRulePeak)
	}
	return own
}

// A TemplateSyntaxError raised at the current line, needing peak frames above TokenStream.__next__ to raise
func (lx *lexer) fail(peak int, format string, args ...any) error {
	lx.errPeak = peak
	return &Error{Kind: "TemplateSyntaxError", Msg: fmt.Sprintf(format, args...), Line: lx.line}
}

// Runs the root state, switching into tag states at each delimiter
func (lx *lexer) run() error {
	for lx.pos < len(lx.src) {
		at, kind, sign, end := lx.nextTag()
		if at < 0 {
			lx.emitData(lx.src[lx.pos:])
			lx.lineStart = strings.HasSuffix(lx.src[lx.pos:], "\n")
			lx.pos = len(lx.src)
			return nil
		}
		text := lx.src[lx.pos:at]
		text = lx.applyLeftStrip(text, sign, kind != tokVariableBegin)
		lx.stripRule = true
		lx.emitData(text)
		lx.line += strings.Count(text, "\n") + lx.newlineSkip
		lx.newlineSkip = 0
		matched := lx.src[at:end]
		lx.lineStart = strings.HasSuffix(matched, "\n")
		lx.pos = end
		var err error
		switch kind {
		case "raw_begin":
			lx.line += strings.Count(matched, "\n")
			err = lx.raw()
		case "comment_begin":
			err = lx.comment()
		case tokBlockBegin:
			lx.emit(tokBlockBegin, matched)
			err = lx.tag(tokBlockEnd)
		case tokVariableBegin:
			lx.emit(tokVariableBegin, matched)
			err = lx.tag(tokVariableEnd)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (lx *lexer) emitData(text string) {
	if text != "" {
		lx.emit(tokData, text)
	}
}

// Strips whitespace before a tag the way jinja2 handles the minus sign and lstrip_blocks
func (lx *lexer) applyLeftStrip(text, sign string, lstrip bool) string {
	if sign == "-" {
		stripped := strings.TrimRightFunc(text, isPySpace)
		lx.newlineSkip = strings.Count(text[len(stripped):], "\n")
		return stripped
	}
	if sign == "+" || !lstrip {
		return text
	}
	lpos := strings.LastIndexByte(text, '\n') + 1
	if lpos > 0 || lx.lineStart {
		rest := text[lpos:]
		if rest != "" && strings.TrimLeftFunc(rest, isPySpace) == "" {
			return text[:lpos]
		}
	}
	return text
}

// The earliest tag start from the current position, its kind, whitespace sign and match end
func (lx *lexer) nextTag() (int, string, string, int) {
	for i := lx.pos; i+1 < len(lx.src); i++ {
		if lx.src[i] != '{' {
			continue
		}
		var kind string
		switch lx.src[i+1] {
		case '{':
			kind = tokVariableBegin
		case '%':
			kind = tokBlockBegin
		case '#':
			kind = "comment_begin"
		default:
			continue
		}
		end := i + 2
		sign := ""
		if end < len(lx.src) && (lx.src[end] == '-' || lx.src[end] == '+') {
			sign = lx.src[end : end+1]
			end++
		}
		if kind == tokBlockBegin {
			if rawEnd, ok := lx.matchRawBegin(end); ok {
				return i, "raw_begin", sign, rawEnd
			}
		}
		return i, kind, sign, end
	}
	return -1, "", "", 0
}

// Matches the rest of a raw opening tag, whitespace raw whitespace then -%} with trailing space or %}
func (lx *lexer) matchRawBegin(p int) (int, bool) {
	p = skipPySpace(lx.src, p)
	if !strings.HasPrefix(lx.src[p:], "raw") {
		return 0, false
	}
	p = skipPySpace(lx.src, p+3)
	if strings.HasPrefix(lx.src[p:], "-%}") {
		return skipPySpace(lx.src, p+3), true
	}
	if strings.HasPrefix(lx.src[p:], "%}") {
		return p + 2, true
	}
	return 0, false
}

// Reads raw text up to the closing endraw tag, an unterminated raw block at the end of the source ending the text
func (lx *lexer) raw() error {
	for i := lx.pos; i+1 < len(lx.src); i++ {
		if lx.src[i] != '{' || lx.src[i+1] != '%' {
			continue
		}
		p := i + 2
		sign := ""
		if p < len(lx.src) && (lx.src[p] == '-' || lx.src[p] == '+') {
			sign = lx.src[p : p+1]
			p++
		}
		p = skipPySpace(lx.src, p)
		if !strings.HasPrefix(lx.src[p:], "endraw") {
			continue
		}
		p = skipPySpace(lx.src, p+6)
		end, ok := matchTagEnd(lx.src, p, "%}", true)
		if !ok {
			continue
		}
		text := lx.applyLeftStrip(lx.src[lx.pos:i], sign, true)
		lx.stripRule = true
		lx.emitData(text)
		lx.line += strings.Count(text, "\n") + lx.newlineSkip + strings.Count(lx.src[i:end], "\n")
		lx.newlineSkip = 0
		lx.lineStart = strings.HasSuffix(lx.src[i:end], "\n")
		lx.pos = end
		return nil
	}
	if lx.pos >= len(lx.src) {
		return nil
	}
	return lx.fail(failureErrorPeak, "Missing end of raw directive")
}

// Skips a comment up to its closing delimiter, an unterminated comment at the end of the source ending the text
func (lx *lexer) comment() error {
	for i := lx.pos; i < len(lx.src); i++ {
		end, ok := matchTagEnd(lx.src, i, "#}", true)
		if !ok {
			continue
		}
		lx.line += strings.Count(lx.src[lx.pos:end], "\n")
		lx.lineStart = strings.HasSuffix(lx.src[lx.pos:end], "\n")
		lx.pos = end
		return nil
	}
	if lx.pos >= len(lx.src) {
		return nil
	}
	return lx.fail(failureErrorPeak, "Missing end of comment tag")
}

// Matches +delim, -delim with trailing whitespace, or delim with one optional newline when trimming
func matchTagEnd(src string, p int, delim string, trim bool) (int, bool) {
	rest := src[p:]
	switch {
	case trim && strings.HasPrefix(rest, "+"+delim):
		return p + 1 + len(delim), true
	case strings.HasPrefix(rest, "-"+delim):
		return skipPySpace(src, p+1+len(delim)), true
	case strings.HasPrefix(rest, delim):
		end := p + len(delim)
		if trim && end < len(src) && src[end] == '\n' {
			end++
		}
		return end, true
	}
	return 0, false
}

// Lexes expression tokens inside a block or variable tag until its closing delimiter
func (lx *lexer) tag(endKind string) error {
	for {
		if lx.pos >= len(lx.src) {
			return nil
		}
		if len(lx.balance) == 0 {
			var end int
			var ok bool
			if endKind == tokBlockEnd {
				end, ok = matchTagEnd(lx.src, lx.pos, "%}", true)
			} else {
				end, ok = matchTagEnd(lx.src, lx.pos, "}}", false)
			}
			if ok {
				matched := lx.src[lx.pos:end]
				lx.emit(endKind, matched)
				lx.line += strings.Count(matched, "\n")
				lx.lineStart = strings.HasSuffix(matched, "\n")
				lx.pos = end
				return nil
			}
		}
		if err := lx.exprToken(); err != nil {
			return err
		}
	}
}

// Lexes one whitespace run, number, name, string or operator in jinja2's rule order
func (lx *lexer) exprToken() error {
	src, p := lx.src, lx.pos
	if end := skipPySpace(src, p); end > p {
		lx.line += strings.Count(src[p:end], "\n")
		lx.pos = end
		return nil
	}
	if end, ok := matchFloat(src, p); ok {
		f, err := strconv.ParseFloat(strings.ReplaceAll(src[p:end], "_", ""), 64)
		if err != nil && !isRangeErr(err) {
			return lx.fail(wrapErrorPeak, "invalid float literal %s", src[p:end])
		}
		lx.emit(tokFloat, f)
		lx.pos = end
		return nil
	}
	if end, ok := matchInteger(src, p); ok {
		n, err := parseIntLiteral(src[p:end])
		if err != nil {
			lx.errPeak = tokenPeak
			return err
		}
		lx.emit(tokInteger, n)
		lx.pos = end
		return nil
	}
	if end := matchName(src, p); end > p {
		name := src[p:end]
		if !isIdentifier(name) {
			return lx.fail(wrapErrorPeak, "Invalid character in identifier")
		}
		lx.emit(tokName, name)
		lx.pos = end
		return nil
	}
	if src[p] == '\'' || src[p] == '"' {
		end, ok := matchString(src, p)
		if ok {
			value, err := unescapeString(src[p+1 : end-1])
			if err != nil {
				return lx.fail(wrapErrorPeak, "%s", err.Error())
			}
			lx.emit(tokString, value)
			lx.line += strings.Count(src[p:end], "\n")
			lx.pos = end
			return nil
		}
	}
	for _, op := range operatorList {
		if !strings.HasPrefix(src[p:], op.text) {
			continue
		}
		switch op.text {
		case "{":
			lx.balance = append(lx.balance, '}')
		case "(":
			lx.balance = append(lx.balance, ')')
		case "[":
			lx.balance = append(lx.balance, ']')
		case "}", ")", "]":
			if len(lx.balance) == 0 {
				return lx.fail(tokeniterErrorPeak, "unexpected '%s'", op.text)
			}
			want := lx.balance[len(lx.balance)-1]
			lx.balance = lx.balance[:len(lx.balance)-1]
			if want != op.text[0] {
				return lx.fail(tokeniterErrorPeak, "unexpected '%s', expected '%c'", op.text, want)
			}
		}
		lx.emit(op.kind, op.text)
		lx.pos += len(op.text)
		return nil
	}
	r, _ := utf8.DecodeRuneInString(src[p:])
	return lx.fail(tokeniterErrorPeak, "unexpected char %s at %d", strRepr(string(r)), p)
}

func isRangeErr(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// Matches jinja2's float pattern, digits with underscores and a fraction or exponent, not after a dot
func matchFloat(s string, p int) (int, bool) {
	if p > 0 && s[p-1] == '.' {
		return 0, false
	}
	q, ok := matchDigitGroups(s, p)
	if !ok {
		return 0, false
	}
	if q < len(s) && s[q] == '.' {
		if r, ok := matchDigitGroups(s, q+1); ok {
			if e, ok := matchExponent(s, r); ok {
				return e, true
			}
			return r, true
		}
	}
	return matchExponent(s, q)
}

func matchExponent(s string, p int) (int, bool) {
	if p >= len(s) || (s[p] != 'e' && s[p] != 'E') {
		return 0, false
	}
	p++
	if p < len(s) && (s[p] == '+' || s[p] == '-') {
		p++
	}
	return matchDigitGroups(s, p)
}

// Matches one or more digits optionally separated by single underscores
func matchDigitGroups(s string, p int) (int, bool) {
	if p >= len(s) || !isASCIIDigit(s[p]) {
		return 0, false
	}
	for p < len(s) {
		if isASCIIDigit(s[p]) {
			p++
			continue
		}
		if s[p] == '_' && p+1 < len(s) && isASCIIDigit(s[p+1]) {
			p++
			continue
		}
		break
	}
	return p, true
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

// Matches jinja2's integer pattern, binary, octal, hex, decimal or zeros with optional underscores
func matchInteger(s string, p int) (int, bool) {
	if p >= len(s) || !isASCIIDigit(s[p]) {
		return 0, false
	}
	if s[p] == '0' && p+1 < len(s) {
		var ok func(byte) bool
		switch s[p+1] {
		case 'b', 'B':
			ok = func(c byte) bool { return c == '0' || c == '1' }
		case 'o', 'O':
			ok = func(c byte) bool { return c >= '0' && c <= '7' }
		case 'x', 'X':
			ok = func(c byte) bool { return isASCIIDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'f') }
		}
		if ok != nil {
			q := p + 2
			n := 0
			for q < len(s) {
				if s[q] == '_' && q+1 < len(s) && ok(s[q+1]) {
					q += 2
					n++
					continue
				}
				if ok(s[q]) {
					q++
					n++
					continue
				}
				break
			}
			if n > 0 {
				return q, true
			}
		}
	}
	if s[p] == '0' {
		q := p + 1
		for q < len(s) {
			if s[q] == '0' {
				q++
			} else if s[q] == '_' && q+1 < len(s) && s[q+1] == '0' {
				q += 2
			} else {
				break
			}
		}
		return q, true
	}
	return matchDigitGroups(s, p)
}

// The value of an integer literal in Python's base prefix rules
func parseIntLiteral(text string) (Value, error) {
	text = strings.ReplaceAll(text, "_", "")
	base := 10
	if len(text) > 1 && text[0] == '0' {
		switch text[1] | 0x20 {
		case 'b':
			base, text = 2, text[2:]
		case 'o':
			base, text = 8, text[2:]
		case 'x':
			base, text = 16, text[2:]
		}
	}
	if base == 10 {
		if err := checkParseDigits(len(text)); err != nil {
			return nil, err
		}
	}
	n, _ := new(big.Int).SetString(text, base)
	return normInt(n), nil
}

// Matches a run of Python word characters
func matchName(s string, p int) int {
	q := p
	for q < len(s) {
		r, size := utf8.DecodeRuneInString(s[q:])
		if r < utf8.RuneSelf && (r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') || r >= utf8.RuneSelf && inRanges(nameRunes, r) {
			q += size
			continue
		}
		break
	}
	return q
}

// Whether a name is a valid Python identifier
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		table := identContinue
		if i == 0 {
			table = identStart
		}
		if r >= utf8.RuneSelf && !inRanges(table, r) || r < utf8.RuneSelf && !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// Matches a quoted string literal with backslash escapes, returning the end after the closing quote
func matchString(s string, p int) (int, bool) {
	q := s[p]
	for i := p + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case q:
			return i + 1, true
		}
	}
	return 0, false
}

// Decodes a literal body as Python's backslashreplace then unicode-escape decoding does
func unescapeString(body string) (string, error) {
	var ascii strings.Builder
	for _, r := range body {
		switch {
		case r < 0x80:
			ascii.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&ascii, "\\x%02x", r)
		case r <= 0xffff:
			fmt.Fprintf(&ascii, "\\u%04x", r)
		default:
			fmt.Fprintf(&ascii, "\\U%08x", r)
		}
	}
	src := ascii.String()
	var out strings.Builder
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c != '\\' {
			out.WriteByte(c)
			continue
		}
		if i+1 >= len(src) {
			return "", fmt.Errorf("\\ at end of string")
		}
		i++
		switch e := src[i]; e {
		case '\n':
		case '\\', '\'', '"':
			out.WriteByte(e)
		case 'a':
			out.WriteByte('\a')
		case 'b':
			out.WriteByte('\b')
		case 'f':
			out.WriteByte('\f')
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		case 'v':
			out.WriteByte('\v')
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v := 0
			n := 0
			for n < 3 && i < len(src) && src[i] >= '0' && src[i] <= '7' {
				v = v*8 + int(src[i]-'0')
				i++
				n++
			}
			i--
			out.WriteString(encodeRune(rune(v)))
		case 'x', 'u', 'U':
			width := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
			if i+1+width > len(src) {
				return "", fmt.Errorf("truncated \\%c%s escape", e, strings.Repeat("X", width))
			}
			v, err := strconv.ParseUint(src[i+1:i+1+width], 16, 32)
			if err != nil || strings.ContainsAny(src[i+1:i+1+width], "+-_") {
				return "", fmt.Errorf("truncated \\%c%s escape", e, strings.Repeat("X", width))
			}
			if v > unicode.MaxRune {
				return "", fmt.Errorf("illegal Unicode character")
			}
			out.WriteString(encodeRune(rune(v)))
			i += width
		case 'N':
			end := strings.IndexByte(src[i:], '}')
			if i+1 >= len(src) || src[i+1] != '{' || end < 0 {
				return "", fmt.Errorf("malformed \\N character escape")
			}
			r, ok := unicodeByName(src[i+2 : i+end])
			if !ok {
				return "", fmt.Errorf("unknown Unicode character name")
			}
			out.WriteRune(r)
			i += end
		default:
			out.WriteByte('\\')
			out.WriteByte(e)
		}
	}
	return out.String(), nil
}

// Python whitespace as str.isspace defines it
func isPySpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, 0x85:
		return true
	}
	if r < 0x80 {
		return false
	}
	return unicode.Is(unicode.White_Space, r) || unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp)
}

func skipPySpace(s string, p int) int {
	for p < len(s) {
		r, size := utf8.DecodeRuneInString(s[p:])
		if !isPySpace(r) {
			break
		}
		p += size
	}
	return p
}
