package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/tools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Marks snippets the gateway sealed into encrypted_content
const sealTag = "nebu\x00"

// An MCP endpoint as an Anthropic request names it
type antMCPServer struct {
	Type          string `json:"type"`
	URL           string `json:"url"`
	Name          string `json:"name"`
	Authorization string `json:"authorization_token,omitempty"`
	Configuration *struct {
		Enabled      *bool    `json:"enabled"`
		AllowedTools []string `json:"allowed_tools"`
	} `json:"tool_configuration,omitempty"`
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

// Writes an Anthropic message block by block, streamed or whole at stop
type antWriter interface {
	Write(Event) error
	// Writes a complete block, streaming the input as a delta when one is given
	block(b antBlock, input json.RawMessage) error
	fail(status int, message, kind string)
	Close() error
}

// Gathers a message's blocks and writes the message whole at stop
type antWhole struct {
	w      http.ResponseWriter
	client Flavor
	model  string
	blocks []any
	text   strings.Builder
	tools  toolGather
}

func (a *antWhole) flush() {
	if a.text.Len() > 0 {
		a.blocks = append(a.blocks, antText{Type: "text", Text: a.text.String()})
		a.text.Reset()
	}
}

func (a *antWhole) Write(ev Event) error {
	switch ev.Kind {
	case "text":
		a.text.WriteString(ev.Text)
	case "tool":
		a.tools.add(ev)
	case "stop":
		a.flush()
		for _, c := range a.tools.list() {
			input, ok := jsonArgs(c.Args)
			if !ok {
				return &toolArgsError{name: c.Name}
			}
			a.blocks = append(a.blocks, antBlock{Type: "tool_use", ID: antToolID(c.ID), Name: c.Name, Input: input})
		}
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		if a.blocks == nil {
			a.blocks = []any{}
		}
		reply := antReply{ID: antID(), Type: "message", Role: "assistant", Model: a.model, Content: a.blocks, StopReason: ptr(antReason(stopOf(res.Stop, len(a.tools.order)))), Usage: antUsageOf(res), Extra: res.Extra}
		data, err := json.Marshal(reply)
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

func (a *antWhole) ran(run *ToolRun) error { return antRan(a, run) }

func (a *antWhole) fail(status int, message, kind string) { a.client.Error(a.w, status, message, kind) }

func (a *antWhole) Close() error { return nil }

func (s *antStream) ran(run *ToolRun) error { return antRan(s, run) }

// Writes a tool the gateway ran as server tool blocks for search and fetch and MCP blocks for the rest
func antRan(w antWriter, run *ToolRun) error {
	input, ok := jsonArgs(run.Call.Args)
	if !ok {
		input = json.RawMessage("{}")
	}
	switch {
	case run.Kind == v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH && !run.AdHoc:
		id := "srvtoolu_" + db.NewID()
		if err := w.block(antBlock{Type: "server_tool_use", ID: id, Name: tools.SearchTool, Input: json.RawMessage("{}")}, serverToolInput(input, "query")); err != nil {
			return err
		}
		return w.block(antBlock{Type: "web_search_tool_result", ToolUseID: id, Content: searchContent(run.Result)}, nil)
	case run.Kind == v1.ToolServerKind_TOOL_SERVER_KIND_FETCH && !run.AdHoc:
		id := "srvtoolu_" + db.NewID()
		if err := w.block(antBlock{Type: "server_tool_use", ID: id, Name: tools.FetchTool, Input: json.RawMessage("{}")}, serverToolInput(input, "url")); err != nil {
			return err
		}
		return w.block(antBlock{Type: "web_fetch_tool_result", ToolUseID: id, Content: fetchContent(run.Result)}, nil)
	}
	id := "mcptoolu_" + db.NewID()
	if err := w.block(antBlock{Type: "mcp_tool_use", ID: id, Name: run.Tool, ServerName: run.Server, Input: json.RawMessage("{}")}, input); err != nil {
		return err
	}
	content, _ := json.Marshal(antContent(resultParts(run.Result), false))
	return w.block(antBlock{Type: "mcp_tool_result", ToolUseID: id, IsError: ptr(run.Result.Failed), Content: content}, nil)
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
func resultParts(r *tools.Result) []Part {
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
		res := antSearchResult{Type: "web_search_result", Title: h.Title, URL: h.URL, EncryptedContent: sealSnippet(h.Snippet)}
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
func sealSnippet(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(sealTag + s))
}

// Opens a snippet this gateway sealed
func openSnippet(s string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || !strings.HasPrefix(string(raw), sealTag) || !utf8.Valid(raw) {
		return "", false
	}
	return strings.TrimPrefix(string(raw), sealTag), true
}

// The text a model reads for a web_search_tool_result block a client sent back
func searchResultText(content json.RawMessage) (string, error) {
	var failure antToolError
	if json.Unmarshal(content, &failure) == nil && failure.Type == "web_search_tool_result_error" {
		return tools.SearchText(nil, failure.ErrorCode), nil
	}
	var results []antSearchResult
	if err := json.Unmarshal(content, &results); err != nil {
		return "", bad("web_search_tool_result content must be a list of results or an error")
	}
	hits := make([]tools.Hit, 0, len(results))
	for _, r := range results {
		snippet, ok := openSnippet(r.EncryptedContent)
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
func fetchResultText(content json.RawMessage) (string, error) {
	var failure antToolError
	if json.Unmarshal(content, &failure) == nil && failure.Type == "web_fetch_tool_result_error" {
		return tools.FetchText(nil, failure.ErrorCode), nil
	}
	var res antFetchResult
	if err := json.Unmarshal(content, &res); err != nil || res.Type != "web_fetch_result" {
		return "", bad("web_fetch_tool_result content must be a web_fetch_result or an error")
	}
	page := &tools.Page{URL: res.URL, Title: res.Content.Title, Text: res.Content.Source.Data}
	if res.Content.Source.Type == "base64" {
		page.Text = "[a " + res.Content.Source.MediaType + " document the model cannot read]"
	}
	return tools.FetchText(page, ""), nil
}

// The parts a model reads for an mcp_tool_result block a client sent back
func mcpResultParts(b antBlock) ([]Part, error) {
	var text string
	if json.Unmarshal(b.Content, &text) == nil {
		return []Part{{Type: "text", Text: text}}, nil
	}
	inner, err := antBlocks(b.Content)
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

// The usage of a result, with the server tools it ran when it ran any
func antUsageOf(r *Result) antUsage {
	u := antUsage{InputTokens: r.In, OutputTokens: r.Out}
	searches, fetches := runsOf(r.Runs, v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH), runsOf(r.Runs, v1.ToolServerKind_TOOL_SERVER_KIND_FETCH)
	if searches > 0 || fetches > 0 {
		u.ServerToolUse = &antServerToolUse{WebSearchRequests: searches, WebFetchRequests: fetches}
	}
	return u
}
