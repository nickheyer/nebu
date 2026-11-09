package vllm

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/nickheyer/nebu/pkg/transcode"
	vllmpb "github.com/nickheyer/nebu/pkg/transcode/gen/vllm"
)

// fakeVLLM records the last request and answers with a scripted response, stream and error
type fakeVLLM struct {
	vllmpb.UnimplementedInferenceServer
	mu     sync.Mutex
	got    *vllmpb.GenerateRequest
	reply  *vllmpb.GenerateResponse
	stream []*vllmpb.GenerateResponse
	err    error
}

func (f *fakeVLLM) Generate(_ context.Context, req *vllmpb.GenerateRequest) (*vllmpb.GenerateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = req
	if f.err != nil {
		return nil, f.err
	}
	return f.reply, nil
}

func (f *fakeVLLM) GenerateStream(req *vllmpb.GenerateRequest, s vllmpb.Inference_GenerateStreamServer) error {
	f.mu.Lock()
	f.got = req
	msgs, err := f.stream, f.err
	f.mu.Unlock()
	for _, m := range msgs {
		if err := s.Send(m); err != nil {
			return err
		}
	}
	return err
}

func (f *fakeVLLM) request() *vllmpb.GenerateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.got
}

// Starts the fake on an in-memory listener and returns a Client with a fixed clock
func newClient(t *testing.T, f *fakeVLLM) *Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	vllmpb.RegisterInferenceServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := New(conn)
	c.now = func() time.Time { return time.Unix(1700000000, 0) }
	return c
}

// Writes a reply through an HTTP recorder and returns the SSE data payloads and the write result
func sse(t *testing.T, reply *transcode.Reply) ([]string, error) {
	t.Helper()
	rec := httptest.NewRecorder()
	werr := reply.Write(rec)
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	src := transcode.NewEventSource(rec.Body)
	var out []string
	for {
		ev, err := src.Next()
		if errors.Is(err, io.EOF) {
			return out, werr
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ev.Data)
	}
}

// Fails unless the fake received exactly the expected generate request
func wantRequest(t *testing.T, f *fakeVLLM, want *vllmpb.GenerateRequest) {
	t.Helper()
	if got := f.request(); !proto.Equal(got, want) {
		t.Fatalf("request mismatch\n got: %s\nwant: %s", prototext.Format(got), prototext.Format(want))
	}
}

// Fails unless the event payloads equal the expected list
func wantEvents(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("events mismatch\n got: %s\nwant: %s", strings.Join(got, "\n      "), strings.Join(want, "\n      "))
	}
}

// renderer joins role tags and text parts and records what it was given
type renderer struct {
	messages []Message
	tools    []Tool
	addGen   bool
}

func (r *renderer) Render(messages []Message, tools []Tool, addGen bool) (string, error) {
	r.messages, r.tools, r.addGen = messages, tools, addGen
	var b strings.Builder
	for _, m := range messages {
		b.WriteString("<" + m.Role + ">")
		for _, p := range m.Content {
			b.WriteString(p.Text)
		}
	}
	if addGen {
		b.WriteString("<assistant>")
	}
	return b.String(), nil
}

// markerParser reads calls written as <call>name|arguments</call>, holding back a partial opening marker
type markerParser struct{}

func (markerParser) Parse(text string) (string, []ToolCall, bool) {
	const open, closing = "<call>", "</call>"
	i := strings.Index(text, open)
	if i < 0 {
		for n := len(open) - 1; n > 0; n-- {
			if strings.HasSuffix(text, open[:n]) {
				return text[:len(text)-n], nil, true
			}
		}
		return text, nil, true
	}
	var calls []ToolCall
	rest := text[i:]
	for strings.HasPrefix(rest, open) {
		end := strings.Index(rest, closing)
		if end < 0 {
			break
		}
		name, args, _ := strings.Cut(rest[len(open):end], "|")
		calls = append(calls, ToolCall{Function: FunctionCall{Name: name, Arguments: args}})
		rest = rest[end+len(closing):]
	}
	return text[:i], calls, true
}

// letters decodes token id n as the text t followed by n
type letters struct{}

func (letters) Decode(ids []uint32) (string, error) {
	var b strings.Builder
	for _, id := range ids {
		b.WriteString("t" + strconv.FormatUint(uint64(id), 10))
	}
	return b.String(), nil
}

func finished(n uint32, reason vllmpb.FinishInfo_FinishReason) *vllmpb.FinishInfo {
	return &vllmpb.FinishInfo{NumOutputTokens: n, FinishReason: reason}
}

const weatherTools = `"tools":[{"type":"function","function":{"name":"get_weather","description":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}},{"type":"function","function":{"name":"get_time"}}]`

func TestChatUnary(t *testing.T) {
	f := &fakeVLLM{reply: &vllmpb.GenerateResponse{
		PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 12},
		Outputs: &vllmpb.SequenceOutput{Text: "Hello there", NumTokens: 3, FinishInfo: &vllmpb.FinishInfo{
			NumOutputTokens: 3, FinishReason: vllmpb.FinishInfo_STOP,
			StopReason: &vllmpb.FinishInfo_EosTokenId{EosTokenId: 2}, NumCachedTokens: proto.Uint32(8),
		}},
	}}
	c := newClient(t, f)
	r := &renderer{}
	body := `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"temperature":0.5,"top_p":0.9,"top_k":40,"max_tokens":32,"stop":"\n\n","seed":7,"presence_penalty":0.1,"frequency_penalty":0.2}`
	reply, err := c.ChatCompletions(context.Background(), []byte(body), r, false, Call{RequestID: "r1", Priority: 3, SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	wantRequest(t, f, &vllmpb.GenerateRequest{
		RequestId:   "r1",
		Model:       "m",
		Prompt:      &vllmpb.GenerateRequest_Text{Text: "<system>be brief<user>hi<assistant>"},
		Temperature: proto.Float32(0.5),
		Sampling:    &vllmpb.RandomSampling{TopK: proto.Uint32(40), TopP: proto.Float32(0.9), Seed: proto.Int64(7)},
		Decoding:    &vllmpb.DecodingParameters{PresencePenalty: 0.1, FrequencyPenalty: 0.2},
		Stopping:    &vllmpb.StoppingCriteria{MaxNewTokens: 32, StopStrings: []string{"\n\n"}},
		Response:    &vllmpb.ResponseOptions{},
		Priority:    3,
		SessionId:   proto.String("s1"),
	})
	if !r.addGen || len(r.messages) != 2 || r.messages[1].Content[0] != (ContentPart{Type: "text", Text: "hi"}) {
		t.Fatalf("renderer saw %+v add %v", r.messages, r.addGen)
	}
	want := `{"id":"chatcmpl-r1","object":"chat.completion","created":1700000000,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Hello there"},"logprobs":null,"finish_reason":"stop","stop_reason":null}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":8}}}`
	if string(reply.Body) != want {
		t.Fatalf("body\n got: %s\nwant: %s", reply.Body, want)
	}
}

func TestChatStream(t *testing.T) {
	f := &fakeVLLM{stream: []*vllmpb.GenerateResponse{
		{PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 5}},
		{Outputs: &vllmpb.SequenceOutput{Text: "Hel", NumTokens: 1}},
		{Outputs: &vllmpb.SequenceOutput{Text: "lo", NumTokens: 1, FinishInfo: &vllmpb.FinishInfo{
			NumOutputTokens: 2, FinishReason: vllmpb.FinishInfo_STOP, StopReason: &vllmpb.FinishInfo_StopString{StopString: "###"},
		}}},
	}}
	c := newClient(t, f)
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true},"max_completion_tokens":8,"max_tokens":99}`
	reply, err := c.ChatCompletions(context.Background(), []byte(body), &renderer{}, true, Call{RequestID: "r2"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sse(t, reply)
	if err != nil {
		t.Fatal(err)
	}
	const head = `{"id":"chatcmpl-r2","object":"chat.completion.chunk","created":1700000000,"model":"m",`
	wantEvents(t, got,
		head+`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}]}`,
		head+`"choices":[{"index":0,"delta":{"content":"Hel"},"logprobs":null,"finish_reason":null}]}`,
		head+`"choices":[{"index":0,"delta":{"content":"lo"},"logprobs":null,"finish_reason":"stop","stop_reason":"###"}]}`,
		head+`"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		"[DONE]",
	)
	if got := f.request().GetStopping().GetMaxNewTokens(); got != 8 {
		t.Fatalf("max_completion_tokens not preferred, got %d", got)
	}
}

func TestChatStreamContinuousUsage(t *testing.T) {
	f := &fakeVLLM{stream: []*vllmpb.GenerateResponse{
		{PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 4}},
		{Outputs: &vllmpb.SequenceOutput{Text: "ab", NumTokens: 2}},
		{Outputs: &vllmpb.SequenceOutput{Text: "c", NumTokens: 1, FinishInfo: finished(3, vllmpb.FinishInfo_LENGTH)}},
	}}
	c := newClient(t, f)
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream_options":{"include_usage":true,"continuous_usage_stats":true}}`
	reply, err := c.ChatCompletions(context.Background(), []byte(body), &renderer{}, true, Call{RequestID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sse(t, reply)
	if err != nil {
		t.Fatal(err)
	}
	const head = `{"id":"chatcmpl-r","object":"chat.completion.chunk","created":1700000000,"model":"m",`
	wantEvents(t, got,
		head+`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}],"usage":{"prompt_tokens":4,"completion_tokens":0,"total_tokens":4}}`,
		head+`"choices":[{"index":0,"delta":{"content":"ab"},"logprobs":null,"finish_reason":null}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
		head+`"choices":[{"index":0,"delta":{"content":"c"},"logprobs":null,"finish_reason":"length","stop_reason":null}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`,
		head+`"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`,
		"[DONE]",
	)
}

func TestCompletionsUnaryTokenPromptAdapter(t *testing.T) {
	f := &fakeVLLM{reply: &vllmpb.GenerateResponse{
		PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 3},
		Outputs:    &vllmpb.SequenceOutput{Text: " world", NumTokens: 4, FinishInfo: finished(4, vllmpb.FinishInfo_LENGTH)},
	}}
	c := newClient(t, f)
	body := `{"model":"my-lora","prompt":[[1,2,3]],"max_tokens":4,"stop_token_ids":[9],"min_tokens":1}`
	reply, err := c.Completions(context.Background(), []byte(body), false, Call{RequestID: "r3", Adapter: "my-lora"})
	if err != nil {
		t.Fatal(err)
	}
	wantRequest(t, f, &vllmpb.GenerateRequest{
		RequestId: "r3",
		LoraName:  "my-lora",
		Prompt:    &vllmpb.GenerateRequest_TokenIds{TokenIds: &vllmpb.TokenIds{Ids: []uint32{1, 2, 3}}},
		Sampling:  &vllmpb.RandomSampling{},
		Decoding:  &vllmpb.DecodingParameters{},
		Stopping:  &vllmpb.StoppingCriteria{MaxNewTokens: 4, MinNewTokens: 1, StopTokenIds: []uint32{9}},
		Response:  &vllmpb.ResponseOptions{},
	})
	want := `{"id":"cmpl-r3","object":"text_completion","created":1700000000,"model":"my-lora","choices":[{"index":0,"text":" world","logprobs":null,"finish_reason":"length","stop_reason":null}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`
	if string(reply.Body) != want {
		t.Fatalf("body\n got: %s\nwant: %s", reply.Body, want)
	}
}

func TestCompletionsStream(t *testing.T) {
	f := &fakeVLLM{stream: []*vllmpb.GenerateResponse{
		{PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 2}},
		{Outputs: &vllmpb.SequenceOutput{Text: "a", NumTokens: 1}},
		{Outputs: &vllmpb.SequenceOutput{Text: "b", NumTokens: 1, FinishInfo: &vllmpb.FinishInfo{
			NumOutputTokens: 2, FinishReason: vllmpb.FinishInfo_STOP, StopReason: &vllmpb.FinishInfo_StopTokenId{StopTokenId: 50},
		}}},
	}}
	c := newClient(t, f)
	reply, err := c.Completions(context.Background(), []byte(`{"model":"m","prompt":"say","stream":true}`), true, Call{RequestID: "r4"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sse(t, reply)
	if err != nil {
		t.Fatal(err)
	}
	const head = `{"id":"cmpl-r4","object":"text_completion","created":1700000000,"model":"m",`
	wantEvents(t, got,
		head+`"choices":[{"index":0,"text":"a","logprobs":null,"finish_reason":null,"stop_reason":null}]}`,
		head+`"choices":[{"index":0,"text":"b","logprobs":null,"finish_reason":"stop","stop_reason":50}]}`,
		"[DONE]",
	)
	req := f.request()
	if req.GetText() != "say" || req.GetStopping().GetMaxNewTokens() != defaultCompletionTokens {
		t.Fatalf("request %s", prototext.Format(req))
	}
}

func TestCompletionsFieldMapping(t *testing.T) {
	kv, err := structpb.NewStruct(map[string]any{"remote_host": "h", "remote_port": 9})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeVLLM{reply: &vllmpb.GenerateResponse{
		PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 1},
		Outputs: &vllmpb.SequenceOutput{Text: "{}", NumTokens: 1, FinishInfo: &vllmpb.FinishInfo{
			NumOutputTokens: 1, FinishReason: vllmpb.FinishInfo_STOP, KvTransferParams: kv,
		}},
	}}
	c := newClient(t, f)
	body := `{"model":"m","prompt":["p"],"max_tokens":null,"top_k":-1,"min_p":0.05,"repetition_penalty":1.1,
		"logit_bias":{"42":-100},"allowed_token_ids":[1,2],"skip_special_tokens":false,"truncate_prompt_tokens":512,
		"cache_salt":"salt","kv_transfer_params":{"do_remote_decode":true},"include_stop_str_in_output":true,"ignore_eos":true,
		"stop":["x","y"],"response_format":{"type":"json_schema","json_schema":{"name":"s","schema":{"type": "object"}}},"n":1}`
	reply, err := c.Completions(context.Background(), []byte(body), false, Call{RequestID: "r5"})
	if err != nil {
		t.Fatal(err)
	}
	wantKV, err := structpb.NewStruct(map[string]any{"do_remote_decode": true})
	if err != nil {
		t.Fatal(err)
	}
	wantRequest(t, f, &vllmpb.GenerateRequest{
		RequestId: "r5",
		Model:     "m",
		Prompt:    &vllmpb.GenerateRequest_Text{Text: "p"},
		Sampling:  &vllmpb.RandomSampling{TopK: proto.Uint32(0), MinP: proto.Float32(0.05)},
		Decoding: &vllmpb.DecodingParameters{
			RepetitionPenalty: 1.1,
			LogitBias:         map[uint32]float32{42: -100},
			AllowedTokenIds:   []uint32{1, 2},
			StructuredOutput:  &vllmpb.DecodingParameters_Json{Json: `{"type":"object"}`},
		},
		Stopping:             &vllmpb.StoppingCriteria{StopStrings: []string{"x", "y"}, IncludeStopStrings: true, IgnoreEos: true},
		Response:             &vllmpb.ResponseOptions{SkipSpecialTokens: proto.Bool(false)},
		TruncatePromptTokens: 512,
		Kv:                   &vllmpb.KVCacheParameters{CacheSalt: "salt", KvTransferParams: wantKV},
	})
	want := `{"id":"cmpl-r5","object":"text_completion","created":1700000000,"model":"m","choices":[{"index":0,"text":"{}","logprobs":null,"finish_reason":"stop","stop_reason":null}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"kv_transfer_params":{"remote_host":"h","remote_port":9}}`
	if string(reply.Body) != want {
		t.Fatalf("body\n got: %s\nwant: %s", reply.Body, want)
	}
}

func TestStructuredOutputs(t *testing.T) {
	cases := []struct {
		field string
		want  *vllmpb.DecodingParameters
	}{
		{`"structured_outputs":{"regex":"[a-z]+"}`, &vllmpb.DecodingParameters{StructuredOutput: &vllmpb.DecodingParameters_Regex{Regex: "[a-z]+"}}},
		{`"structured_outputs":{"choice":["yes","no"]}`, &vllmpb.DecodingParameters{StructuredOutput: &vllmpb.DecodingParameters_Choice{Choice: &vllmpb.DecodingParameters_StringChoices{Choices: []string{"yes", "no"}}}}},
		{`"structured_outputs":{"grammar":"root ::= x"}`, &vllmpb.DecodingParameters{StructuredOutput: &vllmpb.DecodingParameters_Grammar{Grammar: "root ::= x"}}},
		{`"structured_outputs":{"json":"{\"type\":\"string\"}"}`, &vllmpb.DecodingParameters{StructuredOutput: &vllmpb.DecodingParameters_Json{Json: `{"type":"string"}`}}},
		{`"structured_outputs":{"json":{"type": "string"}}`, &vllmpb.DecodingParameters{StructuredOutput: &vllmpb.DecodingParameters_Json{Json: `{"type":"string"}`}}},
		{`"structured_outputs":{"json_object":true}`, &vllmpb.DecodingParameters{StructuredOutput: &vllmpb.DecodingParameters_JsonObject{JsonObject: true}}},
		{`"structured_outputs":{"structural_tag":"{}"}`, &vllmpb.DecodingParameters{StructuredOutput: &vllmpb.DecodingParameters_StructuralTag{StructuralTag: "{}"}}},
		{`"response_format":{"type":"json_object"}`, &vllmpb.DecodingParameters{StructuredOutput: &vllmpb.DecodingParameters_JsonObject{JsonObject: true}}},
		{`"response_format":{"type":"structural_tag","format":{"type":"x"}}`, &vllmpb.DecodingParameters{StructuredOutput: &vllmpb.DecodingParameters_StructuralTag{StructuralTag: `{"type":"structural_tag","format":{"type":"x"}}`}}},
		{`"response_format":{"type":"text"}`, &vllmpb.DecodingParameters{}},
	}
	for _, tc := range cases {
		f := &fakeVLLM{reply: &vllmpb.GenerateResponse{PromptInfo: &vllmpb.PromptInfo{}, Outputs: &vllmpb.SequenceOutput{FinishInfo: finished(0, vllmpb.FinishInfo_STOP)}}}
		c := newClient(t, f)
		if _, err := c.Completions(context.Background(), []byte(`{"prompt":"p",`+tc.field+`}`), false, Call{RequestID: "x"}); err != nil {
			t.Fatalf("%s: %v", tc.field, err)
		}
		if got := f.request().GetDecoding(); !proto.Equal(got, tc.want) {
			t.Errorf("%s: got %s want %s", tc.field, prototext.Format(got), prototext.Format(tc.want))
		}
	}
}

func TestRefusals(t *testing.T) {
	chat := func(extra string) string {
		return `{"model":"m","messages":[{"role":"user","content":"hi"}]` + extra + `}`
	}
	cases := []struct {
		name   string
		body   string
		chat   bool
		stream bool
		call   Call
		msg    string
	}{
		{"n above one", chat(`,"n":2`), true, false, Call{}, "n above 1 is refused"},
		{"n zero", chat(`,"n":0`), true, false, Call{}, "n must be at least 1"},
		{"best_of", `{"prompt":"p","best_of":3}`, false, false, Call{}, "best_of above 1"},
		{"beam", chat(`,"use_beam_search":true`), true, false, Call{}, "use_beam_search"},
		{"stream options without stream", chat(`,"stream_options":{"include_usage":true}`), true, false, Call{}, "stream_options"},
		{"suffix", `{"prompt":"p","suffix":"s"}`, false, false, Call{}, "suffix"},
		{"batch", `{"prompt":["a","b"]}`, false, false, Call{}, "batch of 2 prompts"},
		{"negative ids", `{"prompt":[1,-2]}`, false, false, Call{}, "non-negative"},
		{"empty prompt", `{"prompt":[]}`, false, false, Call{}, "must not be empty"},
		{"top_k", chat(`,"top_k":-5`), true, false, Call{}, "top_k"},
		{"logit bias key", chat(`,"logit_bias":{"abc":1}`), true, false, Call{}, "logit_bias key"},
		{"max tokens zero", chat(`,"max_tokens":0`), true, false, Call{}, "max_tokens must be a positive"},
		{"truncate negative", chat(`,"truncate_prompt_tokens":-1`), true, false, Call{}, "truncate_prompt_tokens"},
		{"auto without parser", chat(`,` + weatherTools), true, false, Call{}, "needs a tool parser"},
		{"named unknown", chat(`,` + weatherTools + `,"tool_choice":{"type":"function","function":{"name":"nope"}}`), true, false, Call{}, "not among the tools"},
		{"required without tools", chat(`,"tool_choice":"required"`), true, false, Call{}, "at least one tool"},
		{"logprobs without detokenizer", chat(`,"logprobs":true`), true, false, Call{}, "no detokenizer"},
		{"top_logprobs alone", chat(`,"top_logprobs":2`), true, false, Call{}, "needs logprobs"},
		{"conflicting constraints", chat(`,"response_format":{"type":"json_object"},"structured_outputs":{"regex":"x"}`), true, false, Call{}, "use only one"},
		{"two structured kinds", chat(`,"structured_outputs":{"regex":"x","grammar":"g"}`), true, false, Call{}, "one constraint kind"},
		{"image part", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`, true, false, Call{}, "image_url"},
		{"template kwargs", chat(`,"chat_template_kwargs":{"enable_thinking":false}`), true, false, Call{}, "chat_template_kwargs"},
		{"continue final", chat(`,"continue_final_message":true`), true, false, Call{}, "continue_final_message"},
		{"no messages", `{"messages":[]}`, true, false, Call{}, "messages must not be empty"},
		{"not an object", `[1]`, true, false, Call{}, "JSON object"},
		{"echo token prompt", `{"prompt":[1,2],"echo":true}`, false, false, Call{}, "echo of a token prompt"},
		{"bad words", `{"prompt":"p","bad_words":["x"]}`, false, false, Call{}, "bad_words"},
		{"prompt logprobs", `{"prompt":"p","prompt_logprobs":1}`, false, false, Call{}, "prompt_logprobs"},
		{"reasoning effort", chat(`,"reasoning_effort":"low"`), true, false, Call{}, "reasoning_effort"},
		{"hide reasoning", chat(`,"include_reasoning":false`), true, false, Call{}, "include_reasoning"},
		{"documents", chat(`,"documents":[{"text":"d"}]`), true, false, Call{}, "documents"},
		{"chat template", chat(`,"chat_template":"{{x}}"`), true, false, Call{}, "chat_template is refused"},
		{"chat echo", chat(`,"echo":true`), true, false, Call{}, "echo is refused"},
		{"negative logprobs", `{"prompt":"p","logprobs":-1}`, false, false, Call{}, "logprobs must be"},
		{"unknown tool type", chat(`,"tools":[{"type":"retrieval","function":{"name":"x"}}]`), true, false, Call{}, "tool type"},
		{"unknown tool choice", chat(`,` + weatherTools + `,"tool_choice":"sometimes"`), true, false, Call{Tools: markerParser{}}, "sometimes"},
		{"unknown response format", chat(`,"response_format":{"type":"yaml"}`), true, false, Call{}, "response_format type"},
		{"schema missing", chat(`,"response_format":{"type":"json_schema"}`), true, false, Call{}, "json_schema.schema"},
		{"max tokens type", `{"prompt":"p","max_tokens":"many"}`, false, false, Call{}, "max_tokens must be an integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeVLLM{}
			c := newClient(t, f)
			var err error
			if tc.chat {
				_, err = c.ChatCompletions(context.Background(), []byte(tc.body), &renderer{}, tc.stream, tc.call)
			} else {
				_, err = c.Completions(context.Background(), []byte(tc.body), tc.stream, tc.call)
			}
			var te *transcode.Error
			if !errors.As(err, &te) || te.Status != 400 || !strings.Contains(string(te.Body), tc.msg) {
				t.Fatalf("got %v, want 400 containing %q", err, tc.msg)
			}
			if f.request() != nil {
				t.Fatal("refused request reached the backend")
			}
		})
	}
}

func TestErrorMapping(t *testing.T) {
	f := &fakeVLLM{err: status.Error(codes.NotFound, "LoRA adapter `x` is not loaded")}
	c := newClient(t, f)
	_, err := c.ChatCompletions(context.Background(), []byte(`{"messages":[{"role":"user","content":"hi"}]}`), &renderer{}, false, Call{Adapter: "x"})
	var te *transcode.Error
	if !errors.As(err, &te) || te.Status != 404 || string(te.Body) != `{"error":{"message":"LoRA adapter `+"`x`"+` is not loaded","type":"NotFoundError","param":null,"code":404}}` {
		t.Fatalf("unary error %v", err)
	}
	f.err = status.Error(codes.ResourceExhausted, "queue full")
	_, err = c.Completions(context.Background(), []byte(`{"prompt":"p"}`), true, Call{})
	if !errors.As(err, &te) || te.Status != 429 {
		t.Fatalf("stream open error %v", err)
	}
}

func TestMidStreamError(t *testing.T) {
	f := &fakeVLLM{
		stream: []*vllmpb.GenerateResponse{
			{PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 1}},
			{Outputs: &vllmpb.SequenceOutput{Text: "par", NumTokens: 1}},
		},
		err: status.Error(codes.Unavailable, "engine died"),
	}
	c := newClient(t, f)
	reply, err := c.ChatCompletions(context.Background(), []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), &renderer{}, true, Call{RequestID: "r6"})
	if err != nil {
		t.Fatal(err)
	}
	got, werr := sse(t, reply)
	var te *transcode.Error
	if !errors.As(werr, &te) || te.Status != 503 {
		t.Fatalf("Write returned %v", werr)
	}
	const head = `{"id":"chatcmpl-r6","object":"chat.completion.chunk","created":1700000000,"model":"m",`
	wantEvents(t, got,
		head+`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}]}`,
		head+`"choices":[{"index":0,"delta":{"content":"par"},"logprobs":null,"finish_reason":null}]}`,
		`{"error":{"message":"engine died","type":"ServiceUnavailableError","param":null,"code":503}}`,
		"[DONE]",
	)
}

func TestMissingPromptInfo(t *testing.T) {
	f := &fakeVLLM{
		reply:  &vllmpb.GenerateResponse{Outputs: &vllmpb.SequenceOutput{FinishInfo: finished(0, vllmpb.FinishInfo_STOP)}},
		stream: []*vllmpb.GenerateResponse{{Outputs: &vllmpb.SequenceOutput{Text: "a", NumTokens: 1}}},
	}
	c := newClient(t, f)
	var te *transcode.Error
	if _, err := c.Completions(context.Background(), []byte(`{"prompt":"p"}`), false, Call{}); !errors.As(err, &te) || te.Status != 502 {
		t.Fatalf("unary without prompt info returned %v", err)
	}
	reply, err := c.ChatCompletions(context.Background(), []byte(`{"messages":[{"role":"user","content":"x"}],"stream_options":{"include_usage":true}}`), &renderer{}, true, Call{RequestID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	got, werr := sse(t, reply)
	if !errors.As(werr, &te) || te.Status != 502 {
		t.Fatalf("stream without prompt info returned %v", werr)
	}
	wantEvents(t, got, `{"error":{"message":"backend stream lacks prompt info for usage","type":"BadGatewayError","param":null,"code":502}}`, "[DONE]")
}

func TestStreamEndsWithoutFinish(t *testing.T) {
	f := &fakeVLLM{stream: []*vllmpb.GenerateResponse{{PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 1}}}}
	c := newClient(t, f)
	reply, err := c.Completions(context.Background(), []byte(`{"prompt":"p"}`), true, Call{RequestID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	got, werr := sse(t, reply)
	var te *transcode.Error
	if !errors.As(werr, &te) || te.Status != 502 {
		t.Fatalf("Write returned %v", werr)
	}
	wantEvents(t, got, `{"error":{"message":"backend stream ended before the sequence finished","type":"BadGatewayError","param":null,"code":502}}`, "[DONE]")
}

func TestToolsAutoUnary(t *testing.T) {
	f := &fakeVLLM{reply: &vllmpb.GenerateResponse{
		PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 20},
		Outputs: &vllmpb.SequenceOutput{Text: `Let me check.<call>get_weather|{"city":"Paris"}</call>`, NumTokens: 9,
			FinishInfo: finished(9, vllmpb.FinishInfo_STOP)},
	}}
	c := newClient(t, f)
	r := &renderer{}
	body := `{"model":"m","messages":[{"role":"user","content":"weather?"},{"role":"assistant","content":null,"tool_calls":[{"id":"c0","type":"function","function":{"name":"get_time","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c0","content":"noon"}],` + weatherTools + `}`
	reply, err := c.ChatCompletions(context.Background(), []byte(body), r, false, Call{RequestID: "r7", Tools: markerParser{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.tools) != 2 || r.tools[0].Function.Name != "get_weather" || r.messages[1].ToolCalls[0].ID != "c0" || r.messages[2].ToolCallID != "c0" || r.messages[1].Content != nil {
		t.Fatalf("renderer saw messages %+v tools %+v", r.messages, r.tools)
	}
	if f.request().GetDecoding().GetStructuredOutput() != nil {
		t.Fatal("auto tool choice constrained output")
	}
	want := `{"id":"chatcmpl-r7","object":"chat.completion","created":1700000000,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Let me check.","tool_calls":[{"id":"call_r7_0","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"logprobs":null,"finish_reason":"tool_calls","stop_reason":null}],"usage":{"prompt_tokens":20,"completion_tokens":9,"total_tokens":29}}`
	if string(reply.Body) != want {
		t.Fatalf("body\n got: %s\nwant: %s", reply.Body, want)
	}
}

func TestToolsAutoStream(t *testing.T) {
	f := &fakeVLLM{stream: []*vllmpb.GenerateResponse{
		{PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 20}},
		{Outputs: &vllmpb.SequenceOutput{Text: "Let me", NumTokens: 2}},
		{Outputs: &vllmpb.SequenceOutput{Text: " check.<ca", NumTokens: 3}},
		{Outputs: &vllmpb.SequenceOutput{Text: `ll>get_weather|{"city":`, NumTokens: 3}},
		{Outputs: &vllmpb.SequenceOutput{Text: `"Paris"}</call>`, NumTokens: 3, FinishInfo: finished(11, vllmpb.FinishInfo_STOP)}},
	}}
	c := newClient(t, f)
	body := `{"model":"m","messages":[{"role":"user","content":"weather?"}],` + weatherTools + `,"tool_choice":"auto"}`
	reply, err := c.ChatCompletions(context.Background(), []byte(body), &renderer{}, true, Call{RequestID: "r8", Tools: markerParser{}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sse(t, reply)
	if err != nil {
		t.Fatal(err)
	}
	const head = `{"id":"chatcmpl-r8","object":"chat.completion.chunk","created":1700000000,"model":"m",`
	wantEvents(t, got,
		head+`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}]}`,
		head+`"choices":[{"index":0,"delta":{"content":"Let me"},"logprobs":null,"finish_reason":null}]}`,
		head+`"choices":[{"index":0,"delta":{"content":" check."},"logprobs":null,"finish_reason":null}]}`,
		head+`"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_r8_0","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"logprobs":null,"finish_reason":"tool_calls","stop_reason":null}]}`,
		"[DONE]",
	)
}

// retracting reports content that shrinks once more text arrives
type retracting struct{}

func (retracting) Parse(text string) (string, []ToolCall, bool) {
	if strings.Contains(text, "!") {
		return "", nil, true
	}
	return text, nil, true
}

func TestToolsAutoStreamRetraction(t *testing.T) {
	f := &fakeVLLM{stream: []*vllmpb.GenerateResponse{
		{Outputs: &vllmpb.SequenceOutput{Text: "abc", NumTokens: 1}},
		{Outputs: &vllmpb.SequenceOutput{Text: "!", NumTokens: 1, FinishInfo: finished(2, vllmpb.FinishInfo_STOP)}},
	}}
	c := newClient(t, f)
	body := `{"model":"m","messages":[{"role":"user","content":"x"}],` + weatherTools + `}`
	reply, err := c.ChatCompletions(context.Background(), []byte(body), &renderer{}, true, Call{RequestID: "r", Tools: retracting{}})
	if err != nil {
		t.Fatal(err)
	}
	got, werr := sse(t, reply)
	var te *transcode.Error
	if !errors.As(werr, &te) || te.Status != 500 || len(got) != 4 || !strings.Contains(got[2], "retracted") {
		t.Fatalf("got %v %q", werr, got)
	}
}

func TestToolsNamedStream(t *testing.T) {
	f := &fakeVLLM{stream: []*vllmpb.GenerateResponse{
		{PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 20}},
		{Outputs: &vllmpb.SequenceOutput{Text: `{"city"`, NumTokens: 2}},
		{Outputs: &vllmpb.SequenceOutput{Text: `:"Paris"}`, NumTokens: 2, FinishInfo: finished(4, vllmpb.FinishInfo_STOP)}},
	}}
	c := newClient(t, f)
	body := `{"model":"m","messages":[{"role":"user","content":"weather?"}],` + weatherTools + `,"tool_choice":{"type":"function","function":{"name":"get_weather"}}}`
	reply, err := c.ChatCompletions(context.Background(), []byte(body), &renderer{}, true, Call{RequestID: "r9"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sse(t, reply)
	if err != nil {
		t.Fatal(err)
	}
	if so := f.request().GetDecoding().GetJson(); so != `{"type":"object","properties":{"city":{"type":"string"}}}` {
		t.Fatalf("named tool schema %q", so)
	}
	const head = `{"id":"chatcmpl-r9","object":"chat.completion.chunk","created":1700000000,"model":"m",`
	wantEvents(t, got,
		head+`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}]}`,
		head+`"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_r9_0","type":"function","function":{"name":"get_weather","arguments":"{\"city\""}}]},"logprobs":null,"finish_reason":null}]}`,
		head+`"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"Paris\"}"}}]},"logprobs":null,"finish_reason":"stop","stop_reason":null}]}`,
		"[DONE]",
	)
}

func TestToolsNamedUnary(t *testing.T) {
	f := &fakeVLLM{reply: &vllmpb.GenerateResponse{
		PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 1},
		Outputs:    &vllmpb.SequenceOutput{Text: `{}`, NumTokens: 1, FinishInfo: finished(1, vllmpb.FinishInfo_STOP)},
	}}
	c := newClient(t, f)
	body := `{"model":"m","messages":[{"role":"user","content":"time?"}],` + weatherTools + `,"tool_choice":{"type":"function","function":{"name":"get_time"}}}`
	reply, err := c.ChatCompletions(context.Background(), []byte(body), &renderer{}, false, Call{RequestID: "rn"})
	if err != nil {
		t.Fatal(err)
	}
	if so := f.request().GetDecoding().GetJson(); so != `{"type":"object","properties":{}}` {
		t.Fatalf("schema %q", so)
	}
	want := `{"id":"chatcmpl-rn","object":"chat.completion","created":1700000000,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_rn_0","type":"function","function":{"name":"get_time","arguments":"{}"}}]},"logprobs":null,"finish_reason":"stop","stop_reason":null}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	if string(reply.Body) != want {
		t.Fatalf("body\n got: %s\nwant: %s", reply.Body, want)
	}
}

func TestToolsRequired(t *testing.T) {
	out := `[{"name":"get_weather","parameters":{"city":"Paris"}}, {"name":"get_time","parameters":{}}]`
	f := &fakeVLLM{
		reply: &vllmpb.GenerateResponse{
			PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 1},
			Outputs:    &vllmpb.SequenceOutput{Text: out, NumTokens: 5, FinishInfo: finished(5, vllmpb.FinishInfo_STOP)},
		},
		stream: []*vllmpb.GenerateResponse{
			{PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 1}},
			{Outputs: &vllmpb.SequenceOutput{Text: `[{"name":"get_weather","parameters":{"city":`, NumTokens: 2}},
			{Outputs: &vllmpb.SequenceOutput{Text: `"Paris"}}, {"name":"get_time"`, NumTokens: 2}},
			{Outputs: &vllmpb.SequenceOutput{Text: `,"parameters":{}}]`, NumTokens: 1, FinishInfo: finished(5, vllmpb.FinishInfo_STOP)}},
		},
	}
	c := newClient(t, f)
	body := `{"model":"m","messages":[{"role":"user","content":"both?"}],` + weatherTools + `,"tool_choice":"required","parallel_tool_calls":true}`
	reply, err := c.ChatCompletions(context.Background(), []byte(body), &renderer{}, false, Call{RequestID: "rq"})
	if err != nil {
		t.Fatal(err)
	}
	wantSchema := `{"items":{"anyOf":[{"properties":{"name":{"enum":["get_weather"],"type":"string"},"parameters":{"type":"object","properties":{"city":{"type":"string"}}}},"required":["name","parameters"]},{"properties":{"name":{"enum":["get_time"],"type":"string"},"parameters":{"type":"object","properties":{}}},"required":["name","parameters"]}],"type":"object"},"minItems":1,"type":"array"}`
	if so := f.request().GetDecoding().GetJson(); so != wantSchema {
		t.Fatalf("schema\n got: %s\nwant: %s", so, wantSchema)
	}
	want := `{"id":"chatcmpl-rq","object":"chat.completion","created":1700000000,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_rq_0","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}},{"id":"call_rq_1","type":"function","function":{"name":"get_time","arguments":"{}"}}]},"logprobs":null,"finish_reason":"tool_calls","stop_reason":null}],"usage":{"prompt_tokens":1,"completion_tokens":5,"total_tokens":6}}`
	if string(reply.Body) != want {
		t.Fatalf("body\n got: %s\nwant: %s", reply.Body, want)
	}
	reply, err = c.ChatCompletions(context.Background(), []byte(body), &renderer{}, true, Call{RequestID: "rs"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sse(t, reply)
	if err != nil {
		t.Fatal(err)
	}
	const head = `{"id":"chatcmpl-rs","object":"chat.completion.chunk","created":1700000000,"model":"m",`
	wantEvents(t, got,
		head+`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}]}`,
		head+`"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_rs_0","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"logprobs":null,"finish_reason":null}]}`,
		head+`"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_rs_1","type":"function","function":{"name":"get_time","arguments":"{}"}}]},"logprobs":null,"finish_reason":"tool_calls","stop_reason":null}]}`,
		"[DONE]",
	)
}

func TestToolsRequiredSingleSchema(t *testing.T) {
	f := &fakeVLLM{}
	c := newClient(t, f)
	body := `{"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"a"}}],"tool_choice":"required","parallel_tool_calls":false}`
	_, _ = c.ChatCompletions(context.Background(), []byte(body), &renderer{}, false, Call{})
	want := `{"items":{"anyOf":[{"properties":{"name":{"enum":["a"],"type":"string"},"parameters":{"type":"object","properties":{}}},"required":["name","parameters"]}],"type":"object"},"maxItems":1,"minItems":1,"type":"array"}`
	if got := f.request().GetDecoding().GetJson(); got != want {
		t.Fatalf("schema %s", got)
	}
}

func TestArrayElements(t *testing.T) {
	cases := []struct {
		in     string
		elems  []string
		closed bool
	}{
		{``, nil, false},
		{` [`, nil, false},
		{`[{"a":"]}"}`, []string{`{"a":"]}"}`}, false},
		{`[{"a":1},{"b":[1,2]}]`, []string{`{"a":1}`, `{"b":[1,2]}`}, true},
		{`[1, "x\"y", true]`, []string{`1`, `"x\"y"`, `true`}, true},
		{`[]`, nil, true},
		{`{"a":1}`, nil, false},
	}
	for _, tc := range cases {
		elems, closed := arrayElements(tc.in)
		if !slices.Equal(elems, tc.elems) || closed != tc.closed {
			t.Errorf("%q got %q %v want %q %v", tc.in, elems, closed, tc.elems, tc.closed)
		}
	}
}

func TestChatLogprobs(t *testing.T) {
	seq := &vllmpb.SequenceOutput{
		Text: "t10t11", NumTokens: 2, TokenIds: []uint32{10, 11}, Logprobs: []float32{-0.5, -1}, Ranks: []uint32{1, 2},
		CandidateTokens: []*vllmpb.CandidateTokenInfo{
			{Tokens: []*vllmpb.CandidateTokenInfo_TokenInfo{{Id: 10, Logprob: -0.5, Rank: 1}, {Id: 12, Logprob: -1.5, Rank: 2}}},
			{Tokens: []*vllmpb.CandidateTokenInfo_TokenInfo{{Id: 13, Logprob: -0.25, Rank: 1}, {Id: 11, Logprob: -1, Rank: 2}}},
		},
		FinishInfo: finished(2, vllmpb.FinishInfo_LENGTH),
	}
	f := &fakeVLLM{reply: &vllmpb.GenerateResponse{PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 1}, Outputs: seq}}
	c := newClient(t, f)
	body := `{"model":"m","messages":[{"role":"user","content":"x"}],"logprobs":true,"top_logprobs":2}`
	reply, err := c.ChatCompletions(context.Background(), []byte(body), &renderer{}, false, Call{RequestID: "rl", Detokenizer: letters{}})
	if err != nil {
		t.Fatal(err)
	}
	wantOpts := &vllmpb.ResponseOptions{OutputLogprobs: true, OutputTokenIds: true, OutputCandidates: &vllmpb.CandidateTokens{Select: &vllmpb.CandidateTokens_TopN{TopN: 2}}}
	if got := f.request().GetResponse(); !proto.Equal(got, wantOpts) {
		t.Fatalf("response options %s", prototext.Format(got))
	}
	want := `{"id":"chatcmpl-rl","object":"chat.completion","created":1700000000,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"t10t11"},"logprobs":{"content":[` +
		`{"token":"t10","logprob":-0.5,"bytes":[116,49,48],"top_logprobs":[{"token":"t10","logprob":-0.5,"bytes":[116,49,48]},{"token":"t12","logprob":-1.5,"bytes":[116,49,50]}]},` +
		`{"token":"t11","logprob":-1,"bytes":[116,49,49],"top_logprobs":[{"token":"t13","logprob":-0.25,"bytes":[116,49,51]},{"token":"t11","logprob":-1,"bytes":[116,49,49]}]}` +
		`]},"finish_reason":"length","stop_reason":null}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`
	if string(reply.Body) != want {
		t.Fatalf("body\n got: %s\nwant: %s", reply.Body, want)
	}
}

func TestChatLogprobsStream(t *testing.T) {
	f := &fakeVLLM{stream: []*vllmpb.GenerateResponse{
		{PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 1}},
		{Outputs: &vllmpb.SequenceOutput{Text: "", NumTokens: 1, TokenIds: []uint32{3}, Logprobs: []float32{float32(negInf())}, Ranks: []uint32{9}, FinishInfo: finished(1, vllmpb.FinishInfo_ABORTED)}},
	}}
	c := newClient(t, f)
	body := `{"model":"m","messages":[{"role":"user","content":"x"}],"logprobs":true,"return_tokens_as_token_ids":true}`
	reply, err := c.ChatCompletions(context.Background(), []byte(body), &renderer{}, true, Call{RequestID: "ra"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sse(t, reply)
	if err != nil {
		t.Fatal(err)
	}
	const head = `{"id":"chatcmpl-ra","object":"chat.completion.chunk","created":1700000000,"model":"m",`
	wantEvents(t, got,
		head+`"choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}]}`,
		head+`"choices":[{"index":0,"delta":{},"logprobs":{"content":[{"token":"token_id:3","logprob":-9999,"bytes":[116,111,107,101,110,95,105,100,58,51],"top_logprobs":[]}]},"finish_reason":"abort","stop_reason":null}]}`,
		"[DONE]",
	)
}

func negInf() float64 {
	return math.Inf(-1)
}

func TestCompletionsEchoLogprobs(t *testing.T) {
	prompt := &vllmpb.PromptInfo{
		NumPromptTokens: 2, TokenIds: []uint32{5, 6}, Logprobs: []float32{0, -2}, Ranks: []uint32{0, 3},
		CandidateTokens: []*vllmpb.CandidateTokenInfo{{}, {Tokens: []*vllmpb.CandidateTokenInfo_TokenInfo{{Id: 7, Logprob: -0.1, Rank: 1}}}},
	}
	output := &vllmpb.SequenceOutput{
		Text: "X", NumTokens: 1, TokenIds: []uint32{8}, Logprobs: []float32{-0.3}, Ranks: []uint32{1},
		CandidateTokens: []*vllmpb.CandidateTokenInfo{{Tokens: []*vllmpb.CandidateTokenInfo_TokenInfo{{Id: 8, Logprob: -0.3, Rank: 1}}}},
		FinishInfo:      finished(1, vllmpb.FinishInfo_LENGTH),
	}
	f := &fakeVLLM{
		reply:  &vllmpb.GenerateResponse{PromptInfo: prompt, Outputs: output},
		stream: []*vllmpb.GenerateResponse{{PromptInfo: prompt}, {Outputs: output}},
	}
	c := newClient(t, f)
	body := `{"model":"m","prompt":"ab","max_tokens":1,"echo":true,"logprobs":1,"return_tokens_as_token_ids":true}`
	reply, err := c.Completions(context.Background(), []byte(body), false, Call{RequestID: "re"})
	if err != nil {
		t.Fatal(err)
	}
	top1 := &vllmpb.CandidateTokens{Select: &vllmpb.CandidateTokens_TopN{TopN: 1}}
	wantOpts := &vllmpb.ResponseOptions{OutputLogprobs: true, OutputTokenIds: true, OutputCandidates: top1, PromptLogprobs: true, PromptTokenIds: true, PromptCandidates: top1}
	if got := f.request().GetResponse(); !proto.Equal(got, wantOpts) {
		t.Fatalf("response options %s", prototext.Format(got))
	}
	want := `{"id":"cmpl-re","object":"text_completion","created":1700000000,"model":"m","choices":[{"index":0,"text":"abX","logprobs":{"text_offset":[0,10,20],"token_logprobs":[null,-2,-0.3],"tokens":["token_id:5","token_id:6","token_id:8"],"top_logprobs":[null,{"token_id:7":-0.1,"token_id:6":-2},{"token_id:8":-0.3}]},"finish_reason":"length","stop_reason":null}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
	if string(reply.Body) != want {
		t.Fatalf("body\n got: %s\nwant: %s", reply.Body, want)
	}
	reply, err = c.Completions(context.Background(), []byte(body), true, Call{RequestID: "rf"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sse(t, reply)
	if err != nil {
		t.Fatal(err)
	}
	const head = `{"id":"cmpl-rf","object":"text_completion","created":1700000000,"model":"m",`
	wantEvents(t, got,
		head+`"choices":[{"index":0,"text":"ab","logprobs":{"text_offset":[0,10],"token_logprobs":[null,-2],"tokens":["token_id:5","token_id:6"],"top_logprobs":[null,{"token_id:7":-0.1,"token_id:6":-2}]},"finish_reason":null,"stop_reason":null}]}`,
		head+`"choices":[{"index":0,"text":"X","logprobs":{"text_offset":[20],"token_logprobs":[-0.3],"tokens":["token_id:8"],"top_logprobs":[{"token_id:8":-0.3}]},"finish_reason":"length","stop_reason":null}]}`,
		"[DONE]",
	)
}

func TestCompletionsEchoTokenPrompt(t *testing.T) {
	f := &fakeVLLM{reply: &vllmpb.GenerateResponse{
		PromptInfo: &vllmpb.PromptInfo{NumPromptTokens: 2},
		Outputs:    &vllmpb.SequenceOutput{Text: "!", NumTokens: 1, FinishInfo: finished(1, vllmpb.FinishInfo_LENGTH)},
	}}
	c := newClient(t, f)
	reply, err := c.Completions(context.Background(), []byte(`{"prompt":[4,2],"echo":true}`), false, Call{RequestID: "rt", Detokenizer: letters{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply.Body), `"text":"t4t2!"`) {
		t.Fatalf("body %s", reply.Body)
	}
}

func TestGeneratedRequestID(t *testing.T) {
	f := &fakeVLLM{reply: &vllmpb.GenerateResponse{PromptInfo: &vllmpb.PromptInfo{}, Outputs: &vllmpb.SequenceOutput{FinishInfo: finished(0, vllmpb.FinishInfo_STOP)}}}
	c := newClient(t, f)
	reply, err := c.Completions(context.Background(), []byte(`{"prompt":"p"}`), false, Call{})
	if err != nil {
		t.Fatal(err)
	}
	rid := f.request().GetRequestId()
	if len(rid) != 32 || !strings.Contains(string(reply.Body), `"id":"cmpl-`+rid+`"`) {
		t.Fatalf("request id %q body %s", rid, reply.Body)
	}
}

func TestStreamCancelsBackendWhenClientLeaves(t *testing.T) {
	block := make(chan struct{})
	done := make(chan struct{})
	srv := &blockingVLLM{block: block, done: done}
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	vllmpb.RegisterInferenceServer(g, srv)
	go func() { _ = g.Serve(lis) }()
	defer g.Stop()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reply, err := New(conn).Completions(context.Background(), []byte(`{"prompt":"p"}`), true, Call{RequestID: "rc"})
	if err != nil {
		t.Fatal(err)
	}
	for payload := range reply.Events {
		if !strings.Contains(string(payload), `"text":"a"`) {
			t.Fatalf("first payload %s", payload)
		}
		break
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("backend stream was not cancelled after the consumer stopped")
	}
	close(block)
}

// blockingVLLM sends one output then waits for cancellation, closing done when it sees it
type blockingVLLM struct {
	vllmpb.UnimplementedInferenceServer
	block chan struct{}
	done  chan struct{}
}

func (b *blockingVLLM) GenerateStream(_ *vllmpb.GenerateRequest, s vllmpb.Inference_GenerateStreamServer) error {
	if err := s.Send(&vllmpb.GenerateResponse{Outputs: &vllmpb.SequenceOutput{Text: "a", NumTokens: 1}}); err != nil {
		return err
	}
	select {
	case <-s.Context().Done():
		close(b.done)
	case <-b.block:
	}
	return nil
}
