package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Completion prompts render one element each, text or token ids, to the completions endpoint untemplated
func TestCompletionsPromptsRenderEach(t *testing.T) {
	for _, tc := range []struct{ prompt, want string }{
		{`"one"`, `"one"`},
		{`["a","b c"]`, `["a","b c"]`},
		{`[1,2,3]`, `[1,2,3]`},
		{`[[1,2],[3]]`, `[[1,2],[3]]`},
		{`""`, `""`},
	} {
		req, err := (openai{}).Parse(OpenAICompletions, []byte(`{"model":"m","prompt":`+tc.prompt+`,"suffix":"z"}`))
		if err != nil {
			t.Fatal(err)
		}
		if req.Kind != Generate || !req.Raw {
			t.Fatalf("%s: kind %v raw %v", tc.prompt, req.Kind, req.Raw)
		}
		path, out, err := (openai{}).Render(req)
		if err != nil || path != OpenAICompletions {
			t.Fatalf("%s: %s %v", tc.prompt, path, err)
		}
		if got := string(rawField(out, "prompt")); got != tc.want || string(rawField(out, "suffix")) != `"z"` || rawField(out, "messages") != nil {
			t.Fatalf("%s rendered %s", tc.prompt, out)
		}
	}
	req, _ := (openai{}).Parse(OpenAICompletions, []byte(`{"model":"m","prompt":["a","b"]}`))
	if len(req.Messages) != 2 || TextOf(req.Messages[1].Parts) != "b" {
		t.Fatalf("one turn per prompt %+v", req.Messages)
	}
	if n := EstimateTokens(req); n != 2*turnTokens+2 {
		t.Fatalf("estimate %d", n)
	}
	for _, rt := range []Runtime{anthropic{}, ollama{}} {
		if _, _, err := rt.Render(req); err == nil {
			t.Fatalf("%T joined prompts it cannot send apart", rt)
		}
	}
	one, _ := (openai{}).Parse(OpenAICompletions, []byte(`{"model":"m","prompt":"hi","max_tokens":4}`))
	path, out, err := (ollama{}).Render(one)
	if err != nil || path != OllamaGenerate || string(rawField(out, "raw")) != "true" || string(rawField(out, "prompt")) != `"hi"` || !strings.Contains(string(out), `"num_predict":4`) {
		t.Fatalf("ollama raw generate %s %s %v", path, out, err)
	}
	if _, _, err := (anthropic{}).Render(one); err == nil || !strings.Contains(err.Error(), "raw prompt") {
		t.Fatalf("anthropic templated a raw prompt: %v", err)
	}
	tokens, _ := (openai{}).Parse(OpenAICompletions, []byte(`{"model":"m","prompt":[5,6]}`))
	if _, _, err := (ollama{}).Render(tokens); err == nil {
		t.Fatal("ollama took token ids")
	}
	gen, _ := (ollama{}).Parse(OllamaGenerate, []byte(`{"model":"m","prompt":"p","system":"s"}`))
	if path, out, err := (openai{}).Render(gen); err != nil || path != OpenAIChat || len(rawList(rawField(out, "messages"))) != 2 {
		t.Fatalf("templated generate goes to chat: %s %s %v", path, out, err)
	}
	if path, out, err := (ollama{}).Render(gen); err != nil || path != OllamaGenerate || string(rawField(out, "system")) != `"s"` || rawField(out, "raw") != nil {
		t.Fatalf("ollama generate %s %s %v", path, out, err)
	}
}

// Every choice of a whole answer survives parsing and rendering by index, a one choice protocol refusing more
func TestChoicesWholeRoundTrip(t *testing.T) {
	req, err := (openai{}).Parse(OpenAIChat, []byte(`{"model":"m","n":2,"messages":[{"role":"user","content":"x"}]}`))
	if err != nil || req.N != 2 {
		t.Fatalf("%+v %v", req, err)
	}
	if _, out, _ := (openai{}).Render(req); string(rawField(out, "n")) != "2" {
		t.Fatalf("n not rendered %s", out)
	}
	if _, _, err := (anthropic{}).Render(req); err == nil || !strings.Contains(err.Error(), "call per choice") {
		t.Fatalf("anthropic dropped choices: %v", err)
	}
	body := `{"id":"x","model":"m","choices":[{"index":1,"message":{"role":"assistant","content":"two"},"finish_reason":"length"},{"index":0,"message":{"role":"assistant","content":"one","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`
	res, err := (openai{}).ParseResult(req, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	all := res.All()
	if len(all) != 2 || all[0].Choice != 0 || all[0].Text != "one" || all[0].Stop != StopTool || all[1].Choice != 1 || all[1].Text != "two" || all[1].Stop != StopLength || res.Usage.In != 3 {
		t.Fatalf("choices %+v %+v", all[0], all[1])
	}
	w := httptest.NewRecorder()
	out := (openai{}).Answer(w, req, "m")
	for _, r := range all {
		out.Write(Event{Kind: Said, Text: r.Text, Choice: r.Choice})
		for i, c := range r.ToolCalls {
			out.Write(Event{Kind: Called, Index: i, Choice: r.Choice, Tool: &c})
		}
	}
	out.Write(Event{Kind: Stopped, Res: res})
	var got struct {
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Choices) != 2 || got.Choices[0].Message.Content != "one" || got.Choices[0].FinishReason != "tool_calls" || got.Choices[0].Message.ToolCalls[0].ID != "c" || got.Choices[1].Index != 1 || got.Choices[1].Message.Content != "two" || got.Choices[1].FinishReason != "length" {
		t.Fatalf("rendered %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	if err := (ollama{}).Answer(w, &Request{Kind: Chat}, "m").Write(Event{Kind: Said, Text: "x", Choice: 1}); err == nil {
		t.Fatal("ollama merged a second choice")
	}
}

// Streamed choices arrive as events keyed by index and go back out as chunks keyed the same way
func TestChoicesStreamRoundTrip(t *testing.T) {
	sse := "data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"a\"}},{\"index\":1,\"delta\":{\"role\":\"assistant\",\"content\":\"b\"}}]}\n\n" +
		"data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"index\":1,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"function\":{\"name\":\"f\",\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"},{\"index\":1,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	req, _ := (openai{}).Parse(OpenAIChat, []byte(`{"model":"m","n":2,"stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"x"}]}`))
	w := httptest.NewRecorder()
	out := (openai{}).Answer(w, req, "m")
	said := map[int]string{}
	var stopped *Result
	err := (openai{}).ParseStream(strings.NewReader(sse), func(ev Event) error {
		switch ev.Kind {
		case Said:
			said[ev.Choice] += ev.Text
		case Stopped:
			stopped = ev.Res
		}
		return out.Write(ev)
	})
	out.Close()
	if err != nil {
		t.Fatal(err)
	}
	if said[0] != "a" || said[1] != "b" || stopped == nil || len(stopped.More) != 1 || stopped.Stop != StopEnd || stopped.More[0].Choice != 1 || stopped.More[0].Stop != StopTool || stopped.More[0].ToolCalls[0].Name != "f" || stopped.Usage.Out != 3 {
		t.Fatalf("said %v stopped %+v", said, stopped)
	}
	raws, chunks := streamChunks(t, w.Body.String())
	finish := map[int]string{}
	roles := map[int]int{}
	for _, c := range chunks {
		for _, ch := range c.Choices {
			if ch.Delta != nil && ch.Delta.Role == "assistant" {
				roles[ch.Index]++
			}
			if ch.FinishReason != nil {
				finish[ch.Index] = *ch.FinishReason
			}
		}
	}
	if finish[0] != "stop" || finish[1] != "tool_calls" || roles[0] != 1 || roles[1] != 1 || chunks[len(chunks)-1].Usage == nil {
		t.Fatalf("chunks %v", raws)
	}
}

// Answers from separate calls join into one answer, each call's tokens a round of its choice
func TestCombineChoices(t *testing.T) {
	joined := Combine([]*Result{
		{ID: "a", Model: "m", Text: "one", Usage: Usage{In: 3, Out: 1, Cached: 2}},
		{ID: "b", Model: "m", Text: "two", Stop: StopLength, Usage: Usage{In: 3, Out: 5}},
	})
	if joined.ID != "a" || joined.Usage.In != 6 || joined.Usage.Out != 6 || joined.Usage.Cached != 2 || len(joined.More) != 1 || joined.More[0].Choice != 1 || joined.More[0].Text != "two" || joined.More[0].Usage.In != 0 {
		t.Fatalf("joined %+v", joined)
	}
	if r := joined.Usage.Rounds; len(r) != 2 || r[0] != (Round{In: 3, Out: 1, Choice: 0}) || r[1] != (Round{In: 3, Out: 5, Choice: 1}) {
		t.Fatalf("rounds %+v", r)
	}
	if !(openai{}).Traits().Choices || (anthropic{}).Traits().Choices || (ollama{}).Traits().Choices {
		t.Fatal("only the openai protocol answers several choices a call")
	}
	w := httptest.NewRecorder()
	out := (openai{}).Answer(w, &Request{Kind: Generate}, "m")
	for _, r := range joined.All() {
		out.Write(Event{Kind: Said, Text: r.Text, Choice: r.Choice})
	}
	out.Write(Event{Kind: Stopped, Res: joined})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"index":1,"text":"two","finish_reason":"length"`) {
		t.Fatalf("completions %s", w.Body.String())
	}
}

// A streamed answer of many small chunks, the shape every token of a chat stream takes
func BenchmarkParseStream(b *testing.B) {
	var sse strings.Builder
	for i := range 2000 {
		sse.WriteString(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m","system_fingerprint":"fp","choices":[{"index":0,"delta":{"content":"tok` + strings.Repeat("x", i%7) + `"},"finish_reason":null}]}` + "\n\n")
	}
	sse.WriteString("data: [DONE]\n\n")
	body := sse.String()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if err := (openai{}).ParseStream(strings.NewReader(body), func(Event) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}
