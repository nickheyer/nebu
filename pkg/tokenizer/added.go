package tokenizer

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// A token added on top of the model vocabulary and how it is found in text
type addedToken struct {
	id         uint32
	content    string
	singleWord bool
	lstrip     bool
	rstrip     bool
	normalized bool
	special    bool
}

// The added tokens, matched on raw text or on normalized text by their normalized flag
type addedVocabulary struct {
	byID       map[uint32]*addedToken
	raw        *matcher
	normalized *matcher
}

// Added tokens grouped by first byte, longest first
type matcher struct {
	byFirst map[byte][]*addedEntry
}

type addedEntry struct {
	form  string
	token *addedToken
}

func newMatcher() *matcher { return &matcher{byFirst: map[byte][]*addedEntry{}} }

func (m *matcher) add(form string, t *addedToken) {
	if form == "" {
		return
	}
	m.byFirst[form[0]] = append(m.byFirst[form[0]], &addedEntry{form: form, token: t})
}

func (m *matcher) sortEntries() {
	for _, list := range m.byFirst {
		sort.SliceStable(list, func(i, j int) bool { return len(list[i].form) > len(list[j].form) })
	}
}

// The leftmost longest entry at or after from, its byte range, and whether one exists
func (m *matcher) find(text string, from int) (*addedEntry, int, int, bool) {
	for i := from; i < len(text); i++ {
		for _, e := range m.byFirst[text[i]] {
			if strings.HasPrefix(text[i:], e.form) {
				return e, i, i + len(e.form), true
			}
		}
	}
	return nil, 0, 0, false
}

// Builds the matchers, a normalized token kept by its normalized form as the library does
func newAddedVocabulary(tokens []addedToken, normalize func(string) string) *addedVocabulary {
	v := &addedVocabulary{byID: map[uint32]*addedToken{}, raw: newMatcher(), normalized: newMatcher()}
	for i := range tokens {
		t := &tokens[i]
		v.byID[t.id] = t
		if t.normalized {
			t.content = normalize(t.content)
			v.normalized.add(t.content, t)
		} else {
			v.raw.add(t.content, t)
		}
	}
	v.raw.sortEntries()
	v.normalized.sortEntries()
	return v
}

// A run of text between added tokens or one added token
type segment struct {
	text  string
	start int
	id    uint32
	token bool
}

// Whether the match sits on word boundaries, so a single word token may take it
func singleWord(text string, start, end int) bool {
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(text[:start])
		if isWordChar(r) {
			return false
		}
	}
	if end < len(text) {
		r, _ := utf8.DecodeRuneInString(text[end:])
		if isWordChar(r) {
			return false
		}
	}
	return true
}

// Cuts the text at every added token the matcher finds, stripping whitespace the token claims
func (v *addedVocabulary) split(text string, offset int, normalized bool) []segment {
	m := v.raw
	if normalized {
		m = v.normalized
	}
	var out []segment
	last, search := 0, 0
	for search < len(text) {
		e, start, end, ok := m.find(text, search)
		if !ok {
			break
		}
		t := e.token
		if t.singleWord && !singleWord(text, start, end) {
			_, n := utf8.DecodeRuneInString(text[start:])
			search = start + n
			continue
		}
		if t.lstrip {
			for start > last {
				r, n := utf8.DecodeLastRuneInString(text[:start])
				if !isWhiteSpace(r) {
					break
				}
				start -= n
			}
		}
		if t.rstrip {
			for end < len(text) {
				r, n := utf8.DecodeRuneInString(text[end:])
				if !isWhiteSpace(r) {
					break
				}
				end += n
			}
		}
		if start > last {
			out = append(out, segment{text: text[last:start], start: offset + last})
		}
		out = append(out, segment{id: t.id, token: true, start: offset + start})
		last, search = end, end
	}
	if last < len(text) {
		out = append(out, segment{text: text[last:], start: offset + last})
	}
	return out
}
