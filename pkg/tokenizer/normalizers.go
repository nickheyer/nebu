package tokenizer

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Where a segment starts in the whole input and whether its first character still sits at offset zero
type normState struct {
	offset     int
	originZero bool
}

// Marks the leading character as dropped when the predicate accepts the first rune
func (st *normState) dropsFirst(s string, accept func(rune) bool) {
	if s != "" && accept(firstRune(s)) {
		st.originZero = false
	}
}

// Rewrites a text segment, recording on the state when it drops the segment's first character
type normalizer interface {
	normalize(s string, st *normState) string
}

type normalizerSequence []normalizer

func (n normalizerSequence) normalize(s string, st *normState) string {
	for _, m := range n {
		s = m.normalize(s, st)
	}
	return s
}

type unicodeForm struct{ form norm.Form }

func (n unicodeForm) normalize(s string, _ *normState) string { return n.form.String(s) }

type lowercase struct{}

// Lowercases each rune, capital I with dot above expanding to i and a combining dot as Rust does
func (lowercase) normalize(s string, _ *normState) string {
	var b strings.Builder
	for _, r := range s {
		if r == 0x130 {
			b.WriteString("i̇")
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

type strip struct{ left, right bool }

func (n strip) normalize(s string, st *normState) string {
	if n.left {
		st.dropsFirst(s, unicode.IsSpace)
		s = strings.TrimLeftFunc(s, unicode.IsSpace)
	}
	if n.right {
		s = strings.TrimRightFunc(s, unicode.IsSpace)
	}
	return s
}

type stripAccents struct{}

// Drops every combining mark
func (stripAccents) normalize(s string, st *normState) string {
	st.dropsFirst(s, unicode.IsMark)
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.M, r) {
			return -1
		}
		return r
	}, s)
}

type prepend struct{ text string }

func (n prepend) normalize(s string, _ *normState) string {
	if s == "" {
		return s
	}
	return n.text + s
}

type replace struct {
	literal string
	re      *pattern
	content string
}

func (n replace) normalize(s string, st *normState) string {
	if n.re == nil {
		if n.content == "" && n.literal != "" && strings.HasPrefix(s, n.literal) {
			st.originZero = false
		}
		return strings.ReplaceAll(s, n.literal, n.content)
	}
	matches := n.re.findAll(s)
	if len(matches) == 0 {
		return s
	}
	if n.content == "" && matches[0][0] == 0 {
		st.originZero = false
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		b.WriteString(n.content)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

type byteLevelNormalizer struct{}

func (byteLevelNormalizer) normalize(s string, _ *normState) string { return toByteLevel(s) }

type bertNormalizer struct {
	cleanText, chineseChars, lowercase bool
	stripAccents                       *bool
}

// Whether a rune is a control for BERT cleaning, tab, newline and return excepted
func isBertControl(r rune) bool {
	if r == '\t' || r == '\n' || r == '\r' {
		return false
	}
	return unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r) || unicode.Is(unicode.Cs, r)
}

// Whether BERT text cleaning removes a rune
func bertRemoves(r rune) bool { return r == 0 || r == 0xFFFD || isBertControl(r) }

// Whether a rune is a CJK ideograph BERT surrounds with spaces
func isChineseChar(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF, r >= 0x3400 && r <= 0x4DBF, r >= 0x20000 && r <= 0x2A6DF,
		r >= 0x2A700 && r <= 0x2B73F, r >= 0x2B740 && r <= 0x2B81F, r >= 0x2B920 && r <= 0x2CEAF,
		r >= 0xF900 && r <= 0xFAFF, r >= 0x2F800 && r <= 0x2FA1F:
		return true
	}
	return false
}

// Cleans controls to nothing and whitespace to spaces, pads CJK, then strips accents and lowercases
func (n bertNormalizer) normalize(s string, st *normState) string {
	accents := n.lowercase
	if n.stripAccents != nil {
		accents = *n.stripAccents
	}
	if n.cleanText {
		st.dropsFirst(s, bertRemoves)
	}
	var b strings.Builder
	for _, r := range s {
		if n.cleanText {
			if bertRemoves(r) {
				continue
			}
			if isWhiteSpace(r) {
				r = ' '
			}
		}
		if n.chineseChars && isChineseChar(r) {
			b.WriteByte(' ')
			b.WriteRune(r)
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if accents {
		out = strings.Map(func(r rune) rune {
			if unicode.Is(unicode.Mn, r) {
				return -1
			}
			return r
		}, norm.NFD.String(out))
	}
	if accents {
		st.dropsFirst(out, func(r rune) bool { return unicode.Is(unicode.Mn, r) })
	}
	if n.lowercase {
		out = lowercase{}.normalize(out, st)
	}
	return out
}

type nmt struct{}

// Whether the NMT normalizer drops a rune
func nmtRemoves(r rune) bool {
	return (r >= 1 && r <= 8) || r == 0x0B || (r >= 0x0E && r <= 0x1F) || r == 0x7F || r == 0x8F || r == 0x9F
}

// Drops the control code points SentencePiece NMT drops and maps its separators to spaces
func (nmt) normalize(s string, st *normState) string {
	st.dropsFirst(s, nmtRemoves)
	return strings.Map(func(r rune) rune {
		switch {
		case nmtRemoves(r):
			return -1
		case r == 0x09, r == 0x0A, r == 0x0C, r == 0x0D, r == 0x1680, r >= 0x200B && r <= 0x200F,
			r == 0x2028, r == 0x2029, r == 0x2581, r == 0xFEFF, r == 0xFFFD:
			return ' '
		}
		return r
	}, s)
}

// The metaspace rewrite as a normalizer, the form the 2.0 file format spells it in
type metaspaceNormalizer struct {
	replacement    rune
	prepend        string
	dropWhitespace bool
}

func (n metaspaceNormalizer) normalize(s string, st *normState) string {
	if s == "" {
		return s
	}
	offset := st.offset
	if !st.originZero {
		offset = st.offset + 1
	}
	var b strings.Builder
	if n.dropWhitespace {
		opens := offset == 0 && !isWhiteSpace(firstRune(s))
		for _, word := range strings.FieldsFunc(s, isWhiteSpace) {
			pre := n.prepend == "always" || (n.prepend == "first" && opens)
			if pre && !strings.HasPrefix(word, string(n.replacement)) {
				b.WriteRune(n.replacement)
			}
			opens = false
			b.WriteString(word)
		}
		return b.String()
	}
	pre := n.prepend == "always" || (n.prepend == "first" && offset == 0)
	if pre && !strings.HasPrefix(s, " ") && !strings.HasPrefix(s, string(n.replacement)) {
		b.WriteRune(n.replacement)
	}
	b.WriteString(strings.ReplaceAll(s, " ", string(n.replacement)))
	return b.String()
}

// The first rune of a string, the replacement character for an empty one
func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}
