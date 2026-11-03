package chat

import (
	"slices"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Token ids a runtime answers for return_token_ids reach the result from every reply shape vLLM and SGLang write
func TestParseResultReadsTokenIDs(t *testing.T) {
	rt := RuntimeOf(v1.ApiFlavor_API_FLAVOR_OPENAI)
	cases := []struct {
		name, path, body string
		prompt, tokens   []int
	}{
		{"vllm completion", OpenAICompletions, `{"id":"c","object":"text_completion","model":"m","choices":[{"index":0,"text":"hi","finish_reason":"stop","token_ids":[7,8],"prompt_token_ids":[1,2,3]}]}`, []int{1, 2, 3}, []int{7, 8}},
		{"vllm chat", OpenAIChat, `{"id":"c","object":"chat.completion","model":"m","prompt_token_ids":[1,2],"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop","token_ids":[9]}]}`, []int{1, 2}, []int{9}},
		{"sglang chat", OpenAIChat, `{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop","prompt_token_ids":[4],"response_token_ids":[5,6]}]}`, []int{4}, []int{5, 6}},
		{"none asked", OpenAIChat, `{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := ClientOf(v1.ApiFlavor_API_FLAVOR_OPENAI).Parse(tc.path, []byte(`{"model":"m","messages":[{"role":"user","content":"x"}],"prompt":"x"}`))
			if err != nil {
				t.Fatal(err)
			}
			res, err := rt.ParseResult(req, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(res.PromptTokenIDs, tc.prompt) || !slices.Equal(res.TokenIDs, tc.tokens) {
				t.Fatalf("prompt %v tokens %v, want %v %v", res.PromptTokenIDs, res.TokenIDs, tc.prompt, tc.tokens)
			}
			if _, ok := res.Extra["prompt_token_ids"]; ok {
				t.Fatal("prompt ids also kept as an extra field, which would write them twice")
			}
		})
	}
}

// A stream's first chunk hands the prompt ids to Started and each delta's ids ride on its Said event
func TestParseStreamCarriesTokenIDs(t *testing.T) {
	rt := RuntimeOf(v1.ApiFlavor_API_FLAVOR_OPENAI)
	stream := "data: " + `{"id":"c","object":"chat.completion.chunk","model":"m","prompt_token_ids":[1,2],"choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"token_ids":[7]}]}` + "\n\n" +
		"data: " + `{"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"token_ids":[8]}]}` + "\n\n" +
		"data: " + `{"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":" there"},"finish_reason":"stop","token_ids":[9]}]}` + "\n\n" +
		"data: [DONE]\n\n"
	var started *Result
	var said [][]int
	var text strings.Builder
	err := rt.ParseStream(strings.NewReader(stream), func(ev Event) error {
		switch ev.Kind {
		case Started:
			started = ev.Res
		case Said:
			text.WriteString(ev.Text)
			said = append(said, ev.TokenIDs)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if started == nil || !slices.Equal(started.PromptTokenIDs, []int{1, 2}) {
		t.Fatalf("started %+v", started)
	}
	if text.String() != "hi there" || len(said) != 3 || !slices.Equal(said[0], []int{7}) || !slices.Equal(said[1], []int{8}) || !slices.Equal(said[2], []int{9}) {
		t.Fatalf("text %q ids %v", text.String(), said)
	}
}
