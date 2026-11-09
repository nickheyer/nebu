package tokenizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The encodings the Python tokenizers package produced for every fixture, see testdata/gen.py
type reference struct {
	Fixtures map[string]struct {
		File  string `json:"file"`
		Cases []struct {
			Text       string   `json:"text"`
			IDs        []uint32 `json:"ids"`
			Tokens     []string `json:"tokens"`
			IDsPlain   []uint32 `json:"ids_plain"`
			Decoded    string   `json:"decoded"`
			DecodedAll string   `json:"decoded_all"`
		} `json:"cases"`
		Singles []struct {
			ID   uint32 `json:"id"`
			Text string `json:"text"`
		} `json:"singles"`
	} `json:"fixtures"`
}

func loadReference(t *testing.T) reference {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "reference.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ref reference
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

// Every fixture encodes and decodes every probe exactly as the Python tokenizers package does
func TestReference(t *testing.T) {
	ref := loadReference(t)
	names := make([]string, 0, len(ref.Fixtures))
	for name := range ref.Fixtures {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		fx := ref.Fixtures[name]
		t.Run(name, func(t *testing.T) {
			tok, err := Load(filepath.Join("testdata", fx.File))
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range fx.Cases {
				ids, err := tok.Encode(c.Text, true)
				if err != nil {
					t.Fatalf("encode %q: %v", c.Text, err)
				}
				if !slices.Equal(ids, c.IDs) {
					t.Errorf("encode %q\n got %v %q\nwant %v %q", c.Text, ids, tok.tokens(ids, false), c.IDs, c.Tokens)
					continue
				}
				plain, err := tok.Encode(c.Text, false)
				if err != nil {
					t.Fatalf("encode %q: %v", c.Text, err)
				}
				if !slices.Equal(plain, c.IDsPlain) {
					t.Errorf("encode %q without specials\n got %v\nwant %v", c.Text, plain, c.IDsPlain)
				}
				if got := tok.tokens(ids, false); !sameTokens(tok, ids, got, c.Tokens) {
					t.Errorf("tokens %q\n got %q\nwant %q", c.Text, got, c.Tokens)
				}
				if got, _ := tok.Decode(c.IDs, true); got != c.Decoded {
					t.Errorf("decode %q\n got %q\nwant %q", c.Text, got, c.Decoded)
				}
				if got, _ := tok.Decode(c.IDs, false); got != c.DecodedAll {
					t.Errorf("decode %q with specials\n got %q\nwant %q", c.Text, got, c.DecodedAll)
				}
			}
			for _, s := range fx.Singles {
				if got, _ := tok.Decode([]uint32{s.ID}, false); got != s.Text {
					t.Errorf("decode id %d\n got %q\nwant %q", s.ID, got, s.Text)
				}
			}
		})
	}
}

// Compares token strings, the reference spelling unknown pieces as their text and stripped tokens with their whitespace
func sameTokens(tok *Tokenizer, ids []uint32, got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] == want[i] || strings.TrimSpace(want[i]) == got[i] {
			continue
		}
		if unk, ok := tok.TokenToID(got[i]); ok && unk == ids[i] && (got[i] == "<unk>" || got[i] == "[UNK]") {
			continue
		}
		return false
	}
	return true
}

// Streaming decode yields the same text as decoding every id at once
func TestStream(t *testing.T) {
	ref := loadReference(t)
	for name, fx := range ref.Fixtures {
		tok, err := Load(filepath.Join("testdata", fx.File))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range fx.Cases {
			s := tok.NewStream(true)
			var b strings.Builder
			for _, id := range c.IDs {
				part, err := s.Step(id)
				if err != nil {
					t.Fatal(err)
				}
				b.WriteString(part)
			}
			b.WriteString(s.Flush())
			want, _ := tok.Decode(c.IDs, true)
			if b.String() != want {
				t.Errorf("%s stream %q\n got %q\nwant %q", name, c.Text, b.String(), want)
			}
		}
	}
}

// Vocabulary lookups answer both ways for model and added tokens
func TestLookups(t *testing.T) {
	tok, err := Load(filepath.Join("testdata", "bert_wordpiece.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		token string
		ok    bool
	}{{"[CLS]", true}, {"the", true}, {"[MASK]", true}, {"no-such-token", false}}
	for _, c := range cases {
		id, ok := tok.TokenToID(c.token)
		if ok != c.ok {
			t.Fatalf("TokenToID(%q) ok %v, want %v", c.token, ok, c.ok)
		}
		if !ok {
			continue
		}
		if back, ok := tok.IDToToken(id); !ok || back != c.token {
			t.Fatalf("IDToToken(%d) = %q %v, want %q", id, back, ok, c.token)
		}
	}
	if tok.VocabSize() < 700 {
		t.Fatalf("vocab size %d", tok.VocabSize())
	}
}

// Files with unsupported or malformed components are refused with the component named
func TestRefusals(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"no model", `{"version":"1.0"}`, "no model"},
		{"unknown normalizer", `{"model":{"type":"WordLevel","vocab":{"a":0,"<unk>":1},"unk_token":"<unk>"},"normalizer":{"type":"Mystery"}}`, "normalizer Mystery"},
		{"unknown pre tokenizer", `{"model":{"type":"WordLevel","vocab":{"a":0,"<unk>":1},"unk_token":"<unk>"},"pre_tokenizer":{"type":"Mystery"}}`, "pre_tokenizer Mystery"},
		{"lookbehind", `{"model":{"type":"WordLevel","vocab":{"a":0,"<unk>":1},"unk_token":"<unk>"},"pre_tokenizer":{"type":"Split","pattern":{"Regex":"(?<=a)b"},"behavior":"Isolated"}}`, "lookaround"},
		{"missing unk", `{"model":{"type":"WordPiece","vocab":{"a":0},"unk_token":"[UNK]"}}`, "unk_token"},
		{"bad merge", `{"model":{"type":"BPE","vocab":{"a":0,"b":1},"merges":["a c"]}}`, "merge 0"},
		{"vocab files", `{"model":{"type":"BPE","files":{"vocab":"vocab.json"},"vocab":{"a":0},"merges":[]}}`, "separate files"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.body))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
		})
	}
}

// Patterns split as the Rust regex crate does, including the trailing lookahead idiom
func TestPattern(t *testing.T) {
	cases := []struct {
		pattern string
		text    string
		want    []string
	}{
		{`'s|'t|'re|'ve|'m|'ll|'d| ?\p{L}+| ?\p{N}+| ?[^\s\p{L}\p{N}]+|\s+(?!\S)|\s+`, "Hello   world  ", []string{"Hello", "  ", " world", "  "}},
		{`\s+(?!\S)|\s+`, "a b", []string{" "}},
		{`\s*[\r\n]+|\s+(?!\S)|\s+`, "\n\n\na", []string{"\n\n\n"}},
		{`\s*[\r\n]+|\s+(?!\S)|\s+`, "x  \n  y", []string{"  \n", " ", " "}},
		{`\p{N}{1,3}`, "12345", []string{"123", "45"}},
		{`\w+`, "ab\u00a0cd\u200dx", []string{"ab", "cd\u200dx"}},
		{`\s+`, "a　b c\u0085d", []string{"　", " ", "\u0085"}},
	}
	for _, c := range cases {
		p, err := compilePattern(c.pattern)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range p.findAll(c.text) {
			got = append(got, c.text[m[0]:m[1]])
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%q on %q\n got %q\nwant %q", c.pattern, c.text, got, c.want)
		}
	}
}
