package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
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
)

// One tool the gateway offers the model and runs itself
type planned struct {
	server tools.Server
	tool   tools.Tool
	// Whether the server came from the request rather than Settings
	adhoc bool
	// The limits the client set on a provided tool, nil for other attachments
	limits *chat.Provided
	uses   int
}

// The tools the gateway runs itself for one request, by the name the model calls them
type toolPlan struct {
	g       *Gateway
	tools   map[string]*planned
	order   []string
	approve string
	closers []interface{ Close() error }
}

func emptyPlan(g *Gateway) *toolPlan {
	return &toolPlan{g: g, tools: map[string]*planned{}}
}

// Whether the gateway runs any tool for the request
func (p *toolPlan) active() bool { return len(p.order) > 0 }

func (p *toolPlan) close() {
	for _, c := range p.closers {
		c.Close()
	}
}

// Takes provided tools and sources out of the request and offers what the gateway runs, bare requests skipping route servers
func (g *Gateway) plan(ctx context.Context, req *chat.Request, route *v1.Route, h http.Header, bare bool) (*toolPlan, error) {
	p := emptyPlan(g)
	switch h.Get(ApproveHeader) {
	case "", "mutating", "all":
		p.approve = h.Get(ApproveHeader)
	default:
		return nil, bad("%s is mutating, all, or unset", ApproveHeader)
	}
	kept, err := p.takeProvided(req)
	if err != nil {
		return nil, err
	}
	for _, s := range req.Sources {
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
	req.Sources = nil
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
	routed := route.GetProfile().GetToolServers()
	if bare {
		routed = nil
	}
	for _, ref := range routed {
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
		kept = append(kept, chat.Tool{Name: name, Description: pl.tool.Description, Schema: pl.tool.Schema})
	}
	req.Tools = kept
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
func (p *toolPlan) offer(server tools.Server, t tools.Tool, adhoc bool, limits *chat.Provided) error {
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

// Takes the provided tools out of the request and offers their servers, answering the tools that stay
func (p *toolPlan) takeProvided(req *chat.Request) ([]chat.Tool, error) {
	kept := make([]chat.Tool, 0, len(req.Tools))
	for _, t := range req.Tools {
		if t.Provided == nil {
			kept = append(kept, t)
			continue
		}
		server, ok := p.g.tools.Find(t.Provided.Kind)
		if !ok {
			switch t.Provided.Kind {
			case v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH:
				return nil, bad("model %s cannot search the web until a web search tool server is added in Settings on the node serving it", req.Model)
			default:
				return nil, bad("model %s cannot fetch pages until a web fetch tool server is added in Settings on the node serving it", req.Model)
			}
		}
		for _, tool := range server.Tools() {
			if err := p.offer(server, tool, false, t.Provided); err != nil {
				return nil, err
			}
		}
	}
	return kept, nil
}

// Whether the client runs a tool itself after approval instead of the gateway
func (p *toolPlan) handsBack(pl *planned) bool {
	return p.approve == "all" || p.approve == "mutating" && !pl.tool.ReadOnly
}

// Runs one of the model's calls, answering what the tool said or why it was not asked
func (p *toolPlan) run(ctx context.Context, c chat.ToolCall, capped bool) *chat.Run {
	pl := p.tools[c.Name]
	run := &chat.Run{Call: c, Server: pl.server.Name(), Tool: pl.tool.Inner, Kind: pl.server.Kind(), AdHoc: pl.adhoc}
	start := time.Now()
	defer func() { run.Took = time.Since(start) }()
	if capped {
		run.Result = &tools.Result{Parts: []tools.Part{{Type: "text", Text: fmt.Sprintf("The gateway's limit of %d rounds of tool calls for one request was reached. Answer with what you have.", maxToolRounds)}}, Failed: true, Code: "max_uses_exceeded"}
		return run
	}
	call := tools.Call{Name: c.Name, Args: json.RawMessage(c.Args)}
	if l := pl.limits; l != nil {
		call.Country, call.Allowed, call.Blocked, call.MaxChars, call.Exhausted = l.Country, l.Allowed, l.Blocked, l.MaxChars, pl.uses >= l.MaxUses
	}
	res, err := pl.server.Call(ctx, call)
	if err != nil {
		p.g.log.Warn("gateway tool", "server", run.Server, "tool", run.Tool, "err", err)
	}
	if res == nil {
		res = &tools.Result{Parts: []tools.Part{{Type: "text", Text: "The tool could not be called."}}, Failed: true, Code: "unavailable"}
	}
	if pl.limits != nil && res.Asked {
		pl.uses++
	}
	run.Result = res
	return run
}

// Takes the gateway's tools away from the model, so it answers with what it has
func (p *toolPlan) withdraw(req *chat.Request) {
	kept := req.Tools[:0]
	for _, t := range req.Tools {
		if _, mine := p.tools[t.Name]; !mine {
			kept = append(kept, t)
		}
	}
	req.Tools = kept
	if len(kept) == 0 {
		req.ToolChoice = ""
	}
}

// The tool turns a round's runs make, text and images as the tools answered them
func toolTurns(runs []*chat.Run) []chat.Message {
	out := make([]chat.Message, 0, len(runs))
	for _, run := range runs {
		parts := chat.ResultParts(run.Result)
		if run.Result.Failed && run.Kind != v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH && run.Kind != v1.ToolServerKind_TOOL_SERVER_KIND_FETCH {
			parts = append([]chat.Part{{Type: "text", Text: "The tool failed: "}}, parts...)
		}
		out = append(out, chat.Message{Role: "tool", ToolID: run.Call.ID, Parts: parts})
	}
	return out
}

// A run as a trace records it
func traceRun(run *chat.Run) *v1.TraceToolCall {
	return &v1.TraceToolCall{Id: run.Call.ID, Name: run.Call.Name, Arguments: run.Call.Args, Server: run.Server, Result: run.Result.Text(), Failed: run.Result.Failed, TookMs: uint32(run.Took.Milliseconds())}
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
