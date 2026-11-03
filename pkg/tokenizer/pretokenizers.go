package tokenizer

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// A run of text the model tokenizes on its own, start being where it began in the whole input
type piece struct {
	text  string
	start int
}

// Cuts a normalized segment into the pieces the model tokenizes one by one
type preTokenizer interface {
	preTokenize(p piece) []piece
}

// How the matched delimiters of a split join the pieces around them
const (
	behaviorRemoved            = "Removed"
	behaviorIsolated           = "Isolated"
	behaviorMergedWithPrevious = "MergedWithPrevious"
	behaviorMergedWithNext     = "MergedWithNext"
	behaviorContiguous         = "Contiguous"
)

type span struct {
	start, end int
	match      bool
}

// Covers the whole text with the matches and the gaps between them, in order
func cover(text string, matches [][2]int, invert bool) []span {
	var out []span
	last := 0
	for _, m := range matches {
		if m[0] > last {
			out = append(out, span{last, m[0], invert})
		}
		out = append(out, span{m[0], m[1], !invert})
		last = m[1]
	}
	if last < len(text) {
		out = append(out, span{last, len(text), invert})
	}
	return out
}

// Joins covered spans into pieces as the behavior says, dropped matches leaving no piece
func joinSpans(p piece, spans []span, behavior string) []piece {
	var ranges [][2]int
	switch behavior {
	case behaviorRemoved:
		for _, s := range spans {
			if !s.match {
				ranges = append(ranges, [2]int{s.start, s.end})
			}
		}
	case behaviorIsolated:
		for _, s := range spans {
			ranges = append(ranges, [2]int{s.start, s.end})
		}
	case behaviorMergedWithPrevious:
		for _, s := range spans {
			if s.match && len(ranges) > 0 {
				ranges[len(ranges)-1][1] = s.end
				continue
			}
			ranges = append(ranges, [2]int{s.start, s.end})
		}
	case behaviorMergedWithNext:
		for i := len(spans) - 1; i >= 0; i-- {
			s := spans[i]
			if s.match && len(ranges) > 0 {
				ranges[len(ranges)-1][0] = s.start
				continue
			}
			ranges = append(ranges, [2]int{s.start, s.end})
		}
		for i, j := 0, len(ranges)-1; i < j; i, j = i+1, j-1 {
			ranges[i], ranges[j] = ranges[j], ranges[i]
		}
	case behaviorContiguous:
		for i, s := range spans {
			if i > 0 && spans[i-1].match == s.match {
				ranges[len(ranges)-1][1] = s.end
				continue
			}
			ranges = append(ranges, [2]int{s.start, s.end})
		}
	}
	out := make([]piece, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, piece{text: p.text[r[0]:r[1]], start: p.start + r[0]})
	}
	return out
}

// Splits a piece on the runes a predicate accepts
func splitRunes(p piece, accept func(rune) bool, behavior string) []piece {
	var matches [][2]int
	for i, r := range p.text {
		if accept(r) {
			matches = append(matches, [2]int{i, i + utf8.RuneLen(r)})
		}
	}
	return joinSpans(p, cover(p.text, matches, false), behavior)
}

type preSequence []preTokenizer

func (s preSequence) preTokenize(p piece) []piece {
	pieces := []piece{p}
	for _, child := range s {
		var next []piece
		for _, q := range pieces {
			next = append(next, child.preTokenize(q)...)
		}
		pieces = next
	}
	return pieces
}

// The GPT-2 pattern, compiled once for every byte level pre tokenizer that uses the regex
var gpt2Pattern = sync.OnceValue(func() *pattern {
	p, err := compilePattern(`'s|'t|'re|'ve|'m|'ll|'d| ?\p{L}+| ?\p{N}+| ?[^\s\p{L}\p{N}]+|\s+(?!\S)|\s+`)
	if err != nil {
		panic(err)
	}
	return p
})

type byteLevelPre struct {
	addPrefixSpace bool
	useRegex       bool
}

// Prepends a space when asked, cuts on the GPT-2 pattern and spells every byte as its character
func (b byteLevelPre) preTokenize(p piece) []piece {
	if b.addPrefixSpace && !strings.HasPrefix(p.text, " ") {
		p.text = " " + p.text
	}
	pieces := []piece{p}
	if b.useRegex {
		pieces = joinSpans(p, cover(p.text, gpt2Pattern().findAll(p.text), false), behaviorIsolated)
	}
	for i := range pieces {
		pieces[i].text = toByteLevel(pieces[i].text)
	}
	return pieces
}

// Whether a rune counts as a word character, alphabetic, digit, mark, connector or join control
func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.Is(unicode.Nl, r) || unicode.Is(unicode.Other_Alphabetic, r) ||
		unicode.Is(unicode.Nd, r) || unicode.Is(unicode.Pc, r) || unicode.IsMark(r) || r == 0x200C || r == 0x200D
}

type whitespacePre struct{}

// Keeps runs of word characters and runs of other non space characters, dropping whitespace
func (whitespacePre) preTokenize(p piece) []piece {
	var out []piece
	start, kind := -1, 0
	flush := func(end int) {
		if start >= 0 {
			out = append(out, piece{text: p.text[start:end], start: p.start + start})
			start = -1
		}
	}
	for i, r := range p.text {
		k := 0
		switch {
		case isWhiteSpace(r):
			k = 0
		case isWordChar(r):
			k = 1
		default:
			k = 2
		}
		if k != kind {
			flush(i)
			kind = k
		}
		if k != 0 && start < 0 {
			start = i
		}
	}
	flush(len(p.text))
	return out
}

type whitespaceSplitPre struct{}

func (whitespaceSplitPre) preTokenize(p piece) []piece {
	return splitRunes(p, isWhiteSpace, behaviorRemoved)
}

// Whether a rune is ASCII punctuation or in the Unicode punctuation category
func isPunctuation(r rune) bool {
	if r < 0x80 {
		return (r >= '!' && r <= '/') || (r >= ':' && r <= '@') || (r >= '[' && r <= '`') || (r >= '{' && r <= '~')
	}
	return unicode.IsPunct(r)
}

type bertPre struct{}

func (bertPre) preTokenize(p piece) []piece {
	var out []piece
	for _, q := range splitRunes(p, isWhiteSpace, behaviorRemoved) {
		out = append(out, splitRunes(q, isPunctuation, behaviorIsolated)...)
	}
	return out
}

type punctuationPre struct{ behavior string }

func (n punctuationPre) preTokenize(p piece) []piece {
	return splitRunes(p, isPunctuation, n.behavior)
}

type digitsPre struct{ individual bool }

func (n digitsPre) preTokenize(p piece) []piece {
	if n.individual {
		return splitRunes(p, unicode.IsNumber, behaviorIsolated)
	}
	return splitRunes(p, unicode.IsNumber, behaviorContiguous)
}

type charDelimiterPre struct{ delimiter rune }

func (n charDelimiterPre) preTokenize(p piece) []piece {
	return splitRunes(p, func(r rune) bool { return r == n.delimiter }, behaviorRemoved)
}

type splitPre struct {
	literal  string
	re       *pattern
	behavior string
	invert   bool
}

func (n splitPre) preTokenize(p piece) []piece {
	var matches [][2]int
	if n.re != nil {
		matches = n.re.findAll(p.text)
	} else if n.literal != "" {
		for from := 0; ; {
			i := strings.Index(p.text[from:], n.literal)
			if i < 0 {
				break
			}
			matches = append(matches, [2]int{from + i, from + i + len(n.literal)})
			from += i + len(n.literal)
		}
	}
	return joinSpans(p, cover(p.text, matches, n.invert), n.behavior)
}

type metaspacePre struct {
	replacement rune
	prepend     string
	split       bool
}

// Turns spaces into the replacement, prepends one by scheme, then cuts before each replacement
func (n metaspacePre) preTokenize(p piece) []piece {
	rep := string(n.replacement)
	text := strings.ReplaceAll(p.text, " ", rep)
	if (n.prepend == "always" || (n.prepend == "first" && p.start == 0)) && !strings.HasPrefix(text, rep) {
		text = rep + text
	}
	q := piece{text: text, start: p.start}
	if !n.split {
		return []piece{q}
	}
	return splitRunes(q, func(r rune) bool { return r == n.replacement }, behaviorMergedWithNext)
}

type fixedLengthPre struct{ length int }

func (n fixedLengthPre) preTokenize(p piece) []piece {
	var out []piece
	start, count := 0, 0
	for i, r := range p.text {
		if count == n.length {
			out = append(out, piece{text: p.text[start:i], start: p.start + start})
			start, count = i, 0
		}
		count++
		_ = r
	}
	if start < len(p.text) {
		out = append(out, piece{text: p.text[start:], start: p.start + start})
	}
	return out
}

// Script names by rune, read from the Go tables and cached, the space and kana fixes of the library applied
var scriptCache sync.Map

type scriptEntry struct {
	name  string
	table *unicode.RangeTable
}

var scriptTables = sync.OnceValue(func() []scriptEntry {
	var out []scriptEntry
	for name, table := range unicode.Scripts {
		out = append(out, scriptEntry{name, table})
	}
	return out
})

// The script of a rune, Any for a space, Han for kana and the prolonged sound mark, Unknown when unassigned
func scriptOf(r rune) string {
	if r == ' ' {
		return "Any"
	}
	if r == 0x30FC {
		return "Han"
	}
	if v, ok := scriptCache.Load(r); ok {
		return v.(string)
	}
	name := "Unknown"
	for _, e := range scriptTables() {
		if unicode.Is(e.table, r) {
			name = e.name
			break
		}
	}
	if name == "Hiragana" || name == "Katakana" {
		name = "Han"
	}
	scriptCache.Store(r, name)
	return name
}

type unicodeScriptsPre struct{}

// Cuts where the script changes, spaces joining whichever run they fall in
func (unicodeScriptsPre) preTokenize(p piece) []piece {
	var out []piece
	start, last := -1, ""
	for i, r := range p.text {
		script := scriptOf(r)
		if script == "Any" {
			continue
		}
		if last == "" || last != script {
			if start >= 0 {
				out = append(out, piece{text: p.text[start:i], start: p.start + start})
			}
			start = i
		}
		last = script
	}
	if start >= 0 {
		out = append(out, piece{text: p.text[start:], start: p.start + start})
	}
	return out
}
