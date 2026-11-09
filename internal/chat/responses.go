package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/tools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The OpenAI Responses API, passed to a runtime serving it or answered over the same exchange as chat completions
const OpenAIResponses = "/v1/responses"

// Reads a Responses API request into the neutral form, instructions leading as a system turn
func parseResponses(body []byte) (*Request, error) {
	var req respRequest
	if err := decodeRequest(body, req.read); err != nil {
		return nil, bad("%v", err)
	}
	c := &Request{Kind: Chat, Responses: true, Model: req.Model, MaxTokens: req.MaxOutputTokens, Temperature: req.Temperature, TopP: req.TopP, Stream: req.Stream, Extra: req.Extra, Origin: originOf(OpenAIResponses, body, req.model, req.Model)}
	refuse := func(err error) {
		if c.Origin.Untranslatable == nil {
			c.Origin.Untranslatable = err
		}
	}
	stored := req.PreviousResponseID != "" || present(req.Conversation) || present(req.Prompt)
	if req.PreviousResponseID != "" {
		refuse(bad("previous_response_id names a stored response, which only a runtime serving the Responses API keeps, so send the whole input"))
	}
	if present(req.Conversation) {
		refuse(bad("conversation names stored state, which only a runtime serving the Responses API keeps, so send the whole input"))
	}
	if present(req.Prompt) {
		refuse(bad("prompt names a stored prompt template, which only a runtime serving the Responses API keeps"))
	}
	if req.Background {
		refuse(bad("background responses need stored state, which only a runtime serving the Responses API keeps"))
	}
	if req.Store != nil && *req.Store {
		refuse(bad("store asks for the response to be kept, which only a runtime serving the Responses API does"))
	}
	if !req.HasInput {
		if !stored {
			return nil, bad("input is required")
		}
		refuse(bad("input is required when the request is translated"))
	}
	if req.Effort != "" {
		c.Extra = mergeExtra(c.Extra, map[string]json.RawMessage{"reasoning_effort": appendStr(nil, req.Effort)})
	}
	format, err := responseFormat(req.Format)
	if err != nil {
		return nil, err
	}
	if format != nil {
		c.Extra = mergeExtra(c.Extra, map[string]json.RawMessage{"response_format": format})
	}
	if req.Verbosity != "" {
		c.Extra = mergeExtra(c.Extra, map[string]json.RawMessage{"verbosity": appendStr(nil, req.Verbosity)})
	}
	if req.Instructions != "" {
		c.Messages = append(c.Messages, Message{Role: "system", Parts: []Part{{Type: "text", Text: req.Instructions}}})
	}
	messages, err := respMessages(&req, refuse)
	if err != nil {
		return nil, err
	}
	c.Messages = append(c.Messages, messages...)
	if c.Tools, c.Sources, err = respTools(req.Tools, refuse); err != nil {
		return nil, err
	}
	if slices.Contains(req.Include, "message.output_text.logprobs") {
		refuse(bad("include asks for logprobs, which only a runtime serving the Responses API returns"))
	}
	if req.Truncation == "auto" {
		refuse(bad("truncation auto drops input to fit the context, which only a runtime serving the Responses API does"))
	}
	if cap := req.MaxToolCalls; cap != nil {
		capToolCalls(c, *cap, refuse)
	}
	if c.ToolChoice, err = respToolChoice(req.ToolChoice); err != nil {
		return nil, err
	}
	return c, nil
}

// The chat completions response_format a text format asks for, nil for plain text
func responseFormat(format *respFormat) (json.RawMessage, error) {
	if format == nil {
		return nil, nil
	}
	switch format.Type {
	case "", "text":
		return nil, nil
	case "json_object":
		return json.RawMessage(`{"type":"json_object"}`), nil
	case "json_schema":
		if len(format.Schema) == 0 {
			return nil, bad("text.format of type json_schema needs a schema")
		}
		b := []byte(`{"type":"json_schema","json_schema":`)
		o := begin(&b)
		o.str("name", format.Name)
		o.raw("schema", format.Schema)
		if format.Strict != nil {
			o.boolean("strict", *format.Strict)
		}
		o.close(nil)
		return append(b, '}'), nil
	}
	return nil, bad("text.format type %q is not text, json_object, or json_schema", format.Type)
}

// Turns from a string or items, tools the gateway ran left out and items only a native runtime takes refused
func respMessages(req *respRequest, refuse func(error)) ([]Message, error) {
	if req.InputText != nil {
		return []Message{{Role: "user", Parts: []Part{{Type: "text", Text: *req.InputText}}}}, nil
	}
	var out []Message
	var thinking []Part
	// Adds parts to the open assistant turn, or opens one led by the reasoning written before it
	assistant := func(parts []Part, calls []ToolCall, extra map[string]json.RawMessage) {
		if n := len(out); n > 0 && out[n-1].Role == "assistant" {
			out[n-1].Parts = append(out[n-1].Parts, parts...)
			out[n-1].ToolCalls = append(out[n-1].ToolCalls, calls...)
			out[n-1].Extra = mergeExtra(out[n-1].Extra, extra)
			return
		}
		out = append(out, Message{Role: "assistant", Parts: append(thinking, parts...), ToolCalls: calls, Extra: extra})
		thinking = nil
	}
	for i, item := range req.Input {
		kind := item.Type
		if kind == "" && item.Role != "" {
			kind = "message"
		}
		switch kind {
		case "message":
			role := item.Role
			switch role {
			case "developer":
				role = "system"
			case "system", "user", "assistant":
			default:
				return nil, bad("input item %d has role %q, want user, assistant, system, or developer", i+1, item.Role)
			}
			parts, err := respParts(item.Content, i, refuse)
			if err != nil {
				return nil, err
			}
			if role == "assistant" {
				assistant(parts, nil, item.Extra)
				continue
			}
			if len(thinking) > 0 {
				assistant(nil, nil, nil)
			}
			out = append(out, Message{Role: role, Parts: parts, Extra: item.Extra})
		case "function_call":
			if item.Name == "" {
				return nil, bad("input item %d is a function call without a name", i+1)
			}
			id := item.CallID
			if id == "" {
				id = "call_" + randomID()
			}
			assistant(nil, []ToolCall{{ID: id, Name: item.Name, Args: item.Arguments}}, item.Extra)
		case "function_call_output":
			if item.CallID == "" {
				return nil, bad("input item %d is a function call output without a call_id", i+1)
			}
			if len(thinking) > 0 {
				assistant(nil, nil, nil)
			}
			parts, err := respOutput(item.Output, i, refuse)
			if err != nil {
				return nil, err
			}
			out = append(out, Message{Role: "tool", ToolID: item.CallID, Parts: parts, Extra: item.Extra})
		case "reasoning":
			if text := strings.Join(item.Summary, "\n\n"); text != "" {
				thinking = append(thinking, Part{Type: "thinking", Text: text})
			}
		case "web_search_call", "mcp_call":
			continue
		case "item_reference":
			refuse(bad("input item %d refers to a stored item, which only a runtime serving the Responses API keeps, so send the item itself", i+1))
		default:
			refuse(bad("input item %d has type %q, which only a runtime serving the Responses API takes", i+1, item.Type))
		}
	}
	if len(thinking) > 0 {
		assistant(nil, nil, nil)
	}
	return out, nil
}

// A message's content as parts, a string or a list of text and image parts
func respParts(content *respContent, i int, refuse func(error)) ([]Part, error) {
	if content == nil {
		return nil, nil
	}
	if !content.List {
		if content.Text == "" {
			return nil, nil
		}
		return []Part{{Type: "text", Text: content.Text}}, nil
	}
	var out []Part
	for _, p := range content.Parts {
		switch p.Type {
		case "input_text", "output_text":
			out = append(out, Part{Type: "text", Text: p.Text, Extra: p.Extra})
		case "refusal":
			out = append(out, Part{Type: "text", Text: p.Refusal, Extra: p.Extra})
		case "input_image":
			switch {
			case p.ImageURL != "":
				part := Part{Type: "image", URL: p.ImageURL, Detail: p.Detail, Extra: p.Extra}
				if mt, data, ok := DataURL(part.URL); ok {
					part.MediaType, part.Data, part.URL = mt, data, ""
				}
				out = append(out, part)
			case p.FileID != "":
				refuse(bad("input item %d: an image by file_id names a stored file, which only a runtime serving the Responses API keeps, so send image_url", i+1))
			default:
				return nil, bad("input item %d: an input_image part needs image_url or file_id", i+1)
			}
		default:
			refuse(bad("input item %d: %s parts are content only a runtime serving the Responses API takes", i+1, p.Type))
		}
	}
	return out, nil
}

// A function call's output as parts, a string or a list of parts
func respOutput(output *respContent, i int, refuse func(error)) ([]Part, error) {
	if output != nil && !output.List {
		return []Part{{Type: "text", Text: output.Text}}, nil
	}
	return respParts(output, i, refuse)
}

// Tools by schema, web search as a tool the gateway runs, and mcp servers as sources to connect
func respTools(list []respTool, refuse func(error)) ([]Tool, []Source, error) {
	var out []Tool
	var sources []Source
	searched := false
	for _, t := range list {
		switch {
		case t.Type == "function":
			if t.Name == "" {
				return nil, nil, bad("a function tool needs a name")
			}
			out = append(out, Tool{Name: t.Name, Description: t.Description, Schema: t.Parameters, Strict: t.Strict, Extra: t.Extra})
		case t.Type == "web_search" || strings.HasPrefix(t.Type, "web_search_"):
			if searched {
				return nil, nil, bad("a request takes one web search tool")
			}
			searched = true
			limits := &Provided{Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, MaxUses: ProvidedUses, Allowed: t.Domains, Country: t.Country}
			out = append(out, Tool{Type: t.Type, Name: tools.SearchTool, Provided: limits, Extra: t.Extra})
		case t.Type == "mcp":
			if t.ServerLabel == "" || t.ServerURL == "" {
				return nil, nil, bad("an mcp tool needs server_label and server_url")
			}
			src := Source{Name: t.ServerLabel, URL: t.ServerURL, Token: t.Auth, Allowed: t.Allowed}
			if src.Token == "" {
				src.Token = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(t.Headers["Authorization"], "Bearer "), "bearer "))
			}
			sources = append(sources, src)
		case t.Type == "custom":
			if t.Name == "" {
				return nil, nil, bad("a custom tool needs a name")
			}
			out = append(out, Tool{Type: "custom", Name: t.Name, Description: t.Description, Extra: t.Extra})
		default:
			refuse(bad("tool type %q is one only a runtime serving the Responses API provides, the gateway providing function, web_search, mcp, and custom", t.Type))
		}
	}
	return out, sources, nil
}

// Holds the tools the gateway runs to max_tool_calls, none at zero, refusing a cap across mcp calls it cannot count
func capToolCalls(c *Request, limit int, refuse func(error)) {
	if limit <= 0 {
		c.Sources = nil
		c.Tools = slices.DeleteFunc(c.Tools, func(t Tool) bool { return t.Provided != nil })
		return
	}
	if len(c.Sources) > 0 {
		refuse(bad("max_tool_calls caps mcp calls, which only a runtime serving the Responses API counts"))
	}
	for _, t := range c.Tools {
		if t.Provided != nil {
			t.Provided.MaxUses = min(t.Provided.MaxUses, limit)
		}
	}
}

// The neutral tool choice, a named function or a mode
func respToolChoice(tc *toolChoiceWire) (string, error) {
	if tc == nil {
		return "", nil
	}
	if !tc.object {
		switch tc.mode {
		case "auto", "none", "required":
			return tc.mode, nil
		}
		return "", bad("tool_choice %q is not auto, none, or required", tc.mode)
	}
	switch tc.kind {
	case "function", "custom":
		if tc.name == "" {
			return "", bad("tool_choice of type %s needs a name", tc.kind)
		}
		return tc.name, nil
	case "allowed_tools":
		if tc.allowed == "required" {
			return "required", nil
		}
		return "auto", nil
	}
	return "required", nil
}

// The usage object of a response
func respUsage(u Usage) map[string]any {
	return map[string]any{
		"input_tokens":          u.In,
		"output_tokens":         u.Out,
		"total_tokens":          u.In + u.Out,
		"input_tokens_details":  map[string]any{"cached_tokens": u.Cached},
		"output_tokens_details": map[string]any{"reasoning_tokens": 0},
	}
}

// The status a stop reason gives a response, and why it is incomplete when it is
func respStatus(stop string) (string, map[string]any) {
	switch stop {
	case StopLength:
		return "incomplete", map[string]any{"reason": "max_output_tokens"}
	case StopFilter:
		return "incomplete", map[string]any{"reason": "content_filter"}
	}
	return "completed", nil
}

// The response object around a list of output items
func respObject(id, model string, created int64, status string, incomplete map[string]any, output []any, usage Usage, extra map[string]json.RawMessage) map[string]any {
	out := map[string]any{
		"id":                  id,
		"object":              "response",
		"created_at":          created,
		"status":              status,
		"model":               model,
		"output":              output,
		"usage":               respUsage(usage),
		"error":               nil,
		"incomplete_details":  incomplete,
		"parallel_tool_calls": true,
		"store":               false,
		"tool_choice":         "auto",
		"tools":               []any{},
		"truncation":          "disabled",
		"text":                map[string]any{"format": map[string]any{"type": "text"}},
	}
	for k, v := range extra {
		if _, taken := out[k]; !taken {
			out[k] = v
		}
	}
	return out
}

func respText(id, text string) map[string]any {
	return map[string]any{"type": "message", "id": id, "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}

func respReasoning(id, text string) map[string]any {
	return map[string]any{"type": "reasoning", "id": id, "summary": []any{map[string]any{"type": "summary_text", "text": text}}}
}

func respCall(id string, call ToolCall) map[string]any {
	return map[string]any{"type": "function_call", "id": id, "call_id": call.ID, "name": call.Name, "arguments": call.Args, "status": "completed"}
}

// A tool the gateway ran as the API shows one, a web search, a page opened, or an mcp call
func respRun(run *Run) map[string]any {
	args, ok := jsonArgs(run.Call.Args)
	if !ok {
		args = json.RawMessage("{}")
	}
	status := "completed"
	if run.Result.Failed || run.Result.Code != "" {
		status = "failed"
	}
	switch {
	case run.Kind == v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH && !run.AdHoc:
		var in struct {
			Query string `json:"query"`
		}
		json.Unmarshal(args, &in)
		sources := make([]any, 0, len(run.Result.Hits))
		for _, h := range run.Result.Hits {
			sources = append(sources, map[string]any{"type": "url", "url": h.URL})
		}
		return map[string]any{"type": "web_search_call", "id": "ws_" + randomID(), "status": status, "action": map[string]any{"type": "search", "query": strings.TrimSpace(in.Query), "sources": sources}}
	case run.Kind == v1.ToolServerKind_TOOL_SERVER_KIND_FETCH && !run.AdHoc:
		var in struct {
			URL string `json:"url"`
		}
		json.Unmarshal(args, &in)
		return map[string]any{"type": "web_search_call", "id": "ws_" + randomID(), "status": status, "action": map[string]any{"type": "open_page", "url": strings.TrimSpace(in.URL)}}
	}
	item := map[string]any{"type": "mcp_call", "id": "mcp_" + randomID(), "server_label": run.Server, "name": run.Tool, "arguments": string(args), "output": run.Result.Text(), "error": nil}
	if status == "failed" {
		item["error"] = run.Result.Text()
		if run.Result.Code != "" {
			item["error"] = run.Result.Code
		}
	}
	return item
}

// A whole answer in the Responses shape
func (openai) renderResponses(c *Request, r *Result) ([]byte, error) {
	var output []any
	for _, run := range r.Runs {
		output = append(output, respRun(run))
	}
	if r.Thinking != "" {
		output = append(output, respReasoning("rs_"+randomID(), r.Thinking))
	}
	if r.Text != "" || len(r.ToolCalls) == 0 {
		output = append(output, respText("msg_"+randomID(), r.Text))
	}
	for _, call := range r.ToolCalls {
		if _, ok := jsonArgs(call.Args); !ok {
			return nil, &ToolArgsError{Name: call.Name}
		}
		output = append(output, respCall("fc_"+randomID(), call))
	}
	status, incomplete := respStatus(r.Stop)
	return json.Marshal(respObject(respID(r), r.Model, time.Now().Unix(), status, incomplete, output, r.Usage, r.Extra))
}

// The response id, the upstream's under the API's prefix or a fresh one
func respID(r *Result) string {
	switch {
	case strings.HasPrefix(r.ID, "resp_"):
		return r.ID
	case r.ID != "":
		return "resp_" + strings.TrimPrefix(r.ID, "chatcmpl-")
	}
	return "resp_" + randomID()
}

// Writes a response as the API streams it, one numbered event per change
type respStream struct {
	live    *liveWriter
	req     *Request
	id      string
	model   string
	created int64
	seq     int
	// The finished output items, and the open one with its kind and id
	output []any
	kind   string
	itemID string
	// The open tool call's stream index, and the indexes already closed
	tool  int
	done  map[int]bool
	text  strings.Builder
	tools toolGather
}

// Opens the stream with response.created, as the API does, pings following while the model works
func (o openai) answerResponses(w http.ResponseWriter, req *Request, model string) Writer {
	if !req.Stream {
		return &whole{w: w, req: req, model: model, client: o, render: o.renderResponses}
	}
	streamHeaders(w, "text/event-stream")
	s := &respStream{live: newLiveWriter(w, pingComment), req: req, id: "resp_" + randomID(), model: model, created: time.Now().Unix(), done: map[int]bool{}}
	s.event("response.created", map[string]any{"response": s.response("in_progress", nil, Usage{}, nil)})
	s.event("response.in_progress", map[string]any{"response": s.response("in_progress", nil, Usage{}, nil)})
	return s
}

// The response object as it stands, the open item left out
func (s *respStream) response(status string, incomplete map[string]any, usage Usage, extra map[string]json.RawMessage) map[string]any {
	output := s.output
	if output == nil {
		output = []any{}
	}
	return respObject(s.id, s.model, s.created, status, incomplete, output, usage, extra)
}

// Writes one event with its type and sequence number inside
func (s *respStream) event(kind string, fields map[string]any) error {
	fields["type"], fields["sequence_number"] = kind, s.seq
	s.seq++
	return s.live.sse(kind, fields)
}

// The output index the open item has, or the next item will
func (s *respStream) index() int { return len(s.output) }

// Opens an output item of the kind
func (s *respStream) open(kind string, item map[string]any) error {
	s.kind, s.itemID = kind, item["id"].(string)
	if err := s.event("response.output_item.added", map[string]any{"output_index": s.index(), "item": item}); err != nil {
		return err
	}
	switch kind {
	case "message":
		return s.event("response.content_part.added", map[string]any{"item_id": s.itemID, "output_index": s.index(), "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
	case "reasoning":
		return s.event("response.reasoning_summary_part.added", map[string]any{"item_id": s.itemID, "output_index": s.index(), "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
	}
	return nil
}

// Closes the open item, writing it finished into the output
func (s *respStream) close() error {
	if s.kind == "" {
		return nil
	}
	kind, id, text := s.kind, s.itemID, s.text.String()
	s.kind, s.itemID = "", ""
	s.text.Reset()
	var item map[string]any
	var err error
	switch kind {
	case "message":
		item = respText(id, text)
		if err = s.event("response.output_text.done", map[string]any{"item_id": id, "output_index": s.index(), "content_index": 0, "text": text, "logprobs": []any{}}); err != nil {
			return err
		}
		if err = s.event("response.content_part.done", map[string]any{"item_id": id, "output_index": s.index(), "content_index": 0, "part": item["content"].([]any)[0]}); err != nil {
			return err
		}
	case "reasoning":
		item = respReasoning(id, text)
		if err = s.event("response.reasoning_summary_text.done", map[string]any{"item_id": id, "output_index": s.index(), "summary_index": 0, "text": text}); err != nil {
			return err
		}
		if err = s.event("response.reasoning_summary_part.done", map[string]any{"item_id": id, "output_index": s.index(), "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": text}}); err != nil {
			return err
		}
	case "function_call":
		s.done[s.tool] = true
		call := *s.tools.calls[s.tool]
		if call.Args == "" {
			call.Args = "{}"
		}
		if _, ok := jsonArgs(call.Args); !ok {
			return &ToolArgsError{Name: call.Name}
		}
		item = respCall(id, call)
		if err = s.event("response.function_call_arguments.done", map[string]any{"item_id": id, "output_index": s.index(), "name": call.Name, "arguments": call.Args}); err != nil {
			return err
		}
	}
	if err := s.event("response.output_item.done", map[string]any{"output_index": s.index(), "item": item}); err != nil {
		return err
	}
	s.output = append(s.output, item)
	return nil
}

func (s *respStream) Write(ev Event) error {
	if err := oneChoice(ev, "Responses"); err != nil {
		return err
	}
	switch ev.Kind {
	case Said:
		if s.kind != "message" {
			if err := s.close(); err != nil {
				return err
			}
			if err := s.open("message", map[string]any{"type": "message", "id": "msg_" + randomID(), "status": "in_progress", "role": "assistant", "content": []any{}}); err != nil {
				return err
			}
		}
		s.text.WriteString(ev.Text)
		return s.event("response.output_text.delta", map[string]any{"item_id": s.itemID, "output_index": s.index(), "content_index": 0, "delta": ev.Text, "logprobs": []any{}})
	case Thought:
		if s.kind != "reasoning" {
			if err := s.close(); err != nil {
				return err
			}
			if err := s.open("reasoning", map[string]any{"type": "reasoning", "id": "rs_" + randomID(), "summary": []any{}}); err != nil {
				return err
			}
		}
		s.text.WriteString(ev.Text)
		return s.event("response.reasoning_summary_text.delta", map[string]any{"item_id": s.itemID, "output_index": s.index(), "summary_index": 0, "delta": ev.Text})
	case Called:
		s.tools.add(ev)
		if s.kind != "function_call" || s.tool != ev.Index {
			if s.done[ev.Index] {
				return fmt.Errorf("upstream sent more of tool call %d after moving on from it", ev.Index)
			}
			call := s.tools.calls[ev.Index]
			if call.Name == "" {
				return fmt.Errorf("upstream started tool call %d without naming the tool", ev.Index)
			}
			if call.ID == "" {
				call.ID = "call_" + randomID()
			}
			if err := s.close(); err != nil {
				return err
			}
			s.tool = ev.Index
			if err := s.open("function_call", map[string]any{"type": "function_call", "id": "fc_" + randomID(), "call_id": call.ID, "name": call.Name, "arguments": "", "status": "in_progress"}); err != nil {
				return err
			}
		}
		if ev.Tool.Args == "" {
			return nil
		}
		return s.event("response.function_call_arguments.delta", map[string]any{"item_id": s.itemID, "output_index": s.index(), "delta": ev.Tool.Args})
	case Ran:
		if err := s.close(); err != nil {
			return err
		}
		item := respRun(ev.Run)
		if err := s.event("response.output_item.added", map[string]any{"output_index": s.index(), "item": item}); err != nil {
			return err
		}
		if err := s.event("response.output_item.done", map[string]any{"output_index": s.index(), "item": item}); err != nil {
			return err
		}
		s.output = append(s.output, item)
		return nil
	case Stopped:
		if err := s.close(); err != nil {
			return err
		}
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		if len(s.output) == 0 {
			item := respText("msg_"+randomID(), "")
			if err := s.event("response.output_item.added", map[string]any{"output_index": 0, "item": item}); err != nil {
				return err
			}
			if err := s.event("response.output_item.done", map[string]any{"output_index": 0, "item": item}); err != nil {
				return err
			}
			s.output = append(s.output, item)
		}
		status, incomplete := respStatus(StopOf(res.Stop, len(s.tools.order)))
		kind := "response.completed"
		if status == "incomplete" {
			kind = "response.incomplete"
		}
		return s.event(kind, map[string]any{"response": s.response(status, incomplete, res.Usage, res.Extra)})
	case Failed:
		fields := respFailure(*ev.Failure)
		resp := s.response("failed", nil, Usage{}, nil)
		resp["error"] = map[string]any{"code": fields["code"], "message": fields["message"]}
		if err := s.event("response.failed", map[string]any{"response": resp}); err != nil {
			return err
		}
		return s.event("error", fields)
	}
	return nil
}

// The fields of the error event a Responses stream ends with, its type and sequence number left to the writer
func respFailure(f Failure) map[string]any {
	code := f.Code
	if code == "" {
		code = CodeUpstream
	}
	return map[string]any{"code": code, "message": f.Message, "param": nil}
}

func (s *respStream) Close() error {
	s.live.close()
	return nil
}
