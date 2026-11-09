package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
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

// Rounds of tool calls one request may make before the model has to answer
const maxToolRounds = 32

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

// Whether the gateway runs any tool for the request
func (p *toolPlan) active() bool { return p != nil && len(p.order) > 0 }

func (p *toolPlan) close() {
	if p == nil {
		return
	}
	for _, c := range p.closers {
		c.Close()
	}
}

// Takes provided tools and sources out of the request and offers what the gateway runs, bare requests skipping route servers
func (g *Gateway) plan(ctx context.Context, req *chat.Request, route *v1.Route, h http.Header, bare bool) (*toolPlan, error) {
	p := &toolPlan{g: g, tools: map[string]*planned{}}
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
		server, err := connectSource(ctx, s, g.log)
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
	var refs []serverRef
	for _, ref := range splitList(h.Get(ToolsHeader)) {
		refs = append(refs, serverRef{name: ref, mutating: mutating})
	}
	profile := route.GetSpec().GetProfile()
	if !bare {
		for _, ref := range profile.GetToolServers() {
			refs = append(refs, serverRef{name: ref, mutating: profile.GetMutatingTools(), route: true})
		}
	}
	servers, failed, err := g.resolveServers(ctx, refs)
	if err != nil {
		p.close()
		if failed.route {
			return nil, &chat.Failure{Status: http.StatusServiceUnavailable, Code: chat.CodeModelUnavailable, Message: fmt.Sprintf("tool server %s the route %s attaches is unavailable: %v", failed.name, route.GetName(), err), RetryAfter: g.cfg.Retry.Unavailable}
		}
		return nil, bad("%v", err)
	}
	for i, server := range servers {
		if err := p.attach(server, refs[i].mutating); err != nil {
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

// One tool server a request attaches, by header or by the route's profile
type serverRef struct {
	name     string
	mutating bool
	route    bool
}

// Resolves every tool server at once, local and on members alike, answering them in order or the first failure by order
func (g *Gateway) resolveServers(ctx context.Context, refs []serverRef) ([]tools.Server, serverRef, error) {
	servers := make([]tools.Server, len(refs))
	errs := make([]error, len(refs))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Go(func() {
			servers[i], errs[i] = g.tools.Server(ctx, ref.name)
			if errs[i] != nil {
				cancel()
			}
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return nil, refs[i], err
		}
	}
	return servers, serverRef{}, nil
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
			if t.Provided.Kind == v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH {
				return nil, bad("model %s cannot search the web until a web search tool server is added in Settings on the node serving it", req.Model)
			}
			return nil, bad("model %s cannot fetch pages until a web fetch tool server is added in Settings on the node serving it", req.Model)
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
	if p == nil {
		return
	}
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

// Splits a comma separated header value
func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

const (
	// Answers whose tool rounds are kept, the oldest leaving past either bound
	roundsMax    = 4096
	roundsBudget = 128 << 20
)

// Tool rounds of recent answers, read without a lock and kept so an echoed answer gets them back
type roundStore struct {
	entries sync.Map
	// Serializes writers keeping the eviction order and the bytes held
	mu    sync.Mutex
	order []string
	held  int
}

// An answer's earlier rounds, its final turn with ran calls before held ones, and that round's results
type roundEntry struct {
	before   []chat.Message
	final    chat.Message
	after    []chat.Message
	heldFrom int
	size     int
}

func newRoundStore() *roundStore { return &roundStore{} }

// The key an echoed answer makes from the route, the turn before it, its text, and its held calls
func roundKey(route string, prev *chat.Message, text string, calls []chat.ToolCall) string {
	h := sha256.New()
	h.Write([]byte(route))
	h.Write([]byte{0})
	if prev != nil {
		h.Write([]byte(prev.Role))
		h.Write([]byte{0})
		h.Write([]byte(strings.TrimSpace(chat.TextOf(prev.Parts))))
	}
	h.Write([]byte{0})
	h.Write([]byte(strings.TrimSpace(text)))
	for _, c := range calls {
		h.Write([]byte{0})
		h.Write([]byte(c.Name))
		h.Write([]byte{0})
		h.Write([]byte(canonicalArgs(c.Args)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Arguments with their keys sorted and spacing dropped, so every protocol's echo of a call reads the same
func canonicalArgs(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		if out, err := json.Marshal(v); err == nil {
			return string(out)
		}
	}
	return strings.TrimSpace(s)
}

// Bytes a turn holds in text, thinking, and arguments
func turnSize(m chat.Message) int {
	n := 0
	for _, p := range m.Parts {
		n += len(p.Text) + len(p.Data)
	}
	for _, c := range m.ToolCalls {
		n += len(c.Name) + len(c.Args)
	}
	return n
}

// Keeps an answer's turns, dropping the oldest answers past the count and byte bounds
func (s *roundStore) put(key string, before []chat.Message, final chat.Message, after []chat.Message, heldFrom int) {
	e := &roundEntry{before: before, final: final, after: after, heldFrom: heldFrom, size: turnSize(final)}
	for _, m := range before {
		e.size += turnSize(m)
	}
	for _, m := range after {
		e.size += turnSize(m)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.entries.Swap(key, e); ok {
		s.held -= old.(*roundEntry).size
	} else {
		s.order = append(s.order, key)
	}
	s.held += e.size
	for len(s.order) > 1 && (len(s.order) > roundsMax || s.held > roundsBudget) {
		oldest := s.order[0]
		s.order = s.order[1:]
		if gone, ok := s.entries.LoadAndDelete(oldest); ok {
			s.held -= gone.(*roundEntry).size
		}
	}
}

func (s *roundStore) get(key string) (*roundEntry, bool) {
	e, ok := s.entries.Load(key)
	if !ok {
		return nil, false
	}
	return e.(*roundEntry), true
}

// Puts the tool rounds back behind every assistant turn that echoes an answer they led to, answering whether any did
func (s *roundStore) splice(route string, msgs []chat.Message) ([]chat.Message, bool) {
	var out []chat.Message
	changed := false
	for i, m := range msgs {
		if m.Role != "assistant" {
			out = append(out, m)
			continue
		}
		var prev *chat.Message
		if i > 0 {
			prev = &msgs[i-1]
		}
		e, ok := s.get(roundKey(route, prev, chat.TextOf(m.Parts), m.ToolCalls))
		if !ok {
			out = append(out, m)
			continue
		}
		changed = true
		out = append(out, e.before...)
		final := e.final
		final.ToolCalls = append(append([]chat.ToolCall(nil), final.ToolCalls[:e.heldFrom]...), m.ToolCalls...)
		final.Extra = m.Extra
		out = append(out, final)
		out = append(out, e.after...)
	}
	return out, changed
}
