package bodyparse

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

func mustParse(t *testing.T, kind Kind, body string) Document {
	t.Helper()
	d, err := Parse(kind, []byte(body))
	if err != nil {
		t.Fatalf("Parse(%s): %v", body, err)
	}
	return d
}

func TestParseValidity(t *testing.T) {
	cases := []string{
		`{}`,
		` {} `,
		`{"a":1}`,
		`{"a":-0.5e+10,"b":[true,false,null],"c":{"d":"e"}}`,
		`{"a":"\u00e9\n\"\\\/"}`,
		`{"a":[],"b":{},"c":[[]],"d":[{}]}`,
		"{\"a\":\"\xff\"}",
		`[]`,
		`"x"`,
		`1`,
		``,
		`   `,
		`{`,
		`{"a"}`,
		`{"a":}`,
		`{"a":1,}`,
		`{"a":1}x`,
		`{"a":01}`,
		`{"a":1.}`,
		`{"a":.5}`,
		`{"a":1e}`,
		`{"a":-}`,
		`{"a":tru}`,
		`{"a":truex}`,
		`{"a":"\x"}`,
		`{"a":"\u12"}`,
		"{\"a\":\"\x01\"}",
		`{"a":[1,]}`,
		`{"a":[1 2]}`,
		`{"a":{"b"}}`,
		`{"a":{"b":1,}}`,
		`{"a":{1:2}}`,
		`{"a":[}`,
		`{"a":{]}`,
		"\xef\xbb\xbf{}",
		`{"a":1}{}`,
		`{"a":"unterminated}`,
		`{"a":"\`,
		`{"a":fals}`,
		`{"a":nul}`,
		`{"a":[1`,
		`{"a":[1,`,
		`{"a":{"b":1`,
		`{"a":{"b"`,
		`{"a":{"b":`,
		`{"a":-`,
		`{"a":"\u00E9\u00e9"}`,
		`{"a":[{"b":[{"c":{}}]}]}`,
		`{"a":[0,-0,1.5e-3,2E+2,1e9]}`,
		`{"a" "b"}`,
		`{"a":1 "b":2}`,
		`{"a":1`,
		`{a:1}`,
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, err := Parse(KindChatCompletions, []byte(c))
			want := json.Valid([]byte(c)) && strings.HasPrefix(strings.TrimLeft(c, " \t\r\n"), "{")
			if (err == nil) != want {
				t.Fatalf("Parse err %v, want valid %v", err, want)
			}
			if err != nil && !errors.Is(err, ErrNotJSON) {
				t.Fatalf("Parse err %v is not ErrNotJSON", err)
			}
		})
	}
}

func TestParseDepth(t *testing.T) {
	for _, depth := range []int{9999, 10000, 10001} {
		body := `{"a":` + strings.Repeat("[", depth-1) + strings.Repeat("]", depth-1) + `}`
		_, err := Parse(KindChatCompletions, []byte(body))
		if want := json.Valid([]byte(body)); (err == nil) != want {
			t.Fatalf("depth %d: err %v, want valid %v", depth, err, want)
		}
	}
}

func TestModel(t *testing.T) {
	cases := []struct {
		name, body, want string
		err              bool
	}{
		{"plain", `{"model":"llama","messages":[]}`, "llama", false},
		{"last of duplicates", `{"model":"a","x":1,"model":"b"}`, "b", false},
		{"escaped value", `{"model":"l\u006cama\/3"}`, "llama/3", false},
		{"escaped key", `{"mod\u0065l":"x"}`, "x", false},
		{"nested model ignored", `{"options":{"model":"no"},"model":"yes"}`, "yes", false},
		{"nested only", `{"options":{"model":"no"}}`, "", true},
		{"absent", `{"messages":[]}`, "", true},
		{"number", `{"model":5}`, "", true},
		{"null", `{"model":null}`, "", true},
		{"empty", `{"model":""}`, "", true},
		{"surrogate pair", `{"model":"\ud83d\ude00"}`, "\U0001F600", false},
		{"lone surrogate", `{"model":"a\ud800b"}`, "a\uFFFDb", false},
		{"invalid utf8", "{\"model\":\"a\xffb\"}", "a\uFFFDb", false},
		{"whitespace", " {\n \"model\" : \"m\" \n} ", "m", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := mustParse(t, KindChatCompletions, c.body)
			got, err := d.Model()
			if c.err {
				if !errors.Is(err, ErrNoModel) {
					t.Fatalf("Model err %v, want ErrNoModel", err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("Model = %q, %v, want %q", got, err, c.want)
			}
		})
	}
}

func TestPromptText(t *testing.T) {
	cases := []struct {
		name string
		kind Kind
		body string
		want string
	}{
		{"chat strings", KindChatCompletions,
			`{"model":"m","messages":[{"role":"system","content":"S."},{"role":"user","content":"Hi\nthere"}]}`,
			"S.Hi\nthere"},
		{"chat parts", KindChatCompletions,
			`{"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"data:x"}},{"type":"text","text":"b"}]}]}`,
			"ab"},
		{"chat null content and tool calls", KindChatCompletions,
			`{"messages":[{"role":"assistant","content":null,"tool_calls":[{"function":{"arguments":"{}"}}]},{"role":"tool","content":"r"}]}`,
			"r"},
		{"chat escapes", KindChatCompletions,
			`{"messages":[{"role":"user","content":"\u00e9\t\"q\""}]}`,
			"\u00e9\t\"q\""},
		{"anthropic system string", KindAnthropicMessages,
			`{"system":"sys ","messages":[{"role":"user","content":"u"}],"max_tokens":5}`,
			"sys u"},
		{"anthropic system blocks and tool result", KindAnthropicMessages,
			`{"messages":[{"role":"user","content":[{"type":"tool_result","content":[{"type":"text","text":"T"}]},{"type":"text","text":"x"}]}],"system":[{"type":"text","text":"A"},{"type":"text","text":"B"}]}`,
			"ABTx"},
		{"ollama chat", KindOllamaChat,
			`{"model":"m","messages":[{"role":"user","content":"o","images":["aGk="]}]}`,
			"o"},
		{"completions string", KindCompletions, `{"prompt":"p"}`, "p"},
		{"completions array", KindCompletions, `{"prompt":["a","b","c"]}`, "a\nb\nc"},
		{"completions tokens", KindCompletions, `{"prompt":[1,2,3]}`, ""},
		{"ollama generate", KindOllamaGenerate, `{"system":"s:","prompt":"p"}`, "s:p"},
		{"responses string", KindResponses, `{"instructions":"I.","input":"in"}`, "I.in"},
		{"responses items", KindResponses,
			`{"input":[{"role":"user","content":[{"type":"input_text","text":"x"},{"type":"input_image","image_url":"u"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"y"}]}]}`,
			"xy"},
		{"embeddings array", KindEmbeddings, `{"input":["e1","e2"]}`, "e1\ne2"},
		{"embeddings string", KindEmbeddings, `{"input":"e"}`, "e"},
		{"missing field", KindChatCompletions, `{"model":"m"}`, ""},
		{"unknown kind", KindUnknown, `{"system":"s","messages":[{"content":"m"}],"prompt":"p"}`, "smp"},
		{"empty containers", KindChatCompletions, `{"messages":[{"role":"user","content":[]},{},{"content":{}},{"content":"x"}]}`, "x"},
		{"non string text member", KindAnthropicMessages, `{"messages":[{"content":[{"type":"text","text":{"a":"b"}},{"type":"text","text":"y"}]}]}`, "y"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := mustParse(t, c.kind, c.body)
			if got := d.PromptText(); got != c.want {
				t.Fatalf("PromptText = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPromptTokenIDs(t *testing.T) {
	cases := []struct {
		name string
		kind Kind
		body string
		want []int
	}{
		{"ints", KindCompletions, `{"prompt":[1, 2,3]}`, []int{1, 2, 3}},
		{"batch", KindCompletions, `{"prompt":[[1,2],[3]]}`, []int{1, 2, 3}},
		{"text", KindCompletions, `{"prompt":"x"}`, nil},
		{"strings", KindCompletions, `{"prompt":["x"]}`, nil},
		{"mixed", KindCompletions, `{"prompt":[1,"x"]}`, nil},
		{"fraction", KindCompletions, `{"prompt":[1.5]}`, nil},
		{"negative", KindCompletions, `{"prompt":[-1]}`, nil},
		{"empty", KindCompletions, `{"prompt":[]}`, nil},
		{"embeddings", KindEmbeddings, `{"input":[7,8]}`, []int{7, 8}},
		{"chat ignores", KindChatCompletions, `{"prompt":[1]}`, nil},
		{"unknown kind prompt", KindUnknown, `{"prompt":[4]}`, []int{4}},
		{"unknown kind input", KindUnknown, `{"input":[5]}`, []int{5}},
		{"bad inner token", KindCompletions, `{"prompt":[[1,"x"]]}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mustParse(t, c.kind, c.body).PromptTokenIDs()
			if !slices.Equal(got, c.want) {
				t.Fatalf("PromptTokenIDs = %v, want %v", got, c.want)
			}
		})
	}
}

func TestScalars(t *testing.T) {
	cases := []struct {
		name   string
		kind   Kind
		body   string
		stream bool
		max    int
	}{
		{"chat completion tokens", KindChatCompletions, `{"stream":true,"max_tokens":10,"max_completion_tokens":20}`, true, 20},
		{"chat max tokens", KindChatCompletions, `{"stream":false,"max_tokens":10}`, false, 10},
		{"chat null bound", KindChatCompletions, `{"max_completion_tokens":null,"max_tokens":7}`, false, 7},
		{"completions", KindCompletions, `{"max_tokens":3,"max_output_tokens":9}`, false, 3},
		{"responses", KindResponses, `{"max_output_tokens":9,"max_tokens":3}`, false, 9},
		{"anthropic", KindAnthropicMessages, `{"max_tokens":1024,"stream":true}`, true, 1024},
		{"ollama", KindOllamaChat, `{"options":{"temperature":0,"num_predict":64}}`, false, 64},
		{"ollama infinite", KindOllamaGenerate, `{"options":{"num_predict":-1}}`, false, 0},
		{"ollama ignores max_tokens", KindOllamaGenerate, `{"max_tokens":5}`, false, 0},
		{"float bound", KindCompletions, `{"max_tokens":256.0}`, false, 256},
		{"huge bound", KindCompletions, `{"max_tokens":1e40}`, false, 1<<31 - 1},
		{"negative bound", KindCompletions, `{"max_tokens":-4}`, false, 0},
		{"string bound", KindCompletions, `{"max_tokens":"4"}`, false, 0},
		{"stream string", KindCompletions, `{"stream":"true"}`, false, 0},
		{"embeddings", KindEmbeddings, `{"max_tokens":4}`, false, 0},
		{"unknown", KindUnknown, `{"options":{"num_predict":8}}`, false, 8},
		{"ollama options without bound", KindOllamaChat, `{"options":{"temperature":1}}`, false, 0},
		{"ollama options not an object", KindOllamaChat, `{"options":null}`, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := mustParse(t, c.kind, c.body)
			if d.Stream() != c.stream || d.MaxTokens() != c.max {
				t.Fatalf("Stream %v MaxTokens %d, want %v %d", d.Stream(), d.MaxTokens(), c.stream, c.max)
			}
		})
	}
}

func TestWithModel(t *testing.T) {
	cases := []struct {
		name, body, model, want string
	}{
		{"replace", `{"messages":[] , "model" : "a" ,"x":{"model":"keep"}}`, "b", `{"messages":[] , "model" : "b" ,"x":{"model":"keep"}}`},
		{"escaped target", `{"model":"a"}`, "q\"\n<", `{"model":"q\"\n<"}`},
		{"every duplicate", `{"model":"a","model":"b"}`, "c", `{"model":"c","model":"c"}`},
		{"non string", `{"model":null,"n":1}`, "m", `{"model":"m","n":1}`},
		{"append", `{"messages":[] }`, "m", `{"messages":[],"model":"m" }`},
		{"append empty object", ` { } `, "m", ` {"model":"m" } `},
		{"longer", `{"model":"a","z":"tail"}`, "much-longer-name", `{"model":"much-longer-name","z":"tail"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := mustParse(t, KindChatCompletions, c.body)
			doc, err := d.WithModel(c.model)
			if err != nil {
				t.Fatal(err)
			}
			if out := doc.Bytes(); string(out) != c.want {
				t.Fatalf("WithModel = %s, want %s", out, c.want)
			}
			if string(d.Bytes()) != c.body {
				t.Fatalf("original body changed to %s", d.Bytes())
			}
			got, err := doc.Model()
			if err != nil || got != c.model {
				t.Fatalf("mutated model %q %v, want %q", got, err, c.model)
			}
		})
	}
	if _, err := mustParse(t, KindChatCompletions, `{}`).WithModel(""); !errors.Is(err, ErrNoModel) {
		t.Fatalf("empty model err %v", err)
	}
}

func TestWithField(t *testing.T) {
	cases := []struct {
		name, body, field, raw, want string
		err                          bool
	}{
		{"set", `{"stream":false,"a":1}`, "stream", `true`, `{"stream":true,"a":1}`, false},
		{"append", `{"a":1}`, "stream_options", `{"include_usage":true}`, `{"a":1,"stream_options":{"include_usage":true}}`, false},
		{"escaped name", `{}`, "a\"b", `1`, `{"a\"b":1}`, false},
		{"invalid raw", `{}`, "a", `{`, "", true},
		{"invalid name", `{}`, "\xff", `1`, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, err := mustParse(t, KindChatCompletions, c.body).WithField(c.field, []byte(c.raw))
			if c.err {
				if err == nil {
					t.Fatalf("WithField = %s, want error", doc.Bytes())
				}
				return
			}
			if err != nil || string(doc.Bytes()) != c.want {
				t.Fatalf("WithField = %v %v, want %s", doc, err, c.want)
			}
			if !json.Valid(doc.Bytes()) {
				t.Fatalf("output %s is not valid JSON", doc.Bytes())
			}
		})
	}
}

func TestDocumentConcurrentReads(t *testing.T) {
	d := mustParse(t, KindChatCompletions, `{"model":"m","stream":true,"max_tokens":4,"messages":[{"role":"user","content":"hello"}]}`)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if m, err := d.Model(); err != nil || m != "m" {
				t.Errorf("Model %q %v", m, err)
			}
			if d.PromptText() != "hello" || !d.Stream() || d.MaxTokens() != 4 || d.PromptTokenIDs() != nil {
				t.Errorf("unexpected fields")
			}
		})
	}
	wg.Wait()
}

func TestQuoteRoundTrip(t *testing.T) {
	for _, s := range []string{"", "plain", "q\"b\\s", "\x00\x1f\n\r\t\b\f", "\u00e9\u2028\u2029\U0001F600", "bad\xffutf8", "<&>"} {
		lit := appendQuoted(nil, s)
		var got string
		if err := json.Unmarshal(lit, &got); err != nil {
			t.Fatalf("appendQuoted(%q) = %s is invalid: %v", s, lit, err)
		}
		want := strings.ToValidUTF8(s, "\uFFFD")
		if got != want {
			t.Fatalf("round trip %q = %q, want %q", s, got, want)
		}
	}
}

func TestDocumentAccessors(t *testing.T) {
	body := `{"model":"m"}`
	d := mustParse(t, KindResponses, body)
	if d.Kind() != KindResponses || string(d.Bytes()) != body {
		t.Fatalf("Kind %s Bytes %s", d.Kind(), d.Bytes())
	}
	if Kind(99).String() != "unknown" || Kind(-1).String() != "unknown" || KindOllamaChat.String() != "ollama_chat" {
		t.Fatal("kind names")
	}
}

// Every named field is set in one pass, present ones in place and absent ones appended in name order
func TestWithFields(t *testing.T) {
	cases := []struct {
		name, body string
		fields     map[string]string
		want       string
	}{
		{"replace two", `{"stream":true,"max_tokens":64,"model":"m"}`, map[string]string{"stream": "false", "max_tokens": "1"}, `{"stream":false,"max_tokens":1,"model":"m"}`},
		{"append two", `{"model":"m"}`, map[string]string{"b": "2", "a": "1"}, `{"model":"m","a":1,"b":2}`},
		{"mixed", `{"model":"m","stream":true}`, map[string]string{"stream": "false", "kv": `{"x":1}`}, `{"model":"m","stream":false,"kv":{"x":1}}`},
		{"empty object", `{}`, map[string]string{"a": "1"}, `{"a":1}`},
		{"duplicates", `{"a":1,"a":2}`, map[string]string{"a": "3"}, `{"a":3,"a":3}`},
		{"none", `{"a":1}`, map[string]string{}, `{"a":1}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fields := map[string][]byte{}
			for k, v := range c.fields {
				fields[k] = []byte(v)
			}
			doc, err := mustParse(t, KindChatCompletions, c.body).WithFields(fields)
			if err != nil || string(doc.Bytes()) != c.want {
				t.Fatalf("WithFields = %s %v, want %s", doc.Bytes(), err, c.want)
			}
			for k, v := range c.fields {
				if got, ok := doc.Field(k); !ok || string(got) != v {
					t.Fatalf("field %s = %s %v, want %s", k, got, ok, v)
				}
			}
		})
	}
	if _, err := mustParse(t, KindChatCompletions, `{}`).WithFields(map[string][]byte{"a": []byte(`{`)}); err == nil {
		t.Fatal("invalid raw accepted")
	}
}

// The raw value of the last member named, false for an absent one
func TestField(t *testing.T) {
	d := mustParse(t, KindOllamaChat, `{"name":"x","model":"a","model":"b","nested":{"name":"inner"}}`)
	if v, ok := d.Field("name"); !ok || string(v) != `"x"` {
		t.Fatalf("name = %s %v", v, ok)
	}
	if v, ok := d.Field("model"); !ok || string(v) != `"b"` {
		t.Fatalf("model = %s %v", v, ok)
	}
	if v, ok := d.Field("absent"); ok {
		t.Fatalf("absent = %s", v)
	}
}

// A view is built once and shared, carried by WithView, and dropped by every byte mutation
func TestViews(t *testing.T) {
	const key ViewKey = "chat"
	d := mustParse(t, KindChatCompletions, `{"model":"a"}`)
	builds := 0
	build := func(doc Document) (any, error) {
		builds++
		m, _ := doc.Model()
		return "view of " + m, nil
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if v, err := d.View(key, build); err != nil || v != "view of a" {
				t.Errorf("view %v %v", v, err)
			}
		})
	}
	wg.Wait()
	if builds != 1 {
		t.Fatalf("built %d times", builds)
	}
	replaced := d.WithView(key, "replaced")
	if v, _ := replaced.View(key, build); v != "replaced" {
		t.Fatalf("WithView gave %v", v)
	}
	if v, _ := d.View(key, build); v != "view of a" {
		t.Fatalf("original view changed to %v", v)
	}
	if string(replaced.Bytes()) != `{"model":"a"}` {
		t.Fatalf("WithView changed bytes to %s", replaced.Bytes())
	}
	other := replaced.WithView("other", 7)
	if v, _ := other.View(key, build); v != "replaced" {
		t.Fatalf("second WithView lost the first view, got %v", v)
	}
	mutated, err := replaced.WithModel("b")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := mutated.View(key, build); v != "view of b" || builds != 2 {
		t.Fatalf("mutated view %v after %d builds, want a fresh build", v, builds)
	}
	failing := mustParse(t, KindChatCompletions, `{}`)
	if _, err := failing.View(key, func(Document) (any, error) { return nil, errors.New("no") }); err == nil {
		t.Fatal("view error dropped")
	}
	if _, err := failing.View(key, func(Document) (any, error) { return "late", nil }); err == nil {
		t.Fatal("a failed view was rebuilt")
	}
}
