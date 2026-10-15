// Package tools runs web search, web fetch, and MCP servers for models.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

const (
	// The name the model calls the search and fetch tools by
	SearchTool = "web_search"
	FetchTool  = "web_fetch"
	// Joins a server's name and a tool's name into the name the model calls
	Join = "__"
	// How long one tool call may run
	callTimeout = 2 * time.Minute
)

// Returned when a tool server reference names no server
var ErrUnknownServer = errors.New("unknown tool server")

// A tool as the model is offered it
type Tool struct {
	// The name the model calls the tool by
	Name        string
	Description string
	Schema      json.RawMessage
	// Whether the tool changes nothing it touches
	ReadOnly bool
	// The server's own name for the tool
	Inner string
}

// One part of what a tool answered, text or a base64 image
type Part struct {
	Type      string
	Text      string
	MediaType string
	Data      string
}

// One page a search turned up
type Hit struct {
	Title, URL, Snippet, Age string
}

// A page web_fetch read
type Page struct {
	URL, Title, Text, MediaType string
	RetrievedAt                 time.Time
}

// What a tool answered
type Result struct {
	Parts []Part
	// Whether the tool reported a failure
	Failed bool
	// Whether the provider was asked at all, which counts as a use
	Asked bool
	// Why the call did not happen, in the words the Anthropic API uses, empty when it did
	Code string
	// The search's hits, for clients that render them one by one
	Hits []Hit
	// The fetched page, for clients that render it as a document
	Page *Page
}

// Text of a result as a model reads it
func (r *Result) Text() string {
	var b strings.Builder
	for _, p := range r.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// A result of one text part
func textResult(s string, failed bool) *Result {
	return &Result{Parts: []Part{{Type: "text", Text: s}}, Failed: failed}
}

// One call of a tool with the constraints a client declared for it
type Call struct {
	// The name the model calls the tool by
	Name string
	Args json.RawMessage
	// The country results are localized to
	Country string
	// Domains results and pages are kept to, or kept from
	Allowed, Blocked []string
	// Characters a fetched page is cut to, zero for the default
	MaxChars int
	// Whether the request has spent the uses the client allowed, so a valid call is refused
	Exhausted bool
}

// A source of tools the gateway can run
type Server interface {
	Name() string
	Kind() v1.ToolServerKind
	// The tools the server offers right now
	Tools() []Tool
	Call(ctx context.Context, c Call) (*Result, error)
}

// A server the registry runs, with its connection state
type entry struct {
	spec   *v1.ToolServer
	state  v1.ToolServerState
	err    string
	server Server
	cancel context.CancelFunc
	// Settled when the first connection attempt ended either way
	ready chan struct{}
}

// Keeps the tool servers Settings names connected and hands them out by name
type Registry struct {
	log    *slog.Logger
	events *events.Bus
	peers  Peers
	mu     sync.Mutex
	byName map[string]*entry
	order  []string
}

func New(log *slog.Logger, bus *events.Bus) *Registry {
	if log == nil {
		log = slog.Default()
	}
	return &Registry{log: log, events: bus, byName: map[string]*entry{}}
}

// Installs the mesh, so servers on other members can be reached
func (r *Registry) SetPeers(p Peers) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.peers = p
}

// Starts added or changed servers and stops removed ones, so the registry matches the list
func (r *Registry) Apply(ctx context.Context, list []*v1.ToolServer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	keep := map[string]bool{}
	var order []string
	for _, spec := range list {
		keep[spec.GetName()] = true
		order = append(order, spec.GetName())
		have, ok := r.byName[spec.GetName()]
		if ok && proto.Equal(have.spec, spec) {
			continue
		}
		if ok {
			r.stopLocked(have)
		}
		r.startLocked(ctx, proto.Clone(spec).(*v1.ToolServer))
	}
	for name, e := range r.byName {
		if !keep[name] {
			r.stopLocked(e)
			delete(r.byName, name)
			r.publish(name, v1.EventAction_EVENT_ACTION_DELETED, r.statusLocked(e))
		}
	}
	r.order = order
}

// Stops every server
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, e := range r.byName {
		r.stopLocked(e)
		delete(r.byName, name)
	}
	r.order = nil
}

func (r *Registry) stopLocked(e *entry) {
	if e.cancel != nil {
		e.cancel()
	}
	if c, ok := e.server.(interface{ Close() error }); ok {
		c.Close()
	}
}

// Starts one server, built in ones at once and MCP ones in the background
func (r *Registry) startLocked(ctx context.Context, spec *v1.ToolServer) {
	e := &entry{spec: spec, ready: make(chan struct{})}
	r.byName[spec.GetName()] = e
	if !spec.GetEnabled() {
		e.state = v1.ToolServerState_TOOL_SERVER_STATE_DISABLED
		close(e.ready)
		r.publish(spec.GetName(), v1.EventAction_EVENT_ACTION_UPDATED, r.statusLocked(e))
		return
	}
	switch spec.GetKind() {
	case v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH:
		s, err := newSearch(spec)
		r.settleLocked(e, s, err)
		close(e.ready)
	case v1.ToolServerKind_TOOL_SERVER_KIND_FETCH:
		r.settleLocked(e, newFetch(spec), nil)
		close(e.ready)
	case v1.ToolServerKind_TOOL_SERVER_KIND_STDIO, v1.ToolServerKind_TOOL_SERVER_KIND_HTTP:
		e.state = v1.ToolServerState_TOOL_SERVER_STATE_CONNECTING
		run, cancel := context.WithCancel(ctx)
		e.cancel = cancel
		m := newMCP(spec, r.log)
		e.server = m
		go m.run(run, func(err error) { r.settle(e, m, err) })
	default:
		r.settleLocked(e, nil, fmt.Errorf("tool server %s has no kind", spec.GetName()))
		close(e.ready)
	}
	r.publish(spec.GetName(), v1.EventAction_EVENT_ACTION_UPDATED, r.statusLocked(e))
}

// Records a server's state after a connection attempt
func (r *Registry) settle(e *entry, s Server, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byName[e.spec.GetName()] != e {
		return
	}
	r.settleLocked(e, s, err)
	select {
	case <-e.ready:
	default:
		close(e.ready)
	}
	r.publish(e.spec.GetName(), v1.EventAction_EVENT_ACTION_UPDATED, r.statusLocked(e))
}

func (r *Registry) settleLocked(e *entry, s Server, err error) {
	if err != nil {
		e.state, e.err = v1.ToolServerState_TOOL_SERVER_STATE_FAILED, err.Error()
		if e.cancel != nil {
			e.state = v1.ToolServerState_TOOL_SERVER_STATE_CONNECTING
		}
		r.log.Warn("tool server", "server", e.spec.GetName(), "err", err)
		return
	}
	e.state, e.err, e.server = v1.ToolServerState_TOOL_SERVER_STATE_READY, "", s
}

func (r *Registry) publish(name string, action v1.EventAction, st *v1.ToolServerStatus) {
	if r.events != nil {
		r.events.Publish(v1.EventKind_EVENT_KIND_TOOL_SERVER, action, name, st)
	}
}

// Waits until every server's first connection attempt ended, or ctx ends
func (r *Registry) Wait(ctx context.Context) {
	r.mu.Lock()
	var waits []chan struct{}
	for _, e := range r.byName {
		waits = append(waits, e.ready)
	}
	r.mu.Unlock()
	for _, w := range waits {
		select {
		case <-w:
		case <-ctx.Done():
			return
		}
	}
}

// The servers in Settings order with their states and tools
func (r *Registry) Status() []*v1.ToolServerStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*v1.ToolServerStatus, 0, len(r.order))
	for _, name := range r.order {
		if e, ok := r.byName[name]; ok {
			out = append(out, r.statusLocked(e))
		}
	}
	return out
}

func (r *Registry) statusLocked(e *entry) *v1.ToolServerStatus {
	st := &v1.ToolServerStatus{Server: proto.Clone(e.spec).(*v1.ToolServer), State: e.state, Error: e.err, Ref: e.spec.GetName()}
	if e.server != nil && e.state == v1.ToolServerState_TOOL_SERVER_STATE_READY {
		st.Tools = Infos(e.server.Tools())
	}
	return st
}

// Tools as the API lists them
func Infos(tools []Tool) []*v1.ToolInfo {
	out := make([]*v1.ToolInfo, 0, len(tools))
	for _, t := range tools {
		out = append(out, &v1.ToolInfo{Name: t.Name, Description: t.Description, Schema: string(t.Schema), ReadOnly: t.ReadOnly})
	}
	return out
}

// The server a route, persona, or request attaches by name, node qualified for another member's
func (r *Registry) Server(ctx context.Context, ref string) (Server, error) {
	node, name, remote := strings.Cut(ref, "/")
	if remote {
		r.mu.Lock()
		peers := r.peers
		r.mu.Unlock()
		if peers == nil || node == peers.Self() {
			return r.local(name)
		}
		return r.remote(ctx, peers, node, name)
	}
	return r.local(ref)
}

func (r *Registry) local(name string) (Server, error) {
	r.mu.Lock()
	e, ok := r.byName[name]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownServer, name)
	}
	<-e.ready
	r.mu.Lock()
	defer r.mu.Unlock()
	switch e.state {
	case v1.ToolServerState_TOOL_SERVER_STATE_READY:
		return e.server, nil
	case v1.ToolServerState_TOOL_SERVER_STATE_DISABLED:
		return nil, fmt.Errorf("tool server %s is turned off in Settings", name)
	}
	return nil, fmt.Errorf("tool server %s is not ready: %s", name, e.err)
}

// The first ready server of a kind in Settings order
func (r *Registry) Find(kind v1.ToolServerKind) (Server, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range r.order {
		e := r.byName[name]
		if e != nil && e.spec.GetKind() == kind && e.state == v1.ToolServerState_TOOL_SERVER_STATE_READY {
			return e.server, true
		}
	}
	return nil, false
}

// Whether Settings name a server, whatever its state
func (r *Registry) Known(ref string) bool {
	_, name, _ := strings.Cut(ref, "/")
	if !strings.Contains(ref, "/") {
		name = ref
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.byName[name]
	return ok
}

// Runs one tool by the name the model calls it, on this node or a member's
func (r *Registry) Call(ctx context.Context, nodeID, name string, args json.RawMessage) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if nodeID != "" {
		r.mu.Lock()
		peers := r.peers
		r.mu.Unlock()
		if peers != nil && nodeID != peers.Self() {
			return callRemote(ctx, peers, nodeID, name, args)
		}
	}
	r.mu.Lock()
	var servers []Server
	for _, n := range r.order {
		if e := r.byName[n]; e != nil && e.state == v1.ToolServerState_TOOL_SERVER_STATE_READY {
			servers = append(servers, e.server)
		}
	}
	r.mu.Unlock()
	for _, s := range servers {
		for _, t := range s.Tools() {
			if t.Name == name {
				return s.Call(ctx, Call{Name: name, Args: args})
			}
		}
	}
	return nil, fmt.Errorf("no tool server offers %s", name)
}

// The tools a server offers an attachment, every one when mutating ones are wanted, else read only
func Offered(s Server, mutating bool) []Tool {
	var out []Tool
	for _, t := range s.Tools() {
		if mutating || t.ReadOnly {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
