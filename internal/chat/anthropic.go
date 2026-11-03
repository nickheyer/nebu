package chat

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nickheyer/nebu/internal/tools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Anthropic endpoints
const (
	AnthropicMessages  = "/v1/messages"
	AnthropicCount     = "/v1/messages/count_tokens"
	anthropicMessages  = AnthropicMessages
	anthropicMaxTokens = 4096
	// Marks snippets the gateway sealed into encrypted_content
	sealTag = "nebu\x00"
	// Searches or fetches one request may run when the client sets no max_uses
	ProvidedUses = 10
)

// The Anthropic Messages protocol
type anthropic struct{}

// The server tools the gateway stands in for, by the name each is called
var providedKinds = map[string]v1.ToolServerKind{tools.SearchTool: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, tools.FetchTool: v1.ToolServerKind_TOOL_SERVER_KIND_FETCH}

// A text block written to clients, which always carries its text field
type antText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// A thinking block written to clients, its signature empty since no runtime here signs reasoning
type antThinking struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

// Fresh plus cached tokens, the whole prompt
func (u antUsage) prompt() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// The tokens of one model round, as the API lists them under usage
type antIteration struct {
	Type                     string `json:"type"`
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int    `json:"cache_read_input_tokens"`
}

// The rounds a usage object lists, cached prompt tokens counted into each round's prompt
func roundsOf(u antUsage) []Round {
	var out []Round
	for _, it := range u.Iterations {
		out = append(out, Round{In: it.InputTokens + it.CacheCreationInputTokens + it.CacheReadInputTokens, Out: it.OutputTokens})
	}
	return out
}

// Server tool counts in a usage object
type antServerToolUse struct {
	WebSearchRequests int `json:"web_search_requests"`
	WebFetchRequests  int `json:"web_fetch_requests,omitempty"`
}

// The usage of a result, input excluding what the cache served, with any server tools it ran
func antUsageOf(r *Result) antUsage {
	u := antUsage{InputTokens: r.Usage.In, OutputTokens: r.Usage.Out}
	if r.Usage.Cached > 0 && r.Usage.Cached <= r.Usage.In {
		u.InputTokens, u.CacheReadInputTokens = r.Usage.In-r.Usage.Cached, r.Usage.Cached
	}
	if len(r.Usage.Rounds) > 1 {
		for _, it := range r.Usage.Rounds {
			u.Iterations = append(u.Iterations, antIteration{Type: "message", InputTokens: it.In, OutputTokens: it.Out})
		}
	}
	searches, fetches := r.RunsOf(v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH), r.RunsOf(v1.ToolServerKind_TOOL_SERVER_KIND_FETCH)
	if searches > 0 || fetches > 0 {
		u.ServerToolUse = &antServerToolUse{WebSearchRequests: searches, WebFetchRequests: fetches}
	}
	return u
}

// A search result block written to the client, the snippet sealed into encrypted_content
type antSearchResult struct {
	Type             string  `json:"type"`
	Title            string  `json:"title"`
	URL              string  `json:"url"`
	EncryptedContent string  `json:"encrypted_content"`
	PageAge          *string `json:"page_age"`
}

// The block that stands in for a server tool's result when the call did not happen
type antToolError struct {
	Type      string `json:"type"`
	ErrorCode string `json:"error_code"`
}

// A fetched page as the API writes it
type antFetchResult struct {
	Type        string           `json:"type"`
	URL         string           `json:"url"`
	Content     antFetchDocument `json:"content"`
	RetrievedAt string           `json:"retrieved_at"`
}

type antFetchDocument struct {
	Type   string `json:"type"`
	Title  string `json:"title,omitempty"`
	Source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

func (anthropic) Endpoint(path string) Kind {
	switch path {
	case AnthropicMessages:
		return Chat
	case AnthropicCount:
		return Count
	}
	return None
}

// Message ids the gateway hands out
func antID() string { return "msg_" + randomID() }

func (anthropic) Parse(path string, body []byte) (*Request, error) {
	if path != AnthropicMessages && path != AnthropicCount {
		return nil, bad("%s is not an endpoint the anthropic protocol serves", path)
	}
	var req antRequest
	if err := decodeRequest(body, req.read); err != nil {
		return nil, bad("%v", err)
	}
	c := &Request{Kind: Chat, Model: req.Model, MaxTokens: req.MaxTokens, Temperature: req.Temperature, TopP: req.TopP, TopK: req.TopK, Stop: req.StopSequences, Stream: req.Stream, Extra: req.Extra, Origin: originOf(path, body, req.model, req.Model)}
	if path == AnthropicCount {
		c.Kind, c.Stream = Count, false
	}
	blocks, err := req.System.blocks()
	if err != nil {
		return nil, err
	}
	system, err := antParts(blocks, false)
	if err != nil {
		return nil, err
	}
	for _, p := range system {
		if p.Type != "text" {
			return nil, bad("system takes text blocks, not %s", p.Type)
		}
	}
	if TextOf(system) != "" {
		c.Messages = append(c.Messages, Message{Role: "system", Parts: system})
	}
	for _, m := range req.Messages {
		turns, err := antTurns(m)
		if err != nil {
			return nil, err
		}
		c.Messages = append(c.Messages, turns...)
	}
	if c.Tools, err = antTools(req.Tools); err != nil {
		return nil, err
	}
	for _, s := range req.MCPServers {
		if s.Type != "url" {
			return nil, bad("mcp server %s has type %q, and url is the one the gateway connects", s.Name, s.Type)
		}
		if s.Name == "" || s.URL == "" {
			return nil, bad("an mcp server needs a name and a url")
		}
		if s.Disabled {
			continue
		}
		c.Sources = append(c.Sources, Source{Name: s.Name, URL: s.URL, Token: s.Authorization, Allowed: s.Allowed})
	}
	if req.ToolChoice != nil {
		switch req.ToolChoice.Type {
		case "any":
			c.ToolChoice = "required"
		case "tool":
			c.ToolChoice = req.ToolChoice.Name
		case "none", "auto":
			c.ToolChoice = req.ToolChoice.Type
		}
	}
	return c, nil
}

// The turns of one message, results as tool turns, what follows a user turn's last result joining it
func antTurns(m antMessage) ([]Message, error) {
	blocks, err := m.Content.blocks()
	if err != nil {
		return nil, err
	}
	var out []Message
	// Fields on the message object go with the first turn made from it
	extra := m.Extra
	msg := Message{Role: m.Role}
	for _, b := range blocks {
		switch b.Type {
		case "tool_use", "server_tool_use":
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: string(b.Input)})
		case "mcp_tool_use":
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: b.ID, Name: b.ServerName + tools.Join + b.Name, Args: string(b.Input)})
		case "tool_result", "web_search_tool_result", "web_fetch_tool_result", "mcp_tool_result":
			parts, err := antResultParts(b)
			if err != nil {
				return nil, err
			}
			if len(msg.Parts) > 0 || len(msg.ToolCalls) > 0 {
				msg.Extra, extra = extra, nil
				out = append(out, msg)
				msg = Message{Role: m.Role}
			}
			out = append(out, Message{Role: "tool", ToolID: b.ToolUseID, Parts: parts, Extra: extra})
			extra = nil
		default:
			parts, err := antParts([]antBlock{b}, m.Role == "assistant")
			if err != nil {
				return nil, err
			}
			msg.Parts = append(msg.Parts, parts...)
		}
	}
	if n := len(out); m.Role == "user" && n > 0 && out[n-1].Role == "tool" && len(msg.Parts) > 0 {
		out[n-1].Parts = JoinParts(out[n-1].Parts, msg.Parts)
		out[n-1].Extra = mergeExtra(out[n-1].Extra, extra)
		return out, nil
	}
	if len(msg.Parts) > 0 || len(msg.ToolCalls) > 0 {
		msg.Extra = extra
		out = append(out, msg)
	}
	return out, nil
}

// The parts a model reads for a result block a client sent back
func antResultParts(b antBlock) ([]Part, error) {
	switch b.Type {
	case "web_search_tool_result":
		text, err := searchResultText(&b.Content)
		if err != nil {
			return nil, err
		}
		return []Part{{Type: "text", Text: text}}, nil
	case "web_fetch_tool_result":
		text, err := fetchResultText(&b.Content)
		if err != nil {
			return nil, err
		}
		return []Part{{Type: "text", Text: text}}, nil
	case "mcp_tool_result":
		return mcpResultParts(b)
	}
	inner, err := b.Content.blocks()
	if err != nil {
		return nil, err
	}
	return antParts(inner, false)
}

// The tools a request offers upfront, server tools marked as provided and the tool search tool left out
func antTools(list []antTool) ([]Tool, error) {
	var out []Tool
	found := map[string]bool{}
	for _, t := range list {
		if strings.HasPrefix(t.Type, "tool_search_tool") {
			continue
		}
		tool := Tool{Type: t.Type, Name: t.Name, Description: t.Description, Schema: t.InputSchema, Strict: t.Strict, Extra: t.Extra}
		base := providedBase(t.Type)
		if base == "" {
			if _, reserved := providedKinds[t.Name]; reserved {
				return nil, bad("the name %s belongs to the %s tool", t.Name, strings.ReplaceAll(t.Name, "_", " "))
			}
			out = append(out, tool)
			continue
		}
		if found[base] {
			return nil, bad("a request takes one %s tool", strings.ReplaceAll(base, "_", " "))
		}
		found[base] = true
		if t.Name != base {
			return nil, bad("the %s tool is named %s", t.Type, base)
		}
		limits, err := providedLimits(t)
		if err != nil {
			return nil, err
		}
		limits.Kind = providedKinds[base]
		tool.Provided = limits
		out = append(out, tool)
	}
	return out, nil
}

// The base name of a server tool type, in any dated version the API has named, empty for other types
func providedBase(typ string) string {
	for base := range providedKinds {
		if typ == base || strings.HasPrefix(typ, base+"_") {
			return base
		}
	}
	return ""
}

// The limits a server tool definition carries, read in the same pass as the tool
func providedLimits(t antTool) (*Provided, error) {
	opts := t.limits
	limits := &Provided{MaxUses: ProvidedUses, Allowed: opts.Allowed, Blocked: opts.Blocked, Country: opts.Country}
	if opts.MaxUses != nil {
		if *opts.MaxUses < 1 {
			return nil, bad("%s max_uses must be at least 1", t.Name)
		}
		limits.MaxUses = *opts.MaxUses
	}
	if len(opts.Allowed) > 0 && len(opts.Blocked) > 0 {
		return nil, bad("%s takes allowed_domains or blocked_domains, not both", t.Name)
	}
	if opts.MaxTokens != nil {
		if *opts.MaxTokens < 1 {
			return nil, bad("%s max_content_tokens must be at least 1", t.Name)
		}
		limits.MaxChars = *opts.MaxTokens * 4
	}
	return limits, nil
}

// Turns blocks into parts, documents as text, opaque and reference blocks left out, the rest refused by name
func antParts(blocks []antBlock, assistant bool) ([]Part, error) {
	var out []Part
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, Part{Type: "text", Text: b.Text, Extra: b.Extra})
		case "image":
			if b.Source == nil {
				return nil, bad("an image block needs a source")
			}
			out = append(out, Part{Type: "image", MediaType: b.Source.MediaType, Data: b.Source.Data, URL: b.Source.URL, Extra: b.Extra})
		case "thinking":
			if !assistant {
				return nil, bad("thinking blocks belong in assistant turns")
			}
			out = append(out, Part{Type: "thinking", Text: b.Thinking, Data: b.Signature, Extra: b.Extra})
		case "document":
			out = append(out, Part{Type: "text", Text: documentText(b)})
		case "redacted_thinking", "tool_reference":
		default:
			return nil, bad("%s blocks are not content the gateway can pass to a model", b.Type)
		}
	}
	return out, nil
}

// The text a model reads for a document block, under its title when it has one
func documentText(b antBlock) string {
	var text string
	switch src := b.Source; {
	case src == nil:
	case src.Type == "text":
		text = src.Data
	case src.Type == "content":
		if blocks, err := src.Content.blocks(); err == nil {
			parts, _ := antParts(blocks, false)
			text = TextOf(parts)
		}
	case src.Type == "url":
		text = "[a document at " + src.URL + " the model cannot read]"
	default:
		text = "[a " + src.MediaType + " document the model cannot read]"
	}
	if b.Title != "" {
		return b.Title + "\n" + text
	}
	return text
}

// Renders parts as blocks, blank text left out because the API refuses it
func antBlocksOf(parts []Part) []antBlock {
	var out []antBlock
	for _, p := range parts {
		switch p.Type {
		case "text":
			if strings.TrimSpace(p.Text) != "" {
				out = append(out, antBlock{Type: "text", Text: p.Text, Extra: p.Extra})
			}
		case "image":
			src := &antSource{}
			if p.URL != "" {
				src.Type, src.URL = "url", p.URL
			} else {
				src.Type, src.MediaType, src.Data = "base64", p.MediaType, p.Data
			}
			out = append(out, antBlock{Type: "image", Source: src, Extra: p.Extra})
		case "thinking":
			out = append(out, antBlock{Type: "thinking", Thinking: p.Text, Signature: p.Data, Extra: p.Extra})
		}
	}
	return out
}

// Refuses a raw prompt, token ids, or several prompts, none of which a runtime templating every chat takes
func chatOnly(c *Request, protocol string) error {
	if c.Kind == Generate && c.Raw {
		return bad("the %s protocol puts every prompt in the model's chat template, so a raw prompt cannot reach it as written", protocol)
	}
	prompts := 0
	for _, m := range c.Messages {
		if hasTokens(m.Parts) {
			return bad("the %s protocol takes prompts as text, not token ids", protocol)
		}
		if m.Role == "user" {
			prompts++
		}
	}
	if c.Kind == Generate && prompts > 1 {
		return bad("the %s protocol takes one prompt a request, and this request holds %d", protocol, prompts)
	}
	return nil
}

func (anthropic) Render(c *Request) (string, []byte, error) {
	if err := translatable(c); err != nil {
		return "", nil, err
	}
	if c.Kind == Embed {
		return "", nil, bad("the anthropic protocol has no embeddings endpoint")
	}
	if err := choicesFit(anthropic{}, c, "anthropic"); err != nil {
		return "", nil, err
	}
	if err := chatOnly(c, "anthropic"); err != nil {
		return "", nil, err
	}
	req := antRequest{Model: c.Model, MaxTokens: c.MaxTokens, Temperature: c.Temperature, TopP: c.TopP, TopK: c.TopK, StopSequences: c.Stop, Stream: c.Stream, Extra: c.Extra}
	if req.MaxTokens == 0 {
		req.MaxTokens = anthropicMaxTokens
	}
	// The API samples with temperature or top_p, never both, and a top_p of 1 changes nothing
	if req.Temperature != nil && req.TopP != nil {
		if *req.TopP != 1 {
			return "", nil, bad("this model samples with temperature or top_p, not both")
		}
		req.TopP = nil
	}
	var system []antBlock
	for _, m := range c.Messages {
		if m.Role == "system" {
			for _, p := range m.Parts {
				if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
					system = append(system, antBlock{Type: "text", Text: p.Text, Extra: p.Extra})
				}
			}
		}
	}
	if len(system) > 0 {
		req.System = blocksContent(system)
	}
	// Adjacent same role turns merge, and tool results ride in user turns
	req.Messages = make([]antMessage, 0, len(c.Messages))
	push := func(role string, blocks []antBlock, extra map[string]json.RawMessage) {
		if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == role {
			last := &req.Messages[n-1]
			last.Content.Blocks = append(last.Content.Blocks, blocks...)
			for k, v := range extra {
				if _, taken := last.Extra[k]; !taken {
					last.Extra = mergeExtra(last.Extra, map[string]json.RawMessage{k: v})
				}
			}
			return
		}
		req.Messages = append(req.Messages, antMessage{Role: role, Content: blocksContent(blocks), Extra: extra})
	}
	for _, m := range c.Messages {
		switch m.Role {
		case "system":
		case "tool":
			block := antBlock{Type: "tool_result", ToolUseID: m.ToolID}
			if blocks := antBlocksOf(m.Parts); len(blocks) > 0 {
				block.Content = blocksContent(blocks)
			}
			push("user", []antBlock{block}, m.Extra)
		case "assistant":
			blocks := antBlocksOf(m.Parts)
			for _, t := range m.ToolCalls {
				input, ok := jsonArgs(t.Args)
				if !ok {
					return "", nil, bad("tool call %s has arguments that are not a JSON object", t.Name)
				}
				blocks = append(blocks, antBlock{Type: "tool_use", ID: t.ID, Name: t.Name, Input: input})
			}
			if len(blocks) > 0 {
				push("assistant", blocks, m.Extra)
			}
		default:
			if blocks := antBlocksOf(m.Parts); len(blocks) > 0 {
				push("user", blocks, m.Extra)
			}
		}
	}
	for _, t := range c.Tools {
		tool := antTool{Type: t.Type, Name: t.Name, Description: t.Description, InputSchema: t.Schema, Strict: t.Strict, Extra: t.Extra}
		// Tools defined by schema need one, and tools the API provides carry none
		if len(tool.InputSchema) == 0 && (t.Type == "" || t.Type == "custom") {
			tool.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		req.Tools = append(req.Tools, tool)
	}
	switch c.ToolChoice {
	case "":
	case "required":
		req.ToolChoice = &antToolChoice{Type: "any"}
	case "auto", "none":
		req.ToolChoice = &antToolChoice{Type: c.ToolChoice}
	default:
		req.ToolChoice = &antToolChoice{Type: "tool", Name: c.ToolChoice}
	}
	b := renderBuffer(c)
	if c.Kind == Count {
		req.MaxTokens, req.Temperature, req.TopP, req.TopK, req.StopSequences, req.Stream = 0, nil, nil, nil, nil, false
		req.appendTo(&b)
		return anthropic{}.Traits().TokenizePath, b, nil
	}
	req.appendTo(&b)
	return AnthropicMessages, b, nil
}

func antStop(reason string) string {
	switch reason {
	case "max_tokens":
		return StopLength
	case "tool_use":
		return StopTool
	}
	return StopEnd
}

func antReason(stop string) string {
	switch stop {
	case StopLength:
		return "max_tokens"
	case StopTool:
		return "tool_use"
	}
	return "end_turn"
}

func (anthropic) ParseResult(c *Request, body []byte) (*Result, error) {
	if c.Kind == Count {
		var count antUsage
		if err := decode(body, count.read); err != nil {
			return nil, malformed(err)
		}
		return &Result{Model: c.Model, Usage: Usage{In: count.InputTokens}}, nil
	}
	var resp antResponse
	if err := decode(body, resp.read); err != nil {
		return nil, malformed(err)
	}
	r := &Result{ID: resp.ID, Model: resp.Model, Usage: Usage{In: resp.Usage.prompt(), Out: resp.Usage.OutputTokens, Cached: resp.Usage.CacheReadInputTokens, Rounds: roundsOf(resp.Usage)}, Extra: resp.Extra}
	for _, b := range resp.Content {
		switch b.Type {
		case "text":
			r.Text += b.Text
		case "thinking":
			r.Thinking += b.Thinking
		case "tool_use":
			r.ToolCalls = append(r.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Args: string(b.Input)})
		}
	}
	if resp.StopReason != nil {
		r.Stop = antStop(*resp.StopReason)
	}
	r.Stop = StopOf(r.Stop, len(r.ToolCalls))
	return r, nil
}

// A tool call id the runtime gave, or one made up for a result to match
func antToolID(id string) string {
	if id != "" {
		return id
	}
	return "toolu_" + randomID()
}

// The calls with the ids they are written to the client under
func antToolIDs(calls []ToolCall) []ToolCall {
	out := make([]ToolCall, 0, len(calls))
	for _, c := range calls {
		c.ID = antToolID(c.ID)
		out = append(out, c)
	}
	return out
}

func (anthropic) ParseStream(rd io.Reader, emit func(Event) error) error {
	final := &Result{}
	var tools toolGather
	// Tool calls numbered in order of appearance, since content block indexes also count text blocks
	order := map[int]int{}
	err := ReadSSE(rd, func(event string, data []byte) error {
		var ev antEvent
		if err := decode(data, ev.read); err != nil {
			return malformed(err)
		}
		switch ev.Type {
		case "error":
			message := ev.ErrorMessage
			if message == "" {
				message = strings.TrimSpace(string(data))
			}
			return errors.New(message)
		case "message_start":
			if ev.Message != nil {
				final.ID, final.Model = ev.Message.ID, ev.Message.Model
				final.Usage.In, final.Usage.Cached = ev.Message.Usage.prompt(), ev.Message.Usage.CacheReadInputTokens
				final.Extra = mergeExtra(final.Extra, ev.Message.Extra)
			}
			return emit(Event{Kind: Started, Res: &Result{ID: final.ID, Model: final.Model, Usage: Usage{In: final.Usage.In, Cached: final.Usage.Cached}}})
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				n := len(order)
				order[ev.Index] = n
				e := Event{Kind: Called, Index: n, Tool: &ToolCall{ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}}
				tools.add(e)
				return emit(e)
			}
		case "content_block_delta":
			if ev.Delta == nil {
				return nil
			}
			if ev.Delta.Type == "input_json_delta" {
				n, ok := order[ev.Index]
				if !ok {
					return fmt.Errorf("tool input arrived for content block %d, which is not a tool call", ev.Index)
				}
				e := Event{Kind: Called, Index: n, Tool: &ToolCall{Args: ev.Delta.PartialJSON}}
				tools.add(e)
				return emit(e)
			}
			if ev.Delta.Type == "thinking_delta" && ev.Delta.Thinking != "" {
				return emit(Event{Kind: Thought, Text: ev.Delta.Thinking})
			}
			if ev.Delta.Text != "" {
				return emit(Event{Kind: Said, Text: ev.Delta.Text})
			}
		case "message_delta":
			if ev.Delta != nil {
				if ev.Delta.StopReason != nil {
					final.Stop = antStop(*ev.Delta.StopReason)
				}
				final.Extra = mergeExtra(final.Extra, ev.Delta.Extra)
			}
			if ev.Usage != nil {
				final.Usage.Out = ev.Usage.OutputTokens
				if ev.Usage.prompt() > 0 {
					final.Usage.In, final.Usage.Cached = ev.Usage.prompt(), ev.Usage.CacheReadInputTokens
				}
				if rounds := roundsOf(*ev.Usage); len(rounds) > 0 {
					final.Usage.Rounds = rounds
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	final.ToolCalls = tools.list()
	final.Stop = StopOf(final.Stop, len(final.ToolCalls))
	return emit(Event{Kind: Stopped, Res: final})
}

// Writes an Anthropic message block by block, streamed or whole at stop
type antWriter interface {
	// Writes a complete block, the input streamed as one delta when given
	block(b antBlock, input json.RawMessage) error
}

// Gathers a message's blocks and writes the message whole at stop
type antWhole struct {
	w      http.ResponseWriter
	req    *Request
	model  string
	blocks []any
	said   textGather
	tools  toolGather
}

// Writes the thinking and text gathered so far as blocks of their own
func (a *antWhole) flush() {
	if a.said.thinking.Len() > 0 {
		a.blocks = append(a.blocks, antThinking{Type: "thinking", Thinking: a.said.thinking.String()})
		a.said.thinking.Reset()
	}
	if a.said.text.Len() > 0 {
		a.blocks = append(a.blocks, antText{Type: "text", Text: a.said.text.String()})
		a.said.text.Reset()
	}
}

func (a *antWhole) Write(ev Event) error {
	if err := oneChoice(ev, "anthropic"); err != nil {
		return err
	}
	switch ev.Kind {
	case Said, Thought:
		a.said.add(ev)
	case Called:
		a.tools.add(ev)
	case Ran:
		return antRan(a, ev.Run)
	case Failed:
		anthropic{}.Error(a.w, *ev.Failure)
	case Stopped:
		if a.req.Kind == Count {
			n := 0
			if ev.Res != nil {
				n = ev.Res.Usage.In
			}
			WriteJSON(a.w, http.StatusOK, map[string]any{"input_tokens": n})
			return nil
		}
		a.flush()
		calls := antToolIDs(a.tools.list())
		for _, c := range calls {
			input, ok := jsonArgs(c.Args)
			if !ok {
				return &ToolArgsError{Name: c.Name}
			}
			a.blocks = append(a.blocks, antBlock{Type: "tool_use", ID: c.ID, Name: c.Name, Input: input})
		}
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		if a.blocks == nil {
			a.blocks = []any{}
		}
		reply := antReply{ID: antID(), Model: a.model, Content: a.blocks, StopReason: ptr(antReason(StopOf(res.Stop, len(a.tools.order)))), Usage: antUsageOf(res), Extra: res.Extra}
		data, err := reply.MarshalJSON()
		if err != nil {
			return err
		}
		a.w.Header().Set("Content-Type", "application/json")
		a.w.WriteHeader(http.StatusOK)
		_, err = a.w.Write(data)
		return err
	}
	return nil
}

func (a *antWhole) block(b antBlock, input json.RawMessage) error {
	a.flush()
	if len(input) > 0 {
		b.Input = input
	}
	a.blocks = append(a.blocks, b)
	return nil
}

func (*antWhole) Close() error { return nil }

// Writes a tool the gateway ran as server tool blocks for search and fetch and MCP blocks for the rest
func antRan(w antWriter, run *Run) error {
	input, ok := jsonArgs(run.Call.Args)
	if !ok {
		input = json.RawMessage("{}")
	}
	switch {
	case run.Kind == v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH && !run.AdHoc:
		id := "srvtoolu_" + randomID()
		if err := w.block(antBlock{Type: "server_tool_use", ID: id, Name: tools.SearchTool, Input: json.RawMessage("{}")}, serverToolInput(input, "query")); err != nil {
			return err
		}
		return w.block(antBlock{Type: "web_search_tool_result", ToolUseID: id, Content: rawContent(searchContent(run.Result))}, nil)
	case run.Kind == v1.ToolServerKind_TOOL_SERVER_KIND_FETCH && !run.AdHoc:
		id := "srvtoolu_" + randomID()
		if err := w.block(antBlock{Type: "server_tool_use", ID: id, Name: tools.FetchTool, Input: json.RawMessage("{}")}, serverToolInput(input, "url")); err != nil {
			return err
		}
		return w.block(antBlock{Type: "web_fetch_tool_result", ToolUseID: id, Content: rawContent(fetchContent(run.Result))}, nil)
	}
	id := "mcptoolu_" + randomID()
	if err := w.block(antBlock{Type: "mcp_tool_use", ID: id, Name: run.Tool, ServerName: run.Server, Input: json.RawMessage("{}")}, input); err != nil {
		return err
	}
	return w.block(antBlock{Type: "mcp_tool_result", ToolUseID: id, IsError: ptr(run.Result.Failed), Content: blocksContent(antBlocksOf(ResultParts(run.Result)))}, nil)
}

// The input of a server tool as the API writes it, its one field or empty
func serverToolInput(args json.RawMessage, field string) json.RawMessage {
	var in map[string]json.RawMessage
	json.Unmarshal(args, &in)
	var value string
	json.Unmarshal(in[field], &value)
	out, _ := json.Marshal(map[string]string{field: strings.TrimSpace(value)})
	return out
}

// A tool's answer as parts
func ResultParts(r *tools.Result) []Part {
	out := make([]Part, 0, len(r.Parts))
	for _, p := range r.Parts {
		out = append(out, Part{Type: p.Type, Text: p.Text, MediaType: p.MediaType, Data: p.Data})
	}
	return out
}

// The content of a web_search_tool_result, the results or the error that stood in for them
func searchContent(r *tools.Result) json.RawMessage {
	if r.Code != "" {
		data, _ := json.Marshal(antToolError{Type: "web_search_tool_result_error", ErrorCode: r.Code})
		return data
	}
	out := make([]antSearchResult, 0, len(r.Hits))
	for _, h := range r.Hits {
		res := antSearchResult{Type: "web_search_result", Title: h.Title, URL: h.URL, EncryptedContent: SealSnippet(h.Snippet)}
		if h.Age != "" {
			age := h.Age
			res.PageAge = &age
		}
		out = append(out, res)
	}
	data, _ := json.Marshal(out)
	return data
}

// The content of a web_fetch_tool_result, the page as a document or the error that stood in for it
func fetchContent(r *tools.Result) json.RawMessage {
	if r.Code != "" || r.Page == nil {
		code := r.Code
		if code == "" {
			code = "unavailable"
		}
		data, _ := json.Marshal(antToolError{Type: "web_fetch_tool_result_error", ErrorCode: code})
		return data
	}
	out := antFetchResult{Type: "web_fetch_result", URL: r.Page.URL, RetrievedAt: r.Page.RetrievedAt.UTC().Format(time.RFC3339)}
	out.Content.Type, out.Content.Title = "document", r.Page.Title
	out.Content.Source.Type, out.Content.Source.MediaType, out.Content.Source.Data = "text", "text/plain", r.Page.Text
	data, _ := json.Marshal(out)
	return data
}

// Seals a snippet into the opaque encrypted_content a client hands back in later turns
func SealSnippet(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(sealTag + s))
}

// Opens a snippet this gateway sealed
func OpenSnippet(s string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || !strings.HasPrefix(string(raw), sealTag) || !utf8.Valid(raw) {
		return "", false
	}
	return strings.TrimPrefix(string(raw), sealTag), true
}

// The text a model reads for a web_search_tool_result block a client sent back
func searchResultText(content *antContent) (string, error) {
	if content.kind == contentObject && content.Object.Type == "web_search_tool_result_error" {
		return tools.SearchText(nil, content.Object.ErrorCode), nil
	}
	if content.kind != contentList {
		return "", bad("web_search_tool_result content must be a list of results or an error")
	}
	hits := make([]tools.Hit, 0, len(content.Blocks))
	for _, r := range content.Blocks {
		snippet, ok := OpenSnippet(r.EncryptedContent)
		if !ok {
			return "", bad("web_search_tool_result carries content this gateway did not write")
		}
		hit := tools.Hit{Title: r.Title, URL: r.URL, Snippet: snippet}
		if r.PageAge != nil {
			hit.Age = *r.PageAge
		}
		hits = append(hits, hit)
	}
	return tools.SearchText(hits, ""), nil
}

// The text a model reads for a web_fetch_tool_result block a client sent back
func fetchResultText(content *antContent) (string, error) {
	if content.kind != contentObject {
		return "", bad("web_fetch_tool_result content must be a web_fetch_result or an error")
	}
	res := content.Object
	switch res.Type {
	case "web_fetch_tool_result_error":
		return tools.FetchText(nil, res.ErrorCode), nil
	case "web_fetch_result":
	default:
		return "", bad("web_fetch_tool_result content must be a web_fetch_result or an error")
	}
	page := &tools.Page{URL: res.URL}
	if doc := res.Content.Object; res.Content.kind == contentObject {
		page.Title = doc.Title
		if src := doc.Source; src != nil {
			page.Text = src.Data
			if src.Type == "base64" {
				page.Text = "[a " + src.MediaType + " document the model cannot read]"
			}
		}
	}
	return tools.FetchText(page, ""), nil
}

// The parts a model reads for an mcp_tool_result block a client sent back
func mcpResultParts(b antBlock) ([]Part, error) {
	if b.Content.kind == contentText {
		return []Part{{Type: "text", Text: b.Content.Text}}, nil
	}
	inner, err := b.Content.blocks()
	if err != nil {
		return nil, err
	}
	parts, err := antParts(inner, false)
	if err != nil {
		return nil, err
	}
	if b.IsError != nil && *b.IsError {
		parts = append([]Part{{Type: "text", Text: "The tool failed: "}}, parts...)
	}
	return parts, nil
}

type antStream struct {
	live *liveWriter
	// The open content block's index and kind, the kind empty between blocks
	index int
	kind  string
	// The stream index of the tool call in the open block, and the stream indexes already closed
	tool int
	done map[int]bool
	// Text that arrived while a tool block was open, written as its own block once that closes
	held  strings.Builder
	tools toolGather
}

// Opens a stream with message_start at once, as the API does, pings following while the model works
func (a anthropic) Answer(w http.ResponseWriter, req *Request, model string) Writer {
	if !req.Stream {
		return &antWhole{w: w, req: req, model: model}
	}
	streamHeaders(w, "text/event-stream")
	s := &antStream{live: newLiveWriter(w, antPing), index: -1, done: map[int]bool{}}
	msg := antReply{ID: antID(), Model: model, Content: []any{}}
	s.live.sse("message_start", map[string]any{"type": "message_start", "message": msg})
	return s
}

func antPing(w http.ResponseWriter) error {
	return writeSSE(w, "ping", map[string]any{"type": "ping"})
}

// Writes a whole block of its own, the input streamed as one delta the way the API sends it
func (s *antStream) block(b antBlock, input json.RawMessage) error {
	if err := s.settle(); err != nil {
		return err
	}
	if err := s.open(b, b.Type); err != nil {
		return err
	}
	if len(input) > 0 {
		if err := s.delta(map[string]any{"type": "input_json_delta", "partial_json": string(input)}); err != nil {
			return err
		}
	}
	return s.close()
}

func (s *antStream) open(block any, kind string) error {
	s.index++
	s.kind = kind
	return s.live.sse("content_block_start", map[string]any{"type": "content_block_start", "index": s.index, "content_block": block})
}

func (s *antStream) close() error {
	if s.kind == "" {
		return nil
	}
	if s.kind == "tool_use" {
		s.done[s.tool] = true
	}
	s.kind = ""
	return s.live.sse("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.index})
}

func (s *antStream) delta(delta map[string]any) error {
	return s.live.sse("content_block_delta", map[string]any{"type": "content_block_delta", "index": s.index, "delta": delta})
}

// Closes the open block, then writes text held back behind a tool block as a block of its own
func (s *antStream) settle() error {
	if err := s.close(); err != nil {
		return err
	}
	if s.held.Len() == 0 {
		return nil
	}
	text := s.held.String()
	s.held.Reset()
	if err := s.open(antText{Type: "text"}, "text"); err != nil {
		return err
	}
	if err := s.delta(map[string]any{"type": "text_delta", "text": text}); err != nil {
		return err
	}
	return s.close()
}

func (s *antStream) Write(ev Event) error {
	if err := oneChoice(ev, "anthropic"); err != nil {
		return err
	}
	switch ev.Kind {
	case Said:
		if s.kind == "tool_use" {
			s.held.WriteString(ev.Text)
			return nil
		}
		if s.kind != "text" {
			if err := s.settle(); err != nil {
				return err
			}
			if err := s.open(antText{Type: "text"}, "text"); err != nil {
				return err
			}
		}
		return s.delta(map[string]any{"type": "text_delta", "text": ev.Text})
	case Thought:
		if s.kind != "thinking" {
			if err := s.settle(); err != nil {
				return err
			}
			if err := s.open(antThinking{Type: "thinking"}, "thinking"); err != nil {
				return err
			}
		}
		return s.delta(map[string]any{"type": "thinking_delta", "thinking": ev.Text})
	case Called:
		s.tools.add(ev)
		if s.kind != "tool_use" || s.tool != ev.Index {
			if s.done[ev.Index] {
				return fmt.Errorf("upstream sent more of tool call %d after moving on from it", ev.Index)
			}
			call := s.tools.calls[ev.Index]
			if call.Name == "" {
				return fmt.Errorf("upstream started tool call %d without naming the tool", ev.Index)
			}
			call.ID = antToolID(call.ID)
			if err := s.settle(); err != nil {
				return err
			}
			if err := s.open(antBlock{Type: "tool_use", ID: call.ID, Name: call.Name, Input: json.RawMessage("{}")}, "tool_use"); err != nil {
				return err
			}
			s.tool = ev.Index
		}
		if ev.Tool.Args == "" {
			return nil
		}
		return s.delta(map[string]any{"type": "input_json_delta", "partial_json": ev.Tool.Args})
	case Ran:
		return antRan(s, ev.Run)
	case Stopped:
		if err := s.settle(); err != nil {
			return err
		}
		for _, call := range s.tools.list() {
			if _, ok := jsonArgs(call.Args); !ok {
				return &ToolArgsError{Name: call.Name}
			}
		}
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		reason := antReason(StopOf(res.Stop, len(s.tools.order)))
		// The delta carries the closing fields and every other key the runtime added
		delta := map[string]any{"stop_reason": reason, "stop_sequence": nil}
		for k, v := range res.Extra {
			if _, taken := delta[k]; !taken {
				delta[k] = v
			}
		}
		if err := s.live.sse("message_delta", map[string]any{"type": "message_delta", "delta": delta, "usage": antUsageOf(res)}); err != nil {
			return err
		}
		return s.live.sse("message_stop", map[string]any{"type": "message_stop"})
	case Failed:
		return s.live.sse("error", antStreamFailure(*ev.Failure))
	}
	return nil
}

// The error event a Messages stream ends with
func antStreamFailure(f Failure) map[string]any {
	code := f.Code
	if code == "" {
		code = CodeAPI
	}
	return map[string]any{"type": "error", "error": map[string]any{"type": code, "message": f.Message}}
}

// Ends a Messages stream with its error event
func (anthropic) StreamFailure(_ StreamAt, f Failure) ([]byte, error) {
	return sseJSON("error", antStreamFailure(f))
}

func (s *antStream) Close() error {
	s.live.close()
	return nil
}

// The count endpoint sizes image blocks itself, and the API refuses fields it does not know
func (anthropic) Traits() Traits {
	return Traits{CountsImages: true, Strict: true, TokenizePath: AnthropicCount}
}

func (a anthropic) Native(req *Request) (string, []byte, error) { return native(a, req) }

func (anthropic) ErrorMessage(body []byte) string { return errorField(body) }

func (anthropic) Error(w http.ResponseWriter, f Failure) {
	retryHeader(w, f)
	WriteJSON(w, f.Status, map[string]any{"type": "error", "error": map[string]any{"type": f.Code, "message": f.Message}})
}
