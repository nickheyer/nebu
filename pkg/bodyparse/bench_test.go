package bodyparse

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A located top level member for the decoder baselines
type span struct {
	key    string
	vs, ve int64
}

// Locates every top level member with encoding/json's Decoder tokenizer and reads the model
func locateStd(body []byte) (string, []span, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", nil, ErrNotJSON
	}
	var model string
	var spans []span
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return "", nil, err
		}
		key := tok.(string)
		vs := dec.InputOffset()
		depth := 0
		for {
			tok, err := dec.Token()
			if err != nil {
				return "", nil, err
			}
			switch v := tok.(type) {
			case json.Delim:
				if v == '{' || v == '[' {
					depth++
				} else {
					depth--
				}
			case string:
				if depth == 0 && key == "model" {
					model = v
				}
			}
			if depth == 0 {
				break
			}
		}
		spans = append(spans, span{key, vs, dec.InputOffset()})
	}
	if _, err := dec.Token(); err != nil {
		return "", nil, err
	}
	return model, spans, nil
}

// Locates every top level member with jsontext's Decoder and reads the model
func locateJSONText(body []byte) (string, []span, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(body), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return "", nil, ErrNotJSON
	}
	var model string
	var spans []span
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return "", nil, err
		}
		key := tok.String()
		vs := dec.InputOffset()
		if key == "model" {
			v, err := dec.ReadToken()
			if err != nil {
				return "", nil, err
			}
			model = v.String()
		} else if err := dec.SkipValue(); err != nil {
			return "", nil, err
		}
		spans = append(spans, span{key, vs, dec.InputOffset()})
	}
	if _, err := dec.ReadToken(); err != nil {
		return "", nil, err
	}
	return model, spans, nil
}

func locateScanner(body []byte) (string, error) {
	d, err := Parse(KindChatCompletions, body)
	if err != nil {
		return "", err
	}
	return d.Model()
}

// Chat bodies of growing size, the model last so no decoder may stop early
func benchBodies() []struct {
	name string
	body []byte
} {
	var msgs []string
	for i := range 400 {
		msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"message %d with some \"escaped\" text and \u00e9 accents to read through"}`, i))
	}
	img := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xA5, 0x3C, 0x7E}, 1<<20))
	return []struct {
		name string
		body []byte
	}{
		{"small", []byte(`{"messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"Hello there"}],"stream":true,"max_tokens":256,"temperature":0.7,"model":"meta-llama/Llama-3.1-8B-Instruct"}`)},
		{"medium", []byte(`{"messages":[` + strings.Join(msgs, ",") + `],"stream":false,"model":"m"}`)},
		{"image", []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + img + `"}}]}],"model":"vision"}`)},
	}
}

func TestBaselinesAgree(t *testing.T) {
	for _, b := range benchBodies() {
		want, err := locateScanner(b.body)
		if err != nil {
			t.Fatal(err)
		}
		std, _, err1 := locateStd(b.body)
		jt, _, err2 := locateJSONText(b.body)
		if err := errors.Join(err1, err2); err != nil || std != want || jt != want {
			t.Fatalf("%s: scanner %q, encoding/json %q, jsontext %q, %v", b.name, want, std, jt, err)
		}
	}
}

func BenchmarkLocate(b *testing.B) {
	for _, body := range benchBodies() {
		b.Run("scanner/"+body.name, func(b *testing.B) {
			b.SetBytes(int64(len(body.body)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := locateScanner(body.body); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("jsontext/"+body.name, func(b *testing.B) {
			b.SetBytes(int64(len(body.body)))
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := locateJSONText(body.body); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("encoding_json/"+body.name, func(b *testing.B) {
			b.SetBytes(int64(len(body.body)))
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := locateStd(body.body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPromptText(b *testing.B) {
	for _, body := range benchBodies() {
		b.Run(body.name, func(b *testing.B) {
			b.SetBytes(int64(len(body.body)))
			b.ReportAllocs()
			for b.Loop() {
				d, _ := Parse(KindChatCompletions, body.body)
				_ = d.PromptText()
			}
		})
	}
}

func BenchmarkWithModel(b *testing.B) {
	for _, body := range benchBodies() {
		d, _ := Parse(KindChatCompletions, body.body)
		b.Run(body.name, func(b *testing.B) {
			b.SetBytes(int64(len(body.body)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := d.WithModel("rewritten/target-model"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
