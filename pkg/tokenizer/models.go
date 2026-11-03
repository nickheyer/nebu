package tokenizer

import (
	"container/heap"
	"fmt"
	"unicode/utf8"
)

// Turns one pre tokenized piece into token ids and answers vocabulary lookups both ways
type model interface {
	tokenize(word string) ([]uint32, error)
	tokenToID(token string) (uint32, bool)
	idToToken(id uint32) (string, bool)
	size() int
}

// A vocabulary held both ways
type vocabulary struct {
	ids    map[string]uint32
	tokens map[uint32]string
}

func newVocabulary(entries map[string]uint32) vocabulary {
	v := vocabulary{ids: entries, tokens: make(map[uint32]string, len(entries))}
	for t, id := range entries {
		v.tokens[id] = t
	}
	return v
}

func (v vocabulary) tokenToID(token string) (uint32, bool) {
	id, ok := v.ids[token]
	return id, ok
}

func (v vocabulary) idToToken(id uint32) (string, bool) {
	t, ok := v.tokens[id]
	return t, ok
}

func (v vocabulary) size() int { return len(v.ids) }

// The ids of the byte fallback tokens of each byte, nil when the vocabulary lacks any
func byteFallbackIDs(v vocabulary) *[256]uint32 {
	var out [256]uint32
	for b := 0; b < 256; b++ {
		id, ok := v.ids[fmt.Sprintf("<0x%02X>", b)]
		if !ok {
			return nil
		}
		out[b] = id
	}
	return &out
}

// Byte pair encoding over a ranked merge list
type bpe struct {
	vocabulary
	merges       map[[2]uint32]mergeInfo
	unk          *uint32
	prefix       string
	suffix       string
	fuseUnk      bool
	byteFallback *[256]uint32
	ignoreMerges bool
}

type mergeInfo struct {
	rank int
	id   uint32
}

// Builds the merge table, each pair merging into the token spelled by both sides with the prefix dropped
func newBPE(v vocabulary, merges [][2]string, unkToken, prefix, suffix string, fuseUnk, byteFallback, ignoreMerges bool) (*bpe, error) {
	m := &bpe{vocabulary: v, merges: make(map[[2]uint32]mergeInfo, len(merges)), prefix: prefix, suffix: suffix, fuseUnk: fuseUnk, ignoreMerges: ignoreMerges}
	if unkToken != "" {
		id, ok := v.ids[unkToken]
		if !ok {
			return nil, fmt.Errorf("unk_token %q is not in the vocabulary", unkToken)
		}
		m.unk = &id
	}
	if byteFallback {
		m.byteFallback = byteFallbackIDs(v)
	}
	for rank, pair := range merges {
		a, ok := v.ids[pair[0]]
		if !ok {
			return nil, fmt.Errorf("merge %d names %q which is not in the vocabulary", rank, pair[0])
		}
		b, ok := v.ids[pair[1]]
		if !ok {
			return nil, fmt.Errorf("merge %d names %q which is not in the vocabulary", rank, pair[1])
		}
		merged := pair[0] + pair[1]
		if prefix != "" && len(pair[1]) >= len(prefix) && pair[1][:len(prefix)] == prefix {
			merged = pair[0] + pair[1][len(prefix):]
		}
		id, ok := v.ids[merged]
		if !ok {
			return nil, fmt.Errorf("merge %d produces %q which is not in the vocabulary", rank, merged)
		}
		m.merges[[2]uint32{a, b}] = mergeInfo{rank: rank, id: id}
	}
	return m, nil
}

// One symbol of a word under merging, linked to its neighbours
type symbol struct {
	id         uint32
	prev, next int
	dead       bool
}

type mergeCandidate struct {
	rank        int
	pos         int
	id          uint32
	left, right uint32
}

type mergeQueue []mergeCandidate

func (q mergeQueue) Len() int { return len(q) }
func (q mergeQueue) Less(i, j int) bool {
	if q[i].rank != q[j].rank {
		return q[i].rank < q[j].rank
	}
	return q[i].pos < q[j].pos
}
func (q mergeQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *mergeQueue) Push(x any)   { *q = append(*q, x.(mergeCandidate)) }
func (q *mergeQueue) Pop() any {
	old := *q
	x := old[len(old)-1]
	*q = old[:len(old)-1]
	return x
}

// Splits the word into characters, affixed as configured, then merges pairs by rank until none applies
func (m *bpe) tokenize(word string) ([]uint32, error) {
	if word == "" {
		return nil, nil
	}
	if m.ignoreMerges {
		if id, ok := m.ids[word]; ok {
			return []uint32{id}, nil
		}
	}
	var symbols []symbol
	count := utf8.RuneCountInString(word)
	i := 0
	for pos, r := range word {
		s := string(r)
		if pos > 0 {
			s = m.prefix + s
		}
		if i == count-1 {
			s += m.suffix
		}
		i++
		if id, ok := m.ids[s]; ok {
			symbols = append(symbols, symbol{id: id})
			continue
		}
		if m.byteFallback != nil {
			for k := 0; k < len(s); k++ {
				symbols = append(symbols, symbol{id: m.byteFallback[s[k]]})
			}
			continue
		}
		if m.unk == nil {
			return nil, fmt.Errorf("%q is not in the vocabulary and the model has no unk_token", s)
		}
		if m.fuseUnk && len(symbols) > 0 && symbols[len(symbols)-1].id == *m.unk {
			continue
		}
		symbols = append(symbols, symbol{id: *m.unk})
	}
	for i := range symbols {
		symbols[i].prev, symbols[i].next = i-1, i+1
	}
	symbols[len(symbols)-1].next = -1
	q := &mergeQueue{}
	for i := 0; i+1 < len(symbols); i++ {
		if info, ok := m.merges[[2]uint32{symbols[i].id, symbols[i+1].id}]; ok {
			heap.Push(q, mergeCandidate{rank: info.rank, pos: i, id: info.id, left: symbols[i].id, right: symbols[i+1].id})
		}
	}
	for q.Len() > 0 {
		c := heap.Pop(q).(mergeCandidate)
		s := &symbols[c.pos]
		if s.dead || s.id != c.left || s.next < 0 || symbols[s.next].id != c.right {
			continue
		}
		right := &symbols[s.next]
		right.dead = true
		s.id, s.next = c.id, right.next
		if s.next >= 0 {
			symbols[s.next].prev = c.pos
			if info, ok := m.merges[[2]uint32{s.id, symbols[s.next].id}]; ok {
				heap.Push(q, mergeCandidate{rank: info.rank, pos: c.pos, id: info.id, left: s.id, right: symbols[s.next].id})
			}
		}
		if s.prev >= 0 {
			if info, ok := m.merges[[2]uint32{symbols[s.prev].id, s.id}]; ok {
				heap.Push(q, mergeCandidate{rank: info.rank, pos: s.prev, id: info.id, left: symbols[s.prev].id, right: s.id})
			}
		}
	}
	out := make([]uint32, 0, len(symbols))
	for i := 0; i >= 0; i = symbols[i].next {
		out = append(out, symbols[i].id)
	}
	return out, nil
}

// Greedy longest match first subword tokenization with a continuation prefix
type wordPiece struct {
	vocabulary
	unk      uint32
	prefix   string
	maxChars int
}

func newWordPiece(v vocabulary, unkToken, prefix string, maxChars int) (*wordPiece, error) {
	unk, ok := v.ids[unkToken]
	if !ok {
		return nil, fmt.Errorf("unk_token %q is not in the vocabulary", unkToken)
	}
	return &wordPiece{vocabulary: v, unk: unk, prefix: prefix, maxChars: maxChars}, nil
}

func (m *wordPiece) tokenize(word string) ([]uint32, error) {
	if word == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(word) > m.maxChars {
		return []uint32{m.unk}, nil
	}
	var out []uint32
	start := 0
	for start < len(word) {
		end := len(word)
		found := false
		for start < end {
			sub := word[start:end]
			if start > 0 {
				sub = m.prefix + sub
			}
			if id, ok := m.ids[sub]; ok {
				out = append(out, id)
				found = true
				break
			}
			_, n := utf8.DecodeLastRuneInString(word[start:end])
			end -= n
		}
		if !found {
			return []uint32{m.unk}, nil
		}
		start = end
	}
	return out, nil
}

// Whole word lookup with an unknown token
type wordLevel struct {
	vocabulary
	unk uint32
}

func newWordLevel(v vocabulary, unkToken string) (*wordLevel, error) {
	unk, ok := v.ids[unkToken]
	if !ok {
		return nil, fmt.Errorf("unk_token %q is not in the vocabulary", unkToken)
	}
	return &wordLevel{vocabulary: v, unk: unk}, nil
}

func (m *wordLevel) tokenize(word string) ([]uint32, error) {
	if word == "" {
		return nil, nil
	}
	if id, ok := m.ids[word]; ok {
		return []uint32{id}, nil
	}
	return []uint32{m.unk}, nil
}

// The unknown token penalty below the lowest score
const unkPenalty = 10.0

// Viterbi segmentation over a scored vocabulary
type unigram struct {
	vocabulary
	scores       []float64
	maxLen       int
	minScore     float64
	unkID        *uint32
	byteFallback *[256]uint32
}

func newUnigram(entries []string, scores []float64, unkID *int, byteFallback bool) (*unigram, error) {
	ids := make(map[string]uint32, len(entries))
	m := &unigram{scores: scores, minScore: 0}
	for i, t := range entries {
		if _, dup := ids[t]; !dup {
			ids[t] = uint32(i)
		}
		if len(t) > m.maxLen {
			m.maxLen = len(t)
		}
		if i == 0 || scores[i] < m.minScore {
			m.minScore = scores[i]
		}
	}
	m.vocabulary = newVocabulary(ids)
	for i, t := range entries {
		m.tokens[uint32(i)] = t
	}
	if unkID != nil {
		if *unkID < 0 || *unkID >= len(entries) {
			return nil, fmt.Errorf("unk_id %d is outside the vocabulary", *unkID)
		}
		id := uint32(*unkID)
		m.unkID = &id
	}
	if byteFallback {
		m.byteFallback = byteFallbackIDs(m.vocabulary)
	}
	return m, nil
}

type pathNode struct {
	id    uint32
	score float64
	from  int
}

// The best scoring segmentation, unknown characters fused into one unknown piece
func (m *unigram) tokenize(word string) ([]uint32, error) {
	if word == "" {
		return nil, nil
	}
	unkScore := m.minScore - unkPenalty
	best := make([]pathNode, len(word)+1)
	for i := range best {
		best[i].from = -1
	}
	for start := 0; start < len(word); {
		_, charLen := utf8.DecodeRuneInString(word[start:])
		single := false
		base := best[start].score
		for end := start + 1; end <= len(word) && end-start <= m.maxLen; end++ {
			id, ok := m.ids[word[start:end]]
			if !ok {
				continue
			}
			score := base + m.scores[id]
			if best[end].from < 0 || score > best[end].score {
				best[end] = pathNode{id: id, score: score, from: start}
			}
			if end-start == charLen {
				single = true
			}
		}
		if !single {
			end := start + charLen
			score := base + unkScore
			if best[end].from < 0 || score > best[end].score {
				if m.unkID == nil {
					return nil, fmt.Errorf("%q has no token and the model has no unk_id", word[start:end])
				}
				best[end] = pathNode{id: *m.unkID, score: score, from: start}
			}
		}
		start += charLen
	}
	var pieces [][2]int
	var unks []bool
	for end := len(word); end > 0; {
		n := best[end]
		isUnk := m.unkID != nil && n.id == *m.unkID
		if isUnk && len(unks) > 0 && unks[len(unks)-1] {
			pieces[len(pieces)-1][0] = n.from
		} else {
			pieces = append(pieces, [2]int{n.from, end})
			unks = append(unks, isUnk)
		}
		end = n.from
	}
	out := make([]uint32, 0, len(pieces))
	for i := len(pieces) - 1; i >= 0; i-- {
		text := word[pieces[i][0]:pieces[i][1]]
		if id, ok := m.ids[text]; ok {
			out = append(out, id)
			continue
		}
		if m.byteFallback != nil {
			for k := 0; k < len(text); k++ {
				out = append(out, m.byteFallback[text[k]])
			}
			continue
		}
		out = append(out, *m.unkID)
	}
	return out, nil
}
