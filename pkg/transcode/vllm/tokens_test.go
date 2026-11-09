package vllm

import (
	"context"
	"strconv"
	"strings"
	"testing"

	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
	"google.golang.org/protobuf/proto"
)

// words encodes each space separated word as its length and decodes id n as the word of n letters w
type words struct{}

func (words) Encode(text string, special bool) ([]uint32, error) {
	var ids []uint32
	if special {
		ids = append(ids, 1)
	}
	for _, w := range strings.Fields(text) {
		ids = append(ids, uint32(len(w)))
	}
	return ids, nil
}

func (words) Decode(ids []uint32) (string, error) {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "w" + strconv.FormatUint(uint64(id), 10)
	}
	return strings.Join(parts, " "), nil
}

func (words) Stream(bool) TokenStream { return &wordStream{} }

type wordStream struct{ n int }

func (s *wordStream) Step(id uint32) (string, error) {
	s.n++
	if s.n > 1 {
		return " w" + strconv.FormatUint(uint64(id), 10), nil
	}
	return "w" + strconv.FormatUint(uint64(id), 10), nil
}

func (*wordStream) Flush() string { return "" }

var tokensCall = Call{RequestID: "tk", Tokenizer: words{}, Detokenizer: words{}}

// specials decodes like words but treats id 0 as a special token its streams leave out when asked
type specials struct{ words }

func (specials) Stream(skip bool) TokenStream { return &specialStream{skip: skip} }

type specialStream struct {
	wordStream
	skip bool
}

func (s *specialStream) Step(id uint32) (string, error) {
	if id == 0 && s.skip {
		return "", nil
	}
	return s.wordStream.Step(id)
}

// A whole answer in tokens mode is decoded under skip_special_tokens, kept by default, shown when the request turns it off
func TestTokensWholeAnswerSkipsSpecialTokens(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"default", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, "w3 w5"},
		{"kept", `{"model":"m","messages":[{"role":"user","content":"hi"}],"skip_special_tokens":false}`, "w3 w5 w0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeVLLM{reply: &vllmpb.GenerateResponse{
				PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 1},
				Outputs:    &vllmpb.SequenceOutput{TokenIds: []uint32{3, 5, 0}, NumTokens: 3, FinishInfo: finished(3, vllmpb.FinishInfo_STOP)},
			}}
			c := newClient(t, f)
			reply, err := c.ChatCompletions(context.Background(), []byte(tc.body), &renderer{}, false, Call{RequestID: "sk", Tokenizer: words{}, Detokenizer: specials{}})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(reply.Body), `"content":"`+tc.want+`"`) {
				t.Fatalf("answer %s", reply.Body)
			}
		})
	}
}

// A chat in tokens mode reaches the engine as the ids of the rendered prompt and answers from the ids it returns
func TestTokensChatUnary(t *testing.T) {
	f := &fakeVLLM{reply: &vllmpb.GenerateResponse{
		PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 2},
		Outputs:    &vllmpb.SequenceOutput{TokenIds: []uint32{3, 5}, NumTokens: 2, FinishInfo: finished(2, vllmpb.FinishInfo_STOP)},
	}}
	c := newClient(t, f)
	reply, err := c.ChatCompletions(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi there"}]}`), &renderer{}, false, tokensCall)
	if err != nil {
		t.Fatal(err)
	}
	req := f.request()
	if !proto.Equal(req.GetTokenIds(), &vllmpb.TokenIds{Ids: []uint32{8, 16}}) || req.GetText() != "" {
		t.Fatalf("prompt %v text %q", req.GetTokenIds(), req.GetText())
	}
	if req.GetResponse().OutputText == nil || req.GetResponse().GetOutputText() || !req.GetResponse().GetOutputTokenIds() {
		t.Fatalf("response options %v", req.GetResponse())
	}
	if !strings.Contains(string(reply.Body), `"content":"w3 w5"`) {
		t.Fatalf("body %s", reply.Body)
	}
}

// Streamed token ids are decoded incrementally, stop strings keeping the engine's own text on
func TestTokensChatStream(t *testing.T) {
	f := &fakeVLLM{stream: []*vllmpb.GenerateResponse{
		{PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 2}},
		{Outputs: &vllmpb.SequenceOutput{TokenIds: []uint32{4}, Text: "ignored", NumTokens: 1}},
		{Outputs: &vllmpb.SequenceOutput{TokenIds: []uint32{6}, Text: "ignored", NumTokens: 1, FinishInfo: finished(2, vllmpb.FinishInfo_STOP)}},
	}}
	c := newClient(t, f)
	reply, err := c.ChatCompletions(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,"stop":["###"]}`), &renderer{}, true, tokensCall)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sse(t, reply)
	if err != nil {
		t.Fatal(err)
	}
	const head = `{"id":"chatcmpl-tk","object":"chat.completion.chunk","created":1700000000,"model":"m",`
	wantEvents(t, got,
		head+`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}]}`,
		head+`"choices":[{"index":0,"delta":{"content":"w4"},"logprobs":null,"finish_reason":null}]}`,
		head+`"choices":[{"index":0,"delta":{"content":" w6"},"logprobs":null,"finish_reason":"stop","stop_reason":null}]}`,
		"[DONE]",
	)
	if req := f.request(); req.GetResponse().OutputText != nil || !req.GetResponse().GetOutputTokenIds() {
		t.Fatalf("response options %v", req.GetResponse())
	}
}

// A text completion prompt is tokenized with special tokens and echoed as the client wrote it
func TestTokensCompletions(t *testing.T) {
	f := &fakeVLLM{reply: &vllmpb.GenerateResponse{
		PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 3},
		Outputs:    &vllmpb.SequenceOutput{TokenIds: []uint32{2}, NumTokens: 1, FinishInfo: finished(1, vllmpb.FinishInfo_LENGTH)},
	}}
	c := newClient(t, f)
	reply, err := c.Completions(context.Background(), []byte(`{"prompt":"ab cde","echo":true}`), false, tokensCall)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(f.request().GetTokenIds(), &vllmpb.TokenIds{Ids: []uint32{1, 2, 3}}) {
		t.Fatalf("prompt %v", f.request().GetTokenIds())
	}
	if !strings.Contains(string(reply.Body), `"text":"ab cdew2"`) {
		t.Fatalf("body %s", reply.Body)
	}
}

// return_token_ids puts prompt and answer ids on completions choices and chat answers as vLLM does
func TestReturnTokenIDs(t *testing.T) {
	f := &fakeVLLM{reply: &vllmpb.GenerateResponse{
		PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 2, TokenIds: []uint32{7, 8}},
		Outputs:    &vllmpb.SequenceOutput{Text: "ok", TokenIds: []uint32{9}, NumTokens: 1, FinishInfo: finished(1, vllmpb.FinishInfo_STOP)},
	}}
	c := newClient(t, f)
	reply, err := c.Completions(context.Background(), []byte(`{"prompt":"p","return_token_ids":true}`), false, Call{RequestID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply.Body), `"prompt_token_ids":[7,8],"token_ids":[9]`) {
		t.Fatalf("completion body %s", reply.Body)
	}
	if ro := f.request().GetResponse(); !ro.GetPromptTokenIds() || !ro.GetOutputTokenIds() {
		t.Fatalf("response options %v", ro)
	}
	reply, err = c.ChatCompletions(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"return_token_ids":true}`), &renderer{}, false, Call{RequestID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	body := string(reply.Body)
	if !strings.Contains(body, `"token_ids":[9]`) || !strings.HasSuffix(body, `"prompt_token_ids":[7,8]}`) {
		t.Fatalf("chat body %s", body)
	}
	f.stream = []*vllmpb.GenerateResponse{
		{PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 2, TokenIds: []uint32{7, 8}}},
		{Outputs: &vllmpb.SequenceOutput{Text: "ok", TokenIds: []uint32{9}, NumTokens: 1, FinishInfo: finished(1, vllmpb.FinishInfo_STOP)}},
	}
	reply, err = c.ChatCompletions(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"return_token_ids":true,"stream":true}`), &renderer{}, true, Call{RequestID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sse(t, reply)
	if err != nil {
		t.Fatal(err)
	}
	const head = `{"id":"chatcmpl-r","object":"chat.completion.chunk","created":1700000000,"model":"m",`
	wantEvents(t, got,
		head+`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}],"prompt_token_ids":[7,8]}`,
		head+`"choices":[{"index":0,"delta":{"content":"ok"},"logprobs":null,"finish_reason":"stop","stop_reason":null,"token_ids":[9]}]}`,
		"[DONE]",
	)
}

// Tokens mode refuses a detokenizer that cannot decode incrementally and an engine answer without ids
func TestTokensRefusals(t *testing.T) {
	f := &fakeVLLM{reply: &vllmpb.GenerateResponse{
		PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 2},
		Outputs:    &vllmpb.SequenceOutput{Text: "no ids", NumTokens: 2, FinishInfo: finished(2, vllmpb.FinishInfo_STOP)},
	}}
	c := newClient(t, f)
	_, err := c.Completions(context.Background(), []byte(`{"prompt":"p"}`), false, Call{Tokenizer: words{}, Detokenizer: letters{}})
	if err == nil || !strings.Contains(err.Error(), "decodes incrementally") {
		t.Fatalf("err %v", err)
	}
	_, err = c.Completions(context.Background(), []byte(`{"prompt":"p"}`), false, tokensCall)
	if err == nil || !strings.Contains(err.Error(), "lacks the token ids") {
		t.Fatalf("err %v", err)
	}
}
