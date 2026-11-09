package vllm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/nickheyer/nebu/pkg/transcode"
	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
)

// Stands in for negative infinity and NaN, which JSON cannot carry, as vLLM does
const floorLogprob = -9999.0

// Turns token ids and their scores into OpenAI logprob entries
type logprobs struct {
	asIDs bool
	dec   Detokenizer
	top   int
	cache map[uint32]string
}

// One scored token at a position
type candidate struct {
	id      uint32
	logprob float32
	rank    uint32
}

// The sampled token at one position with its ranked alternatives
type position struct {
	sampled candidate
	alts    []*vllmpb.CandidateTokenInfo_TokenInfo
}

// Builds a logprob formatter, refusing when token text is needed and no detokenizer exists
func newLogprobs(top int, asIDs bool, dec Detokenizer) (*logprobs, error) {
	if !asIDs && dec == nil {
		return nil, transcode.BadRequest("logprobs need token text and this model has no detokenizer, set return_tokens_as_token_ids")
	}
	return &logprobs{asIDs: asIDs, dec: dec, top: top, cache: map[uint32]string{}}, nil
}

// Returns the text of one token, as token_id form when requested, caching decoded tokens
func (l *logprobs) token(id uint32) (string, error) {
	if l.asIDs {
		return "token_id:" + strconv.FormatUint(uint64(id), 10), nil
	}
	if t, ok := l.cache[id]; ok {
		return t, nil
	}
	t, err := l.dec.Decode([]uint32{id})
	if err != nil {
		return "", transcode.NewError(http.StatusInternalServerError, fmt.Sprintf("decode token %d: %v", id, err))
	}
	l.cache[id] = t
	return t, nil
}

// Zips the parallel id, logprob, rank and candidate arrays into positions, failing on length mismatch
func positions(ids []uint32, lps []float32, ranks []uint32, cands []*vllmpb.CandidateTokenInfo) ([]position, error) {
	if len(lps) != len(ids) || len(ranks) != len(ids) || (len(cands) != 0 && len(cands) != len(ids)) {
		return nil, transcode.BadGateway("backend logprob arrays disagree in length, %d ids %d logprobs %d ranks %d candidates", len(ids), len(lps), len(ranks), len(cands))
	}
	out := make([]position, len(ids))
	for i, id := range ids {
		out[i].sampled = candidate{id: id, logprob: lps[i], rank: ranks[i]}
		if len(cands) > 0 {
			out[i].alts = cands[i].GetTokens()
		}
	}
	return out, nil
}

// Returns up to top candidates by rank, appending the sampled token when keepSampled and it ranks lower
func (l *logprobs) ranked(p position, keepSampled bool) []candidate {
	all := []candidate{p.sampled}
	for _, a := range p.alts {
		if a.GetId() != p.sampled.id {
			all = append(all, candidate{id: a.GetId(), logprob: a.GetLogprob(), rank: a.GetRank()})
		}
	}
	slices.SortStableFunc(all, func(a, b candidate) int { return int(a.rank) - int(b.rank) })
	if len(all) > l.top {
		cut := all[:l.top]
		if keepSampled && !slices.ContainsFunc(cut, func(c candidate) bool { return c.id == p.sampled.id }) {
			cut = append(cut, p.sampled)
		}
		all = cut
	}
	return all
}

// Clamps a logprob into the JSON representable range
func clamp(v float32) float32 {
	if math.IsNaN(float64(v)) || math.IsInf(float64(v), -1) {
		return floorLogprob
	}
	return v
}

// Returns the UTF-8 bytes of a token as integers so JSON shows a number list
func tokenBytes(t string) []int {
	out := make([]int, len(t))
	for i := range len(t) {
		out[i] = int(t[i])
	}
	return out
}

type chatLogprobs struct {
	Content []chatToken `json:"content"`
}

type chatToken struct {
	Token       string    `json:"token"`
	Logprob     float32   `json:"logprob"`
	Bytes       []int     `json:"bytes"`
	TopLogprobs []chatTop `json:"top_logprobs"`
}

type chatTop struct {
	Token   string  `json:"token"`
	Logprob float32 `json:"logprob"`
	Bytes   []int   `json:"bytes"`
}

// Formats the sequence logprobs as chat completion content entries
func (l *logprobs) chat(seq *vllmpb.SequenceOutput) ([]chatToken, error) {
	ps, err := positions(seq.GetTokenIds(), seq.GetLogprobs(), seq.GetRanks(), seq.GetCandidateTokens())
	if err != nil {
		return nil, err
	}
	out := make([]chatToken, 0, len(ps))
	for _, p := range ps {
		t, err := l.token(p.sampled.id)
		if err != nil {
			return nil, err
		}
		entry := chatToken{Token: t, Logprob: clamp(p.sampled.logprob), Bytes: tokenBytes(t), TopLogprobs: []chatTop{}}
		for _, c := range l.ranked(p, false) {
			ct, err := l.token(c.id)
			if err != nil {
				return nil, err
			}
			entry.TopLogprobs = append(entry.TopLogprobs, chatTop{Token: ct, Logprob: clamp(c.logprob), Bytes: tokenBytes(ct)})
		}
		out = append(out, entry)
	}
	return out, nil
}

// The legacy completions logprob object
type completionLogprobs struct {
	TextOffset    []int      `json:"text_offset"`
	TokenLogprobs []*float32 `json:"token_logprobs"`
	Tokens        []string   `json:"tokens"`
	TopLogprobs   []topMap   `json:"top_logprobs"`
}

// A token to logprob object kept in rank order, null when absent
type topMap []chatTop

// Writes the entries as a JSON object in rank order, keeping the first of duplicate token texts
func (m topMap) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	seen := map[string]bool{}
	for _, e := range m {
		if seen[e.Token] {
			continue
		}
		seen[e.Token] = true
		if len(seen) > 1 {
			buf.WriteByte(',')
		}
		k, err := json.Marshal(e.Token)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(e.Logprob)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Appends positions to legacy completion logprobs advancing the text offset, a first prompt position has no score
func (l *logprobs) completion(out *completionLogprobs, offset *int, ps []position, unscoredFirst bool) error {
	for i, p := range ps {
		t, err := l.token(p.sampled.id)
		if err != nil {
			return err
		}
		out.Tokens = append(out.Tokens, t)
		out.TextOffset = append(out.TextOffset, *offset)
		*offset += utf8.RuneCountInString(t)
		if i == 0 && unscoredFirst {
			out.TokenLogprobs = append(out.TokenLogprobs, nil)
			out.TopLogprobs = append(out.TopLogprobs, nil)
			continue
		}
		lp := clamp(p.sampled.logprob)
		out.TokenLogprobs = append(out.TokenLogprobs, &lp)
		top := topMap{}
		for _, c := range l.ranked(p, true) {
			ct, err := l.token(c.id)
			if err != nil {
				return err
			}
			top = append(top, chatTop{Token: ct, Logprob: clamp(c.logprob)})
		}
		out.TopLogprobs = append(out.TopLogprobs, top)
	}
	return nil
}
