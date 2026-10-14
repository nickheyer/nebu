package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	webSearchName = "web_search"
	// Searches one request may run when the client sets no max_uses
	webSearchUses = 10
	// Results handed to the model and the client per search
	webSearchHits = 10
	// Marks snippets the gateway sealed into encrypted_content
	sealTag = "nebu\x00"
)

// Whether a tool is the web search server tool, in any dated version the API has named
func isWebSearch(t Tool) bool {
	return t.Type == webSearchName || strings.HasPrefix(t.Type, webSearchName+"_")
}

// The web_search tool as the client defined it
type webSearch struct {
	maxUses int
	allowed []string
	blocked []string
	country string
}

// The function the model calls in the server tool's place
var webSearchTool = Tool{
	Name:        webSearchName,
	Description: "Searches the web and answers with numbered results carrying title, URL, snippet, and date. Search again with another query to find more",
	Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"What to search for"}},"required":["query"]}`),
}

// A result block written to the client, the snippet sealed into encrypted_content
type antSearchResult struct {
	Type             string  `json:"type"`
	Title            string  `json:"title"`
	URL              string  `json:"url"`
	EncryptedContent string  `json:"encrypted_content"`
	PageAge          *string `json:"page_age"`
}

// The block that stands in for results when a search did not happen
type antSearchError struct {
	Type      string `json:"type"`
	ErrorCode string `json:"error_code"`
}

// Takes the web_search server tool out of an Anthropic chat's tools and reads its settings.
// Reports whether there was one
func takeWebSearch(c *Chat) (*webSearch, bool, error) {
	if c.Format != v1.ApiFlavor_API_FLAVOR_ANTHROPIC {
		return nil, false, nil
	}
	var found *webSearch
	kept := make([]Tool, 0, len(c.Tools))
	for _, t := range c.Tools {
		if !isWebSearch(t) {
			if t.Name == webSearchName {
				return nil, false, bad("the name %s belongs to the web search tool", webSearchName)
			}
			kept = append(kept, t)
			continue
		}
		if found != nil {
			return nil, false, bad("a request takes one web search tool")
		}
		if t.Name != webSearchName {
			return nil, false, bad("the %s tool is named %s", t.Type, webSearchName)
		}
		var opts struct {
			MaxUses  *int     `json:"max_uses"`
			Allowed  []string `json:"allowed_domains"`
			Blocked  []string `json:"blocked_domains"`
			Location *struct {
				Country string `json:"country"`
			} `json:"user_location"`
		}
		raw, _ := json.Marshal(t.Extra)
		if err := json.Unmarshal(raw, &opts); err != nil {
			return nil, false, bad("%s: %v", webSearchName, err)
		}
		found = &webSearch{maxUses: webSearchUses, allowed: opts.Allowed, blocked: opts.Blocked}
		if opts.MaxUses != nil {
			if *opts.MaxUses < 1 {
				return nil, false, bad("%s max_uses must be at least 1", webSearchName)
			}
			found.maxUses = *opts.MaxUses
		}
		if len(opts.Allowed) > 0 && len(opts.Blocked) > 0 {
			return nil, false, bad("%s takes allowed_domains or blocked_domains, not both", webSearchName)
		}
		if opts.Location != nil {
			found.country = opts.Location.Country
		}
	}
	if found == nil {
		return nil, false, nil
	}
	c.Tools = append(kept, webSearchTool)
	return found, true, nil
}

// Keeps the results the domain lists allow, up to the per search cap
func (t *webSearch) filter(hits []searchHit) []searchHit {
	out := make([]searchHit, 0, len(hits))
	for _, h := range hits {
		if len(t.allowed) > 0 && !underDomains(h.URL, t.allowed) || len(t.blocked) > 0 && underDomains(h.URL, t.blocked) {
			continue
		}
		out = append(out, h)
		if len(out) == webSearchHits {
			break
		}
	}
	return out
}

// Writes an Anthropic message block by block, streamed or whole at stop
type antWriter interface {
	Write(Event) error
	// Writes a complete block, streaming the input as a delta when one is given
	block(b antBlock, input json.RawMessage) error
	// Ends the message with an error
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

func (a *antWhole) fail(status int, message, kind string) { a.client.Error(a.w, status, message, kind) }

func (a *antWhole) Close() error { return nil }

// The host's search provider, or why the model's searches have nowhere to go
func (g *Gateway) searcher(name string) (searcher, error) {
	var cfg *v1.WebSearch
	if g.webSearch != nil {
		cfg = g.webSearch()
	}
	if cfg.GetProvider() == "" {
		return nil, fmt.Errorf("model %s cannot search the web until a web search provider is chosen in Settings on the node serving it", name)
	}
	return searcherFor(cfg, &http.Client{Timeout: searchTimeout})
}

// Serves a chat whose client offered the web_search tool to a runtime without one
func (g *Gateway) search(w *traceWriter, r *http.Request, chat *Chat, tool *webSearch, name string, targets []target, hold *seatHold, policy *v1.Policy, client, upstream Flavor) int {
	t := w.t
	provider, err := g.searcher(name)
	if err != nil {
		g.refuse(w, client, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return -1
	}
	sink := &traceSink{t: t}
	defer sink.close()
	var out antWriter
	fail := func(status int, message, kind string) {
		t.Error = message
		if out == nil {
			client.Error(w, status, message, kind)
			return
		}
		out.fail(status, message, kind)
	}
	total := &Result{Model: name}
	var calls []ToolCall
	used, uses := -1, 0
	for {
		path, body, err := upstream.RenderRequest(chat)
		if err != nil {
			fail(http.StatusBadRequest, err.Error(), "invalid_request_error")
			break
		}
		t.UpstreamRequest = capped(body)
		resp, cancel, i, err := g.open(r.Context(), targets, hold, path, body, chat.Stream, policy, r.Header, name)
		if err != nil {
			g.log.Warn("gateway search", "model", name, "err", err)
			fail(upstreamFailure(name, err))
			break
		}
		used = i
		if out == nil && resp.StatusCode < http.StatusMultipleChoices {
			t.FirstByteAt = timestamppb.Now()
			relayHeaders(w.Header(), resp.Header)
			if chat.Stream {
				out = newAntStream(w, name)
			} else {
				out = &antWhole{w: w, client: client, model: name}
			}
		}
		var said strings.Builder
		res, err := g.round(chat, resp, upstream, func(text string) error {
			said.WriteString(text)
			sink.event(Event{Kind: "text", Text: text})
			return out.Write(Event{Kind: "text", Text: text})
		})
		cancel()
		if err != nil {
			switch {
			case r.Context().Err() != nil:
				t.Stop, t.Error = "cancelled", "client went away"
				g.log.Info("gateway search", "model", name, "err", t.Error)
			case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
				fail(http.StatusGatewayTimeout, fmt.Sprintf("model %s did not answer within its timeout", name), "timeout_error")
			default:
				g.log.Warn("gateway search", "model", name, "err", err)
				if refusal, ok := errors.AsType[*upstreamRefusal](err); ok {
					fail(refusal.status, refusal.message, refusal.kind)
					t.Error = "upstream: " + refusal.message
				} else {
					fail(http.StatusBadGateway, "upstream: "+err.Error(), "server_error")
				}
			}
			break
		}
		total.In, total.Out, total.Stop, total.Extra = total.In+res.In, total.Out+res.Out, res.Stop, mergeExtra(total.Extra, res.Extra)
		total.DraftOffered, total.DraftAccepted = total.DraftOffered+res.DraftOffered, total.DraftAccepted+res.DraftAccepted
		calls = append(calls, res.ToolCalls...)
		if len(res.ToolCalls) > 0 {
			sink.first()
		}
		var searches, others []ToolCall
		for _, c := range res.ToolCalls {
			if c.Name == webSearchName {
				searches = append(searches, c)
			} else {
				others = append(others, c)
			}
		}
		var answers []Message
		for _, c := range searches {
			hits, code, input := g.runSearch(r.Context(), provider, tool, c, &uses, name)
			id := "srvtoolu_" + db.NewID()
			if err := out.block(antBlock{Type: "server_tool_use", ID: id, Name: webSearchName, Input: json.RawMessage("{}")}, input); err != nil {
				return used
			}
			if err := out.block(antBlock{Type: "web_search_tool_result", ToolUseID: id, Content: searchContent(hits, code)}, nil); err != nil {
				return used
			}
			answers = append(answers, Message{Role: "tool", ToolID: c.ID, Parts: []Part{{Type: "text", Text: searchText(hits, code)}}})
		}
		if len(searches) == 0 || len(others) > 0 {
			for i, c := range others {
				if err := out.Write(Event{Kind: "tool", Index: i, Tool: &ToolCall{ID: c.ID, Name: c.Name, Args: c.Args}}); err != nil {
					return used
				}
			}
			total.Searches = uses
			if err := out.Write(Event{Kind: "stop", Res: total}); err != nil {
				if _, ok := errors.AsType[*toolArgsError](err); ok {
					fail(http.StatusBadGateway, "upstream: "+err.Error(), "upstream_error")
				}
			}
			break
		}
		var parts []Part
		if said.Len() > 0 {
			parts = []Part{{Type: "text", Text: said.String()}}
		}
		chat.Messages = append(append(chat.Messages, Message{Role: "assistant", Parts: parts, ToolCalls: res.ToolCalls}), answers...)
		// A forced first call has been made, the rest of the turn is the model's
		if chat.ToolChoice != "" && chat.ToolChoice != "auto" && chat.ToolChoice != "none" {
			chat.ToolChoice = "auto"
		}
	}
	total.ToolCalls = calls
	sink.result(total)
	t.WebSearches = uint32(uses)
	if out != nil {
		out.Close()
	}
	return used
}

// Runs one of the model's searches under the tool's limits. Answers the results, the error code
// that stood in for them, and the input the client sees
func (g *Gateway) runSearch(ctx context.Context, provider searcher, tool *webSearch, call ToolCall, uses *int, name string) ([]searchHit, string, json.RawMessage) {
	var in struct {
		Query string `json:"query"`
	}
	query := ""
	if json.Unmarshal([]byte(call.Args), &in) == nil {
		query = strings.TrimSpace(in.Query)
	}
	input, _ := json.Marshal(map[string]string{"query": query})
	switch {
	case query == "":
		return nil, "invalid_input", input
	case *uses >= tool.maxUses:
		return nil, "max_uses_exceeded", input
	case provider.queryMax() > 0 && utf8.RuneCountInString(query) > provider.queryMax():
		return nil, "query_too_long", input
	}
	*uses++
	hits, err := provider.search(ctx, searchAsk{Query: query, Country: tool.country})
	if err != nil {
		g.log.Warn("gateway search", "model", name, "query", query, "err", err)
		code := "unavailable"
		if failure, ok := errors.AsType[*searchError](err); ok {
			code = failure.code
		}
		return nil, code, input
	}
	return tool.filter(hits), "", input
}

// Reads one answer of the model, text going out as it arrives and tool calls and usage coming
// back whole. An error status comes back as an upstreamRefusal
func (g *Gateway) round(chat *Chat, resp *http.Response, upstream Flavor, text func(string) error) (*Result, error) {
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		message := upstream.ErrorMessage(raw)
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		return nil, &upstreamRefusal{status: resp.StatusCode, message: message, kind: errorKind(resp.StatusCode, raw)}
	}
	if chat.Stream {
		var final *Result
		err := upstream.ParseStream(resp.Body, func(ev Event) error {
			switch ev.Kind {
			case "text":
				return text(ev.Text)
			case "stop":
				final = ev.Res
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if final == nil {
			return nil, errors.New("the stream ended without a stop")
		}
		return final, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	res, err := upstream.ParseResult(chat, raw)
	if err != nil {
		if refusal, ok := errors.AsType[*upstreamRefusal](err); ok && refusal.kind == "" {
			refusal.kind = errorKind(refusal.status, raw)
		}
		return nil, err
	}
	if res.Text != "" {
		if err := text(res.Text); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// The content of a web_search_tool_result, the results or the error that stood in for them
func searchContent(hits []searchHit, code string) json.RawMessage {
	if code != "" {
		data, _ := json.Marshal(antSearchError{Type: "web_search_tool_result_error", ErrorCode: code})
		return data
	}
	out := make([]antSearchResult, 0, len(hits))
	for _, h := range hits {
		r := antSearchResult{Type: "web_search_result", Title: h.Title, URL: h.URL, EncryptedContent: sealSnippet(h.Snippet)}
		if h.Age != "" {
			age := h.Age
			r.PageAge = &age
		}
		out = append(out, r)
	}
	data, _ := json.Marshal(out)
	return data
}

// The results as the model reads them, or what kept the search from happening
func searchText(hits []searchHit, code string) string {
	switch code {
	case "":
	case "max_uses_exceeded":
		return "No more web searches are allowed in this request. Answer with what you have."
	case "invalid_input":
		return "A web_search call needs a query string."
	case "query_too_long":
		return "The query is too long for the search provider. Shorten it."
	case "too_many_requests":
		return "The search provider is rate limiting. Try again later."
	default:
		return "Web search is unavailable right now."
	}
	if len(hits) == 0 {
		return "No results."
	}
	var b strings.Builder
	b.WriteString("Search results:\n")
	for i, h := range hits {
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, h.Title, h.URL)
		if h.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", h.Snippet)
		}
		if h.Age != "" {
			fmt.Fprintf(&b, "   published %s\n", h.Age)
		}
	}
	return b.String()
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
	var failure antSearchError
	if json.Unmarshal(content, &failure) == nil && failure.Type == "web_search_tool_result_error" {
		return searchText(nil, failure.ErrorCode), nil
	}
	var results []antSearchResult
	if err := json.Unmarshal(content, &results); err != nil {
		return "", bad("web_search_tool_result content must be a list of results or an error")
	}
	hits := make([]searchHit, 0, len(results))
	for _, r := range results {
		snippet, ok := openSnippet(r.EncryptedContent)
		if !ok {
			return "", bad("web_search_tool_result carries content this gateway did not write")
		}
		hit := searchHit{Title: r.Title, URL: r.URL, Snippet: snippet}
		if r.PageAge != nil {
			hit.Age = *r.PageAge
		}
		hits = append(hits, hit)
	}
	return searchText(hits, ""), nil
}
