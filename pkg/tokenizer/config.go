package tokenizer

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// The top level of a tokenizer.json file
type fileConfig struct {
	Version       string           `json:"version"`
	AddedTokens   []addedTokenJSON `json:"added_tokens"`
	Normalizer    json.RawMessage  `json:"normalizer"`
	PreTokenizer  json.RawMessage  `json:"pre_tokenizer"`
	PostProcessor json.RawMessage  `json:"post_processor"`
	Decoder       json.RawMessage  `json:"decoder"`
	Model         json.RawMessage  `json:"model"`
}

// Parses the file, refusing one with no model
func unmarshalConfig(data []byte, cfg *fileConfig) error {
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("tokenizer.json: %w", err)
	}
	if len(cfg.Model) == 0 || string(cfg.Model) == "null" {
		return errors.New("tokenizer.json has no model")
	}
	return nil
}

type addedTokenJSON struct {
	ID         uint32 `json:"id"`
	Content    string `json:"content"`
	SingleWord bool   `json:"single_word"`
	LStrip     bool   `json:"lstrip"`
	RStrip     bool   `json:"rstrip"`
	Normalized bool   `json:"normalized"`
	Special    bool   `json:"special"`
}

type modelJSON struct {
	Type                    string            `json:"type"`
	UnkToken                *string           `json:"unk_token"`
	ContinuingSubwordPrefix *string           `json:"continuing_subword_prefix"`
	EndOfWordSuffix         *string           `json:"end_of_word_suffix"`
	FuseUnk                 bool              `json:"fuse_unk"`
	ByteFallback            bool              `json:"byte_fallback"`
	IgnoreMerges            bool              `json:"ignore_merges"`
	ByteLevel               bool              `json:"byte_level"`
	MaxInputCharsPerWord    *int              `json:"max_input_chars_per_word"`
	UnkID                   *int              `json:"unk_id"`
	Vocab                   json.RawMessage   `json:"vocab"`
	Merges                  []json.RawMessage `json:"merges"`
	Files                   json.RawMessage   `json:"files"`
}

// A typed component, its fields kept raw until the type says how to read them
type component struct {
	kind   string
	fields map[string]json.RawMessage
}

// Reads a component object, ok false for a missing or null one
func readComponent(raw json.RawMessage) (component, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return component{}, false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return component{}, false, err
	}
	var kind string
	if err := json.Unmarshal(fields["type"], &kind); err != nil {
		return component{}, false, errors.New("component has no type")
	}
	return component{kind: kind, fields: fields}, true, nil
}

func (c component) str(name string) (string, bool) {
	var s string
	if raw, ok := c.fields[name]; ok && json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	return "", false
}

func (c component) boolean(name string, def bool) bool {
	var b bool
	if raw, ok := c.fields[name]; ok && json.Unmarshal(raw, &b) == nil {
		return b
	}
	return def
}

func (c component) integer(name string) (int, bool) {
	var n int
	if raw, ok := c.fields[name]; ok && json.Unmarshal(raw, &n) == nil {
		return n, true
	}
	return 0, false
}

func (c component) list(name string) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if raw, ok := c.fields[name]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("%s %s: %w", c.kind, name, err)
		}
	}
	return items, nil
}

// The single character a field holds
func (c component) char(name string) (rune, error) {
	s, ok := c.str(name)
	if !ok || utf8.RuneCountInString(s) != 1 {
		return 0, fmt.Errorf("%s %s must be exactly one character", c.kind, name)
	}
	r, _ := utf8.DecodeRuneInString(s)
	return r, nil
}

// The String or Regex pattern of a Replace or Split, as a literal or a compiled pattern
func (c component) pattern() (string, *pattern, error) {
	var pat struct {
		String *string `json:"String"`
		Regex  *string `json:"Regex"`
	}
	if err := json.Unmarshal(c.fields["pattern"], &pat); err != nil {
		return "", nil, fmt.Errorf("%s pattern: %w", c.kind, err)
	}
	switch {
	case pat.String != nil:
		return *pat.String, nil, nil
	case pat.Regex != nil:
		p, err := compilePattern(*pat.Regex)
		return "", p, err
	}
	return "", nil, fmt.Errorf("%s pattern is neither String nor Regex", c.kind)
}

func readNormalizer(raw json.RawMessage) (normalizer, error) {
	c, ok, err := readComponent(raw)
	if err != nil || !ok {
		return nil, err
	}
	switch c.kind {
	case "Sequence":
		items, err := c.list("normalizers")
		if err != nil {
			return nil, err
		}
		var seq normalizerSequence
		for _, item := range items {
			n, err := readNormalizer(item)
			if err != nil {
				return nil, err
			}
			if n != nil {
				seq = append(seq, n)
			}
		}
		return seq, nil
	case "NFC":
		return unicodeForm{norm.NFC}, nil
	case "NFD":
		return unicodeForm{norm.NFD}, nil
	case "NFKC":
		return unicodeForm{norm.NFKC}, nil
	case "NFKD":
		return unicodeForm{norm.NFKD}, nil
	case "Lowercase":
		return lowercase{}, nil
	case "Strip":
		return strip{left: c.boolean("strip_left", true), right: c.boolean("strip_right", true)}, nil
	case "StripAccents":
		return stripAccents{}, nil
	case "Prepend":
		s, _ := c.str("prepend")
		return prepend{text: s}, nil
	case "Replace":
		return readReplace(c)
	case "ByteLevel":
		return byteLevelNormalizer{}, nil
	case "Nmt":
		return nmt{}, nil
	case "BertNormalizer":
		n := bertNormalizer{cleanText: c.boolean("clean_text", true), chineseChars: c.boolean("handle_chinese_chars", true), lowercase: c.boolean("lowercase", true)}
		if raw, ok := c.fields["strip_accents"]; ok && string(raw) != "null" {
			v := c.boolean("strip_accents", false)
			n.stripAccents = &v
		}
		return n, nil
	case "Precompiled":
		s, ok := c.str("precompiled_charsmap")
		if !ok {
			return nil, errors.New("precompiled normalizer has no precompiled_charsmap")
		}
		blob, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("precompiled_charsmap: %w", err)
		}
		return newPrecompiled(blob)
	case "MetaspaceNormalizer":
		r, err := c.char("replacement")
		if err != nil {
			return nil, err
		}
		scheme, _ := c.str("prepend")
		return metaspaceNormalizer{replacement: r, prepend: scheme, dropWhitespace: c.boolean("drop_whitespace", false)}, nil
	}
	return nil, fmt.Errorf("normalizer %s is not supported", c.kind)
}

func readReplace(c component) (replace, error) {
	literal, re, err := c.pattern()
	if err != nil {
		return replace{}, err
	}
	content, _ := c.str("content")
	return replace{literal: literal, re: re, content: content}, nil
}

// The split behavior a component names, the default when it names none
func readBehavior(c component, def string) (string, error) {
	s, ok := c.str("behavior")
	if !ok {
		return def, nil
	}
	switch s {
	case behaviorRemoved, behaviorIsolated, behaviorMergedWithPrevious, behaviorMergedWithNext, behaviorContiguous:
		return s, nil
	}
	return "", fmt.Errorf("%s behavior %q is unknown", c.kind, s)
}

// The metaspace prepend scheme, read from prepend_scheme or the legacy add_prefix_space flag
func readPrependScheme(c component) (string, error) {
	scheme, ok := c.str("prepend_scheme")
	if !ok {
		scheme = "always"
		if raw, has := c.fields["add_prefix_space"]; has && string(raw) == "false" {
			scheme = "never"
		}
	}
	switch scheme {
	case "always", "first", "never":
		return scheme, nil
	}
	return "", fmt.Errorf("%s prepend_scheme %q is unknown", c.kind, scheme)
}

func readPreTokenizer(raw json.RawMessage) (preTokenizer, error) {
	c, ok, err := readComponent(raw)
	if err != nil || !ok {
		return nil, err
	}
	switch c.kind {
	case "Sequence":
		items, err := c.list("pretokenizers")
		if err != nil {
			return nil, err
		}
		var seq preSequence
		for _, item := range items {
			p, err := readPreTokenizer(item)
			if err != nil {
				return nil, err
			}
			if p != nil {
				seq = append(seq, p)
			}
		}
		return seq, nil
	case "ByteLevel":
		return byteLevelPre{addPrefixSpace: c.boolean("add_prefix_space", true), useRegex: c.boolean("use_regex", true)}, nil
	case "Whitespace":
		return whitespacePre{}, nil
	case "WhitespaceSplit":
		return whitespaceSplitPre{}, nil
	case "BertPreTokenizer":
		return bertPre{}, nil
	case "Punctuation":
		b, err := readBehavior(c, behaviorIsolated)
		return punctuationPre{behavior: b}, err
	case "Digits":
		return digitsPre{individual: c.boolean("individual_digits", false)}, nil
	case "CharDelimiterSplit":
		r, err := c.char("delimiter")
		return charDelimiterPre{delimiter: r}, err
	case "UnicodeScripts":
		return unicodeScriptsPre{}, nil
	case "FixedLength":
		n, ok := c.integer("length")
		if !ok || n < 1 {
			return nil, errors.New("FixedLength needs a positive length")
		}
		return fixedLengthPre{length: n}, nil
	case "Split":
		literal, re, err := c.pattern()
		if err != nil {
			return nil, err
		}
		b, err := readBehavior(c, behaviorIsolated)
		if err != nil {
			return nil, err
		}
		return splitPre{literal: literal, re: re, behavior: b, invert: c.boolean("invert", false)}, nil
	case "Metaspace":
		r, err := c.char("replacement")
		if err != nil {
			return nil, err
		}
		scheme, err := readPrependScheme(c)
		if err != nil {
			return nil, err
		}
		return metaspacePre{replacement: r, prepend: scheme, split: c.boolean("split", true)}, nil
	}
	return nil, fmt.Errorf("pre_tokenizer %s is not supported", c.kind)
}

// Reads a token and id pair such as the sep and cls of a BERT processor
func readPair(c component, name string) (uint32, error) {
	var pair []json.RawMessage
	if err := json.Unmarshal(c.fields[name], &pair); err != nil || len(pair) != 2 {
		return 0, fmt.Errorf("%s %s is not a token and id pair", c.kind, name)
	}
	var id uint32
	if err := json.Unmarshal(pair[1], &id); err != nil {
		return 0, fmt.Errorf("%s %s id: %w", c.kind, name, err)
	}
	return id, nil
}

func readPostProcessor(raw json.RawMessage) (postProcessor, error) {
	c, ok, err := readComponent(raw)
	if err != nil {
		return nil, err
	}
	if !ok {
		return noProcessor{}, nil
	}
	switch c.kind {
	case "Sequence":
		items, err := c.list("processors")
		if err != nil {
			return nil, err
		}
		var seq postSequence
		for _, item := range items {
			p, err := readPostProcessor(item)
			if err != nil {
				return nil, err
			}
			seq = append(seq, p)
		}
		return seq, nil
	case "ByteLevel":
		return noProcessor{}, nil
	case "BertProcessing", "RobertaProcessing":
		sep, err := readPair(c, "sep")
		if err != nil {
			return nil, err
		}
		cls, err := readPair(c, "cls")
		if err != nil {
			return nil, err
		}
		return bertTemplate(cls, sep), nil
	case "TemplateProcessing":
		return readTemplate(c)
	}
	return nil, fmt.Errorf("post_processor %s is not supported", c.kind)
}

// Reads the single template, pieces spelled as Sequence and SpecialToken objects or as seq and ids
func readTemplate(c component) (postProcessor, error) {
	specials := map[string][]uint32{}
	if raw, ok := c.fields["special_tokens"]; ok && string(raw) != "null" {
		var table map[string]struct {
			IDs []uint32 `json:"ids"`
		}
		if err := json.Unmarshal(raw, &table); err != nil {
			return nil, fmt.Errorf("TemplateProcessing special_tokens: %w", err)
		}
		for name, e := range table {
			specials[name] = e.IDs
		}
	}
	items, err := c.list("single")
	if err != nil {
		return nil, err
	}
	t := templateProcessor{}
	for _, item := range items {
		var p struct {
			Sequence *struct {
				ID string `json:"id"`
			} `json:"Sequence"`
			SpecialToken *struct {
				ID string `json:"id"`
			} `json:"SpecialToken"`
			Seq *string  `json:"seq"`
			IDs []uint32 `json:"ids"`
		}
		if err := json.Unmarshal(item, &p); err != nil {
			return nil, fmt.Errorf("TemplateProcessing single: %w", err)
		}
		switch {
		case p.Sequence != nil || p.Seq != nil:
			t.single = append(t.single, templatePiece{sequence: true})
		case p.SpecialToken != nil:
			ids, ok := specials[p.SpecialToken.ID]
			if !ok {
				return nil, fmt.Errorf("TemplateProcessing names special token %q which special_tokens lacks", p.SpecialToken.ID)
			}
			t.single = append(t.single, templatePiece{ids: ids})
		case p.IDs != nil:
			t.single = append(t.single, templatePiece{ids: p.IDs})
		default:
			return nil, errors.New("TemplateProcessing piece is neither a sequence nor a special token")
		}
	}
	return t, nil
}

func readDecoder(raw json.RawMessage) (decoder, error) {
	c, ok, err := readComponent(raw)
	if err != nil || !ok {
		return nil, err
	}
	switch c.kind {
	case "Sequence":
		items, err := c.list("decoders")
		if err != nil {
			return nil, err
		}
		var seq decoderSequence
		for _, item := range items {
			d, err := readDecoder(item)
			if err != nil {
				return nil, err
			}
			if d != nil {
				seq = append(seq, d)
			}
		}
		return seq, nil
	case "ByteLevel":
		return byteLevelDecoder{}, nil
	case "WordPiece":
		prefix, ok := c.str("prefix")
		if !ok {
			prefix = "##"
		}
		return wordPieceDecoder{prefix: prefix, cleanup: c.boolean("cleanup", true)}, nil
	case "Metaspace":
		r, err := c.char("replacement")
		if err != nil {
			return nil, err
		}
		scheme, err := readPrependScheme(c)
		return metaspaceDecoder{replacement: r, prepend: scheme}, err
	case "BPEDecoder":
		suffix, ok := c.str("suffix")
		if !ok {
			suffix = "</w>"
		}
		return bpeDecoder{suffix: suffix}, nil
	case "ByteFallback":
		return byteFallbackDecoder{}, nil
	case "Fuse":
		return fuseDecoder{}, nil
	case "Strip":
		r, err := c.char("content")
		if err != nil {
			return nil, err
		}
		start, _ := c.integer("start")
		stop, _ := c.integer("stop")
		return stripDecoder{content: r, start: start, stop: stop}, nil
	case "Replace":
		rp, err := readReplace(c)
		return replaceDecoder{rp}, err
	case "CTC":
		pad, _ := c.str("pad_token")
		delim, _ := c.str("word_delimiter_token")
		return ctcDecoder{pad: pad, delimiter: delim, cleanup: c.boolean("cleanup", true)}, nil
	}
	return nil, fmt.Errorf("decoder %s is not supported", c.kind)
}

// Reads the model, merges spelled as pairs or as space separated strings
func readModel(raw json.RawMessage) (model, bool, error) {
	var m modelJSON
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false, fmt.Errorf("model: %w", err)
	}
	if len(m.Files) > 0 && string(m.Files) != "null" {
		return nil, false, errors.New("a model whose vocabulary lives in separate files is not supported")
	}
	kind := m.Type
	if kind == "" {
		switch {
		case m.Merges != nil:
			kind = "BPE"
		case m.ContinuingSubwordPrefix != nil:
			kind = "WordPiece"
		case len(m.Vocab) > 0 && m.Vocab[0] == '[':
			kind = "Unigram"
		default:
			kind = "WordLevel"
		}
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	switch kind {
	case "BPE":
		v, err := readVocabMap(m.Vocab)
		if err != nil {
			return nil, false, err
		}
		merges := make([][2]string, 0, len(m.Merges))
		for i, raw := range m.Merges {
			var pair [2]string
			var line string
			switch {
			case json.Unmarshal(raw, &pair) == nil:
			case json.Unmarshal(raw, &line) == nil:
				if strings.HasPrefix(line, "#version") {
					continue
				}
				a, b, ok := strings.Cut(line, " ")
				if !ok || strings.Contains(b, " ") {
					return nil, false, fmt.Errorf("merge %d %q does not split into two tokens on a space", i, line)
				}
				pair = [2]string{a, b}
			default:
				return nil, false, fmt.Errorf("merge %d is neither a pair nor a string", i)
			}
			merges = append(merges, pair)
		}
		b, err := newBPE(newVocabulary(v), merges, str(m.UnkToken), str(m.ContinuingSubwordPrefix), str(m.EndOfWordSuffix), m.FuseUnk, m.ByteFallback, m.IgnoreMerges)
		return b, m.ByteLevel, err
	case "WordPiece":
		v, err := readVocabMap(m.Vocab)
		if err != nil {
			return nil, false, err
		}
		maxChars := 100
		if m.MaxInputCharsPerWord != nil {
			maxChars = *m.MaxInputCharsPerWord
		}
		prefix := "##"
		if m.ContinuingSubwordPrefix != nil {
			prefix = *m.ContinuingSubwordPrefix
		}
		unk := "[UNK]"
		if m.UnkToken != nil {
			unk = *m.UnkToken
		}
		w, err := newWordPiece(newVocabulary(v), unk, prefix, maxChars)
		return w, false, err
	case "WordLevel":
		v, err := readVocabMap(m.Vocab)
		if err != nil {
			return nil, false, err
		}
		unk := "<unk>"
		if m.UnkToken != nil {
			unk = *m.UnkToken
		}
		w, err := newWordLevel(newVocabulary(v), unk)
		return w, false, err
	case "Unigram":
		var entries []json.RawMessage
		if err := json.Unmarshal(m.Vocab, &entries); err != nil {
			return nil, false, fmt.Errorf("unigram vocab: %w", err)
		}
		tokens := make([]string, len(entries))
		scores := make([]float64, len(entries))
		for i, raw := range entries {
			var pair []json.RawMessage
			if err := json.Unmarshal(raw, &pair); err != nil || len(pair) != 2 {
				return nil, false, fmt.Errorf("unigram vocab entry %d is not a token and score pair", i)
			}
			if err := json.Unmarshal(pair[0], &tokens[i]); err != nil {
				return nil, false, fmt.Errorf("unigram vocab entry %d token: %w", i, err)
			}
			if err := json.Unmarshal(pair[1], &scores[i]); err != nil {
				return nil, false, fmt.Errorf("unigram vocab entry %d score: %w", i, err)
			}
		}
		u, err := newUnigram(tokens, scores, m.UnkID, m.ByteFallback)
		return u, false, err
	}
	return nil, false, fmt.Errorf("model %s is not supported", kind)
}

func readVocabMap(raw json.RawMessage) (map[string]uint32, error) {
	var v map[string]uint32
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("vocab: %w", err)
	}
	if len(v) == 0 {
		return nil, errors.New("vocab is empty")
	}
	return v, nil
}

// The added tokens in ascending id order
func readAddedTokens(items []addedTokenJSON) []addedToken {
	sort.SliceStable(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	out := make([]addedToken, 0, len(items))
	for _, a := range items {
		if a.Content == "" {
			continue
		}
		out = append(out, addedToken{id: a.ID, content: a.Content, singleWord: a.SingleWord, lstrip: a.LStrip, rstrip: a.RStrip, normalized: a.Normalized, special: a.Special})
	}
	return out
}
