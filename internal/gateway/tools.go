package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/tools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The headers a request attaches tool servers and asks for approval with
const (
	// Names the tool servers a request attaches, comma separated, node qualified for a member's
	ToolsHeader = "X-Nebu-Tools"
	// Whether the servers the header attaches offer their mutating tools too
	MutatingHeader = "X-Nebu-Tools-Mutating"
	// Which gateway tools the client runs itself after approval, mutating or all
	ApproveHeader = "X-Nebu-Approve-Tools"
	// Rounds of tool calls one request may make before the model has to answer
	maxToolRounds = 32
	// Searches or fetches one request may run when the client sets no max_uses
	serverToolUses = 10
)

// The Anthropic server tools the gateway stands in for, by base type and the tool each stands for
var serverToolKinds = map[string]v1.ToolServerKind{tools.SearchTool: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, tools.FetchTool: v1.ToolServerKind_TOOL_SERVER_KIND_FETCH}

// Installs the tool servers, so routes, personas, and requests can attach them
func (g *Gateway) SetTools(r *tools.Registry) { g.tools = r }

// Limits a client declared for an Anthropic server tool
type serverTool struct {
	maxUses          int
	allowed, blocked []string
	country          string
	maxChars         int
	uses             int
}

// One tool the gateway offers the model and runs itself
type planned struct {
	server tools.Server
	tool   tools.Tool
	// Whether the server is one the request named rather than one Settings hold
	adhoc bool
	// Limits from the request's server tool, nil for other attachments
	limits *serverTool
}

// The tools the gateway runs itself for one request, by the name the model calls them
type toolPlan struct {
	g       *Gateway
	tools   map[string]*planned
	order   []string
	approve string
	closers []interface{ Close() error }
}

// Whether the gateway runs any tool for the request
func (p *toolPlan) active() bool { return len(p.order) > 0 }

func (p *toolPlan) close() {
	for _, c := range p.closers {
		c.Close()
	}
}

// Takes server tools and attachments out of the chat and offers the model what the gateway runs
func (g *Gateway) plan(ctx context.Context, chat *Chat, route *v1.Route, h http.Header) (*toolPlan, error) {
	p := &toolPlan{g: g, tools: map[string]*planned{}}
	switch h.Get(ApproveHeader) {
	case "", "mutating", "all":
		p.approve = h.Get(ApproveHeader)
	default:
		return nil, bad("%s is mutating, all, or unset", ApproveHeader)
	}
	kept, err := p.takeServerTools(chat)
	if err != nil {
		return nil, err
	}
	for _, s := range chat.MCPServers {
		server, err := tools.Connect(ctx, s.Name, s.URL, bearer(s.Token), s.Allowed, g.log)
		if err != nil {
			p.close()
			return nil, bad("mcp server %s could not be reached: %v", s.Name, err)
		}
		p.closers = append(p.closers, server)
		for _, t := range server.Tools() {
			if err := p.offer(server, t, true, nil); err != nil {
				p.close()
				return nil, err
			}
		}
	}
	chat.MCPServers = nil
	mutating := strings.EqualFold(h.Get(MutatingHeader), "true") || h.Get(MutatingHeader) == "1"
	for _, ref := range splitList(h.Get(ToolsHeader)) {
		server, err := g.tools.Server(ctx, ref)
		if err != nil {
			p.close()
			return nil, bad("%v", err)
		}
		if err := p.attach(server, mutating); err != nil {
			p.close()
			return nil, err
		}
	}
	for _, ref := range route.GetProfile().GetToolServers() {
		server, err := g.tools.Server(ctx, ref)
		if err != nil {
			g.log.Warn("tool server attached to a route is not offered", "model", route.GetName(), "server", ref, "err", err)
			continue
		}
		if err := p.attach(server, route.GetProfile().GetMutatingTools()); err != nil {
			p.close()
			return nil, err
		}
	}
	for _, t := range kept {
		if have, taken := p.tools[t.Name]; taken {
			p.close()
			return nil, bad("the name %s belongs to tool server %s", t.Name, have.server.Name())
		}
	}
	for _, name := range p.order {
		pl := p.tools[name]
		kept = append(kept, Tool{Name: name, Description: pl.tool.Description, Schema: pl.tool.Schema})
	}
	chat.Tools = kept
	return p, nil
}

// Offers a server's tools under an attachment
func (p *toolPlan) attach(server tools.Server, mutating bool) error {
	for _, t := range tools.Offered(server, mutating) {
		if err := p.offer(server, t, false, nil); err != nil {
			return err
		}
	}
	return nil
}

// Offers one tool, the same server's twice being once
func (p *toolPlan) offer(server tools.Server, t tools.Tool, adhoc bool, limits *serverTool) error {
	if have, taken := p.tools[t.Name]; taken {
		if have.server.Name() == server.Name() && have.server.Kind() == server.Kind() {
			return nil
		}
		return bad("tool servers %s and %s both offer %s", have.server.Name(), server.Name(), t.Name)
	}
	p.tools[t.Name] = &planned{server: server, tool: t, adhoc: adhoc, limits: limits}
	p.order = append(p.order, t.Name)
	return nil
}

// Whether a tool is the Anthropic server tool of a base type, in any dated version the API has named
func isServerTool(t Tool, base string) bool {
	return t.Type == base || strings.HasPrefix(t.Type, base+"_")
}

// Takes the Anthropic server tools out of the chat and offers their servers, answering the tools that stay
func (p *toolPlan) takeServerTools(chat *Chat) ([]Tool, error) {
	if chat.Format != v1.ApiFlavor_API_FLAVOR_ANTHROPIC {
		return chat.Tools, nil
	}
	kept := make([]Tool, 0, len(chat.Tools))
	found := map[string]bool{}
	for _, t := range chat.Tools {
		var base string
		for name := range serverToolKinds {
			if isServerTool(t, name) {
				base = name
			}
		}
		if base == "" {
			for name := range serverToolKinds {
				if t.Name == name {
					return nil, bad("the name %s belongs to the %s tool", name, strings.ReplaceAll(name, "_", " "))
				}
			}
			kept = append(kept, t)
			continue
		}
		if found[base] {
			return nil, bad("a request takes one %s tool", strings.ReplaceAll(base, "_", " "))
		}
		found[base] = true
		if t.Name != base {
			return nil, bad("the %s tool is named %s", t.Type, base)
		}
		limits, err := serverToolLimits(t)
		if err != nil {
			return nil, err
		}
		server, ok := p.g.tools.Find(serverToolKinds[base])
		if !ok {
			switch base {
			case tools.SearchTool:
				return nil, bad("model %s cannot search the web until a web search tool server is added in Settings on the node serving it", chat.Model)
			default:
				return nil, bad("model %s cannot fetch pages until a web fetch tool server is added in Settings on the node serving it", chat.Model)
			}
		}
		for _, tool := range server.Tools() {
			if err := p.offer(server, tool, false, limits); err != nil {
				return nil, err
			}
		}
	}
	return kept, nil
}

// Reads the limits a server tool definition carries
func serverToolLimits(t Tool) (*serverTool, error) {
	var opts struct {
		MaxUses  *int     `json:"max_uses"`
		Allowed  []string `json:"allowed_domains"`
		Blocked  []string `json:"blocked_domains"`
		Location *struct {
			Country string `json:"country"`
		} `json:"user_location"`
		MaxTokens *int `json:"max_content_tokens"`
	}
	raw, _ := json.Marshal(t.Extra)
	if err := json.Unmarshal(raw, &opts); err != nil {
		return nil, bad("%s: %v", t.Name, err)
	}
	limits := &serverTool{maxUses: serverToolUses, allowed: opts.Allowed, blocked: opts.Blocked}
	if opts.MaxUses != nil {
		if *opts.MaxUses < 1 {
			return nil, bad("%s max_uses must be at least 1", t.Name)
		}
		limits.maxUses = *opts.MaxUses
	}
	if len(opts.Allowed) > 0 && len(opts.Blocked) > 0 {
		return nil, bad("%s takes allowed_domains or blocked_domains, not both", t.Name)
	}
	if opts.Location != nil {
		limits.country = opts.Location.Country
	}
	if opts.MaxTokens != nil {
		if *opts.MaxTokens < 1 {
			return nil, bad("%s max_content_tokens must be at least 1", t.Name)
		}
		limits.maxChars = *opts.MaxTokens * 4
	}
	return limits, nil
}

// Whether the client runs a tool itself after approval instead of the gateway
func (p *toolPlan) handsBack(pl *planned) bool {
	return p.approve == "all" || p.approve == "mutating" && !pl.tool.ReadOnly
}

// Runs one of the model's calls, answering what the tool said or why it was not asked
func (p *toolPlan) run(ctx context.Context, c ToolCall, capped bool) *ToolRun {
	pl := p.tools[c.Name]
	run := &ToolRun{Call: c, Server: pl.server.Name(), Tool: pl.tool.Inner, Kind: pl.server.Kind(), AdHoc: pl.adhoc}
	start := time.Now()
	defer func() { run.Took = time.Since(start) }()
	if capped {
		run.Result = &tools.Result{Parts: []tools.Part{{Type: "text", Text: fmt.Sprintf("The gateway's limit of %d rounds of tool calls for one request was reached. Answer with what you have.", maxToolRounds)}}, Failed: true, Code: "max_uses_exceeded"}
		return run
	}
	call := tools.Call{Name: c.Name, Args: json.RawMessage(c.Args)}
	if l := pl.limits; l != nil {
		call.Country, call.Allowed, call.Blocked, call.MaxChars, call.Exhausted = l.country, l.allowed, l.blocked, l.maxChars, l.uses >= l.maxUses
	}
	res, err := pl.server.Call(ctx, call)
	if err != nil {
		p.g.log.Warn("gateway tool", "server", run.Server, "tool", run.Tool, "err", err)
	}
	if res == nil {
		res = &tools.Result{Parts: []tools.Part{{Type: "text", Text: "The tool could not be called."}}, Failed: true, Code: "unavailable"}
	}
	if pl.limits != nil && res.Asked {
		pl.limits.uses++
	}
	run.Result = res
	return run
}

// Takes the gateway's tools away from the model, so it answers with what it has
func (p *toolPlan) withdraw(chat *Chat) {
	kept := chat.Tools[:0]
	for _, t := range chat.Tools {
		if _, mine := p.tools[t.Name]; !mine {
			kept = append(kept, t)
		}
	}
	chat.Tools = kept
	if len(kept) == 0 {
		chat.ToolChoice = ""
	}
}

// The tool turns a round's runs make, text and images as the tools answered them
func toolTurns(runs []*ToolRun) []Message {
	out := make([]Message, 0, len(runs))
	for _, run := range runs {
		var parts []Part
		for _, p := range run.Result.Parts {
			parts = append(parts, Part{Type: p.Type, Text: p.Text, MediaType: p.MediaType, Data: p.Data})
		}
		if run.Result.Failed && run.Kind != v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH && run.Kind != v1.ToolServerKind_TOOL_SERVER_KIND_FETCH {
			parts = append([]Part{{Type: "text", Text: "The tool failed: "}}, parts...)
		}
		out = append(out, Message{Role: "tool", ToolID: run.Call.ID, Parts: parts})
	}
	return out
}

// The tool as the API records it in a trace
func traceRun(run *ToolRun) *v1.TraceToolCall {
	return &v1.TraceToolCall{Id: run.Call.ID, Name: run.Call.Name, Arguments: run.Call.Args, Server: run.Server, Result: run.Result.Text(), Failed: run.Result.Failed, TookMs: uint32(run.Took.Milliseconds())}
}

// Counts the runs of a kind that reached their provider, for usage
func runsOf(runs []*ToolRun, kind v1.ToolServerKind) int {
	n := 0
	for _, r := range runs {
		if r.Kind == kind && !r.AdHoc && r.Result.Asked {
			n++
		}
	}
	return n
}

// The headers an MCP endpoint named with a token gets
func bearer(token string) map[string]string {
	if token == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + token}
}

// Splits a comma separated header value
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
