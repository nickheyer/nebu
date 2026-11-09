// Package tokenizer encodes text to token ids and back from a HuggingFace tokenizer.json file in pure Go
package tokenizer

import (
	"fmt"
	"os"
	"strings"
)

// A loaded tokenizer, safe for concurrent use
type Tokenizer struct {
	normalizer normalizer
	pre        preTokenizer
	model      model
	post       postProcessor
	decoder    decoder
	added      *addedVocabulary
	byteLevel  bool
}

// Reads and parses a tokenizer.json file
func Load(path string) (*Tokenizer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

// Builds a tokenizer from the JSON text of a tokenizer.json file
func Parse(data []byte) (*Tokenizer, error) {
	var cfg fileConfig
	if err := unmarshalConfig(data, &cfg); err != nil {
		return nil, err
	}
	t := &Tokenizer{}
	var err error
	if t.normalizer, err = readNormalizer(cfg.Normalizer); err != nil {
		return nil, err
	}
	if t.pre, err = readPreTokenizer(cfg.PreTokenizer); err != nil {
		return nil, err
	}
	if t.model, t.byteLevel, err = readModel(cfg.Model); err != nil {
		return nil, err
	}
	if t.post, err = readPostProcessor(cfg.PostProcessor); err != nil {
		return nil, err
	}
	if t.decoder, err = readDecoder(cfg.Decoder); err != nil {
		return nil, err
	}
	t.added = newAddedVocabulary(readAddedTokens(cfg.AddedTokens), func(s string) string { return t.normalize(s, &normState{offset: 1}) })
	return t, nil
}

// Applies the normalizer when the file declares one
func (t *Tokenizer) normalize(s string, st *normState) string {
	if t.normalizer == nil {
		return s
	}
	return t.normalizer.normalize(s, st)
}

// Cuts a normalized run into model pieces, the whole run when the file declares no pre tokenizer
func (t *Tokenizer) preTokenize(p piece) []piece {
	var pieces []piece
	if t.pre == nil {
		pieces = []piece{p}
	} else {
		pieces = t.pre.preTokenize(p)
	}
	if t.byteLevel {
		for i := range pieces {
			pieces[i].text = toByteLevel(pieces[i].text)
		}
	}
	return pieces
}

// The token ids of a text, wrapped in the template's special tokens when asked
func (t *Tokenizer) Encode(text string, addSpecialTokens bool) ([]uint32, error) {
	ids := make([]uint32, 0, len(text)/4+1)
	for _, seg := range t.added.split(text, 0, false) {
		if seg.token {
			ids = append(ids, seg.id)
			continue
		}
		st := normState{offset: seg.start, originZero: seg.start == 0}
		normalized := t.normalize(seg.text, &st)
		base := seg.start
		if !st.originZero {
			base++
		}
		for _, inner := range t.added.split(normalized, base, true) {
			if inner.token {
				ids = append(ids, inner.id)
				continue
			}
			for _, p := range t.preTokenize(piece{text: inner.text, start: inner.start}) {
				more, err := t.model.tokenize(p.text)
				if err != nil {
					return nil, err
				}
				ids = append(ids, more...)
			}
		}
	}
	if addSpecialTokens {
		ids = t.post.process(ids)
	}
	return ids, nil
}

// The token strings of ids, added tokens by their content, special ones left out when asked
func (t *Tokenizer) tokens(ids []uint32, skipSpecial bool) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if a, ok := t.added.byID[id]; ok {
			if !skipSpecial || !a.special {
				out = append(out, a.content)
			}
			continue
		}
		if tok, ok := t.model.idToToken(id); ok {
			out = append(out, tok)
		}
	}
	return out
}

// The text of token ids, special tokens left out when asked
func (t *Tokenizer) Decode(ids []uint32, skipSpecialTokens bool) (string, error) {
	tokens := t.tokens(ids, skipSpecialTokens)
	if t.decoder == nil {
		return strings.Join(tokens, " "), nil
	}
	return strings.Join(t.decoder.decodeChain(tokens), ""), nil
}

// The id of a token, added tokens included
func (t *Tokenizer) TokenToID(token string) (uint32, bool) {
	for _, m := range []*matcher{t.added.raw, t.added.normalized} {
		for _, list := range m.byFirst {
			for _, e := range list {
				if e.token.content == token {
					return e.token.id, true
				}
			}
		}
	}
	return t.model.tokenToID(token)
}

// The token of an id, added tokens included
func (t *Tokenizer) IDToToken(id uint32) (string, bool) {
	if a, ok := t.added.byID[id]; ok {
		return a.content, true
	}
	return t.model.idToToken(id)
}

// The number of ids the model and the added tokens cover
func (t *Tokenizer) VocabSize() int {
	n := t.model.size()
	for id := range t.added.byID {
		if _, ok := t.model.idToToken(id); !ok {
			n++
		}
	}
	return n
}
