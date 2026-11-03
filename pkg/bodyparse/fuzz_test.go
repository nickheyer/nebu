package bodyparse

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

var fuzzSeeds = []string{
	`{"model":"llama","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":5}`,
	`{"model":"m","prompt":[1,2,3],"max_tokens":3}`,
	`{"model":"m","prompt":["a","b"]}`,
	`{"model":"m","instructions":"i","input":[{"role":"user","content":[{"type":"input_text","text":"x"}]}]}`,
	`{"mod\u0065l":"\ud83d\ude00","model":"x"}`,
	`{"model":5,"system":[{"type":"text","text":"s"}]}`,
	`{"options":{"num_predict":-1},"model":""}`,
	`{}`,
	`[]`,
	`{"a":[{"b":[1,2,{"c":"\u0000"}]}]}`,
}

// Expected Model answer by encoding/json, empty and non string values naming no model
func stdModel(t *testing.T, m map[string]json.RawMessage) (string, bool) {
	raw, ok := m["model"]
	if !ok || len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("model value %s does not unmarshal: %v", raw, err)
	}
	return s, s != ""
}

func FuzzParse(f *testing.F) {
	for i, s := range fuzzSeeds {
		f.Add(uint8(i%3+1), []byte(s))
	}
	f.Fuzz(func(t *testing.T, k uint8, body []byte) {
		kind := Kind(k % 8)
		d, err := Parse(kind, body)
		trimmed := bytes.TrimLeft(body, " \t\r\n")
		wantValid := json.Valid(body) && len(trimmed) > 0 && trimmed[0] == '{'
		if (err == nil) != wantValid {
			t.Fatalf("Parse err %v, json.Valid object %v", err, wantValid)
		}
		if err != nil {
			if !errors.Is(err, ErrNotJSON) {
				t.Fatalf("Parse err %v is not ErrNotJSON", err)
			}
			return
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("valid body does not unmarshal: %v", err)
		}
		want, named := stdModel(t, m)
		got, merr := d.Model()
		if named != (merr == nil) || got != want {
			t.Fatalf("Model %q %v, encoding/json %q named %v", got, merr, want, named)
		}
		if d.Stream() != (string(m["stream"]) == "true") {
			t.Fatalf("Stream %v disagrees with %s", d.Stream(), m["stream"])
		}
		_ = d.PromptText()
		_ = d.PromptTokenIDs()
		if d.MaxTokens() < 0 {
			t.Fatalf("MaxTokens %d is negative", d.MaxTokens())
		}
		doc, err := d.WithModel("fuzz/model")
		if err != nil {
			t.Fatal(err)
		}
		out := doc.Bytes()
		var after map[string]json.RawMessage
		if err := json.Unmarshal(out, &after); err != nil {
			t.Fatalf("WithModel output %q is invalid: %v", out, err)
		}
		if string(after["model"]) != `"fuzz/model"` {
			t.Fatalf("WithModel left model %s", after["model"])
		}
		for key, raw := range m {
			if key != "model" && !bytes.Equal(after[key], raw) {
				t.Fatalf("WithModel changed %q from %s to %s", key, raw, after[key])
			}
		}
		if len(after) != len(m) && !(len(after) == len(m)+1 && m["model"] == nil) {
			t.Fatalf("WithModel changed the member count from %d to %d", len(m), len(after))
		}
	})
}

// Each invalid byte replaced by U+FFFD as encoding/json does
func replaceInvalid(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		sb.WriteRune(r)
		i += size
	}
	return sb.String()
}

func FuzzUnquote(f *testing.F) {
	for _, s := range []string{`plain`, `\u00e9`, `\ud83d\ude00`, `\ud800A`, `\udc00`, "\xff", `\"\\\/\b\f\n\r\t`, `\u12`, "\x01"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, inner []byte) {
		lit := append(append([]byte{'"'}, inner...), '"')
		end, _, ok := scanString(lit, 0)
		valid := json.Valid(lit)
		if valid != (ok && end == len(lit)) {
			t.Fatalf("scanString ok %v end %d, json.Valid %v", ok, end, valid)
		}
		if !valid {
			return
		}
		var want string
		if err := json.Unmarshal(lit, &want); err != nil {
			t.Fatal(err)
		}
		if got := unquote(lit); got != want {
			t.Fatalf("unquote %q, encoding/json %q", got, want)
		}
	})
}

func FuzzQuote(f *testing.F) {
	for _, s := range []string{"", "a\"b", "\x00\u2028", "bad\xff", "\U0001F600"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		lit := appendQuoted(nil, s)
		var got string
		if err := json.Unmarshal(lit, &got); err != nil {
			t.Fatalf("appendQuoted(%q) = %s is invalid: %v", s, lit, err)
		}
		if want := replaceInvalid(s); got != want {
			t.Fatalf("round trip %q, want %q", got, want)
		}
	})
}
