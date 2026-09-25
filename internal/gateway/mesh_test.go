package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Credentials that accept one gateway key and recognize member sessions by token
type fakeCreds struct {
	key   string
	peers map[string]string
}

func (c fakeCreds) Enabled() bool { return true }

func (c fakeCreds) Authenticated(h http.Header) bool {
	return c.key != "" && bearerOf(h) == c.key
}

func (c fakeCreds) PeerOf(h http.Header) (string, bool) {
	id, ok := c.peers[bearerOf(h)]
	return id, ok
}

func bearerOf(h http.Header) string {
	return strings.TrimSpace(strings.TrimPrefix(h.Get("Authorization"), "Bearer "))
}

// Adds a session token to every request, as the mesh client does
type sessionTransport struct {
	token string
}

func (s sessionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+s.token)
	return http.DefaultTransport.RoundTrip(req)
}

// Members as a test sees them: gateway bases, session tokens, link classes, and the slot moves
// and drops the relay asked for
type fakePeers struct {
	self   string
	bases  map[string]string
	tokens map[string]string
	links  map[string]int
	mu     sync.Mutex
	moved  []string
	drops  []string
	move   error
}

func (p *fakePeers) Self() string { return p.self }

func (p *fakePeers) Dial(_ context.Context, node string) (string, *http.Client, error) {
	base, ok := p.bases[node]
	if !ok {
		return "", nil, fmt.Errorf("unknown node %s", node)
	}
	return base, &http.Client{Transport: sessionTransport{token: p.tokens[node]}}, nil
}

func (p *fakePeers) LinkBetween(from, to string) int {
	if from == to {
		return 0
	}
	if rank, ok := p.links[from+">"+to]; ok {
		return rank
	}
	return 10
}

func (p *fakePeers) MoveSlotTo(_ context.Context, formation, to, from, file string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.moved = append(p.moved, strings.Join([]string{formation, to, from, file}, " "))
	return p.move
}

func (p *fakePeers) DropSlotOn(_ context.Context, node, formation, file string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.drops = append(p.drops, strings.Join([]string{node, formation, file}, " "))
	return nil
}

// A runtime seat as a test stands it up: it records every request and answers as told
type seat struct {
	*httptest.Server
	mu    sync.Mutex
	calls []seatCall
}

type seatCall struct {
	path   string
	query  string
	header http.Header
	body   map[string]any
}

func newSeat(t *testing.T, answer func(w http.ResponseWriter, r *http.Request, body map[string]any)) *seat {
	t.Helper()
	s := &seat{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.calls = append(s.calls, seatCall{path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone(), body: body})
		s.mu.Unlock()
		answer(w, r, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *seat) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *seat) call(i int) seatCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[i]
}

func (s *seat) host() string { return strings.TrimPrefix(s.URL, "http://") }

// A finished OpenAI chat answer
func chatAnswer(w http.ResponseWriter, text string) {
	writeJSON(w, http.StatusOK, map[string]any{"id": "c1", "object": "chat.completion", "model": "m", "choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 1}})
}

// A streamed OpenAI chat answer
func chatStream(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q}}]}\n\n", text)
	fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// Answers as a seat over its limits
func refuse(w http.ResponseWriter, status int) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": "seat refuses", "type": "server_error"}})
}

func newGateway(t *testing.T) (*Gateway, *Table) {
	t.Helper()
	table, err := OpenTable(context.Background(), nil, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return New(table, nil, nil, &v1.Policy{}, nil, slog.Default()), table
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

// Sends a request to a gateway and returns the answer with its trace
func ask(t *testing.T, g *Gateway, base, method, path string, body string, headers map[string]string) (reply, *v1.Trace) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := reply{status: resp.StatusCode, header: resp.Header, body: raw}
	var trace *v1.Trace
	if id := resp.Header.Get(traceHeader); id != "" && g != nil {
		trace = finished(t, g, id)
	}
	return out, trace
}

// The trace once the gateway closed it, which happens after the answer reached the client
func finished(t *testing.T, g *Gateway, id string) *v1.Trace {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if trace, ok := g.Traces().Get(id); ok && trace.GetFinishedAt() != nil {
			return trace
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("trace %s never finished", id)
	return nil
}

const chatBody = `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello there, how are you today"}]}`

func replicaRoute(table *Table, seats []*v1.RouteSeat, affinity *v1.AffinityPolicy, policy *v1.Policy) *v1.Route {
	f := &v1.Formation{Id: "f1", Name: "m", Shape: v1.Shape_SHAPE_REPLICAS, RuntimeId: "llamacpp", Repo: "r", Group: "g", Plan: &v1.FormationPlan{Affinity: affinity}}
	head := &v1.Instance{Id: seats[0].GetInstanceId(), Endpoint: seats[0].GetEndpoint(), Name: "m"}
	return table.ServeFormation(f, head, v1.ApiFlavor_API_FLAVOR_OPENAI, seats, policy, nil)
}

func readySeat(id, endpoint, node string, rank uint32) *v1.RouteSeat {
	return &v1.RouteSeat{InstanceId: id, Endpoint: endpoint, NodeId: node, Role: "replica", Rank: rank, State: v1.InstanceState_INSTANCE_STATE_READY}
}

// A request for a route another member conducts goes to that member's gateway under the
// member session with the entry node's id, streams back, and leaves a forwarded trace naming
// the conductor's trace, with auth on at both ends
func TestForwardedRequest(t *testing.T) {
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		chatStream(w, "Hello")
	})
	conductor, ctable := newGateway(t)
	conductor.SetCredentials(fakeCreds{key: "conductor-key", peers: map[string]string{"sess-n1": "n1"}})
	conductor.SetPeers(&fakePeers{self: "n2"})
	ctable.Set("m", "i1", "", model.URL, "r:g", "m", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)
	csrv := httptest.NewServer(conductor.Handler())
	defer csrv.Close()

	entry, etable := newGateway(t)
	entry.SetCredentials(fakeCreds{key: "entry-key"})
	entry.SetPeers(&fakePeers{self: "n1", bases: map[string]string{"n2": csrv.URL}, tokens: map[string]string{"n2": "sess-n1"}})
	if _, err := etable.Forward(&v1.Formation{Id: "f1", Name: "m", Conductor: "n2", Shape: v1.Shape_SHAPE_CHAIN, State: v1.FormationState_FORMATION_STATE_READY, Endpoint: csrv.URL, Repo: "r", Group: "g", RuntimeId: "llamacpp"}, v1.ApiFlavor_API_FLAVOR_OPENAI); err != nil {
		t.Fatal(err)
	}
	esrv := httptest.NewServer(entry.Handler())
	defer esrv.Close()

	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	if r, _ := ask(t, entry, esrv.URL, http.MethodPost, openaiChat, body, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("no key: %d %s", r.status, r.body)
	}
	r, trace := ask(t, entry, esrv.URL, http.MethodPost, openaiChat, body, map[string]string{"Authorization": "Bearer entry-key"})
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "Hello") || !strings.Contains(r.header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("forwarded stream: %d %s %s", r.status, r.header.Get("Content-Type"), r.body)
	}
	if trace == nil || !trace.GetForwarded() || trace.GetNodeId() != "n2" || trace.GetConductorTrace() == "" || trace.GetStatus() != http.StatusOK {
		t.Fatalf("entry trace %v", trace)
	}
	if got := trace.GetResponse(); got != "Hello" {
		t.Fatalf("entry trace parsed the conductor's answer: %q", got)
	}
	ct := finished(t, conductor, trace.GetConductorTrace())
	if ct.GetForwarded() || ct.GetRoute() != "m" || ct.GetInstanceId() != "i1" || ct.GetResponse() != "Hello" {
		t.Fatalf("conductor trace %v", ct)
	}
	call := model.call(0)
	if call.header.Get("Authorization") != "" {
		t.Fatal("the client's credential must not reach the model")
	}
	// The conductor saw the entry node and the member session, and nothing else opens the path.
	if r, _ := ask(t, conductor, csrv.URL, http.MethodPost, openaiChat, body, map[string]string{"Authorization": "Bearer sess-n1"}); r.status != http.StatusUnauthorized {
		t.Fatalf("a member session without the entry header must not pass: %d", r.status)
	}
	if r, _ := ask(t, conductor, csrv.URL, http.MethodPost, openaiChat, body, map[string]string{"Authorization": "Bearer sess-n1", entryHeader: "n1"}); r.status != http.StatusOK {
		t.Fatalf("a member session with the entry header passes: %d %s", r.status, r.body)
	}
	if r, _ := ask(t, conductor, csrv.URL, http.MethodPost, openaiChat, body, map[string]string{"Authorization": "Bearer stranger", entryHeader: "n1"}); r.status != http.StatusUnauthorized {
		t.Fatalf("a stranger with the entry header must not pass: %d", r.status)
	}
	// A route forwarded on both nodes is refused rather than bounced between them.
	if _, err := ctable.Forward(&v1.Formation{Id: "f2", Name: "loop", Conductor: "n1", Shape: v1.Shape_SHAPE_CHAIN, State: v1.FormationState_FORMATION_STATE_READY, Endpoint: esrv.URL, Repo: "r", Group: "g"}, v1.ApiFlavor_API_FLAVOR_OPENAI); err != nil {
		t.Fatal(err)
	}
	loop := `{"model":"loop","messages":[{"role":"user","content":"hi"}]}`
	if r, _ := ask(t, conductor, csrv.URL, http.MethodPost, openaiChat, loop, map[string]string{"Authorization": "Bearer sess-n1", entryHeader: "n1"}); r.status != http.StatusBadGateway {
		t.Fatalf("a forwarded route reached by a forwarded request: %d %s", r.status, r.body)
	}
	// Every member's gateway lists the route.
	r, _ = ask(t, nil, esrv.URL, http.MethodGet, modelsPath, "", map[string]string{"Authorization": "Bearer entry-key"})
	if !strings.Contains(string(r.body), `"id":"m"`) {
		t.Fatalf("models on the entry node: %s", r.body)
	}
}

// A video job forwarded to another member is polled, downloaded, and cancelled through the
// entry node, which remembers which member runs it
func TestForwardedVideoJob(t *testing.T) {
	done := make(chan struct{})
	model := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		switch {
		case r.URL.Path == sdcppVideoJob:
			writeJSON(w, http.StatusAccepted, map[string]any{"id": "job1", "status": "queued"})
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			close(done)
			writeJSON(w, http.StatusOK, map[string]any{"id": "job1", "status": "cancelled"})
		default:
			select {
			case <-done:
				writeJSON(w, http.StatusOK, map[string]any{"id": "job1", "status": "cancelled"})
			default:
				writeJSON(w, http.StatusOK, map[string]any{"id": "job1", "status": "processing"})
			}
		}
	})
	conductor, ctable := newGateway(t)
	conductor.SetPeers(&fakePeers{self: "n2"})
	ctable.SetModes("i1", []string{"vid_gen"})
	ctable.Set("v", "i1", "", model.URL, "r:g", "v", v1.ApiFlavor_API_FLAVOR_SDCPP, nil, nil)
	csrv := httptest.NewServer(conductor.Handler())
	defer csrv.Close()
	entry, etable := newGateway(t)
	entry.SetPeers(&fakePeers{self: "n1", bases: map[string]string{"n2": csrv.URL}})
	if _, err := etable.Forward(&v1.Formation{Id: "f1", Name: "v", Conductor: "n2", Shape: v1.Shape_SHAPE_STAGES, State: v1.FormationState_FORMATION_STATE_READY, Endpoint: csrv.URL, Repo: "r", Group: "g", RuntimeId: "sdcpp"}, v1.ApiFlavor_API_FLAVOR_SDCPP); err != nil {
		t.Fatal(err)
	}
	esrv := httptest.NewServer(entry.Handler())
	defer esrv.Close()
	r, trace := ask(t, entry, esrv.URL, http.MethodPost, videosPath, `{"model":"v","prompt":"a cat","seconds":1}`, nil)
	if r.status != http.StatusAccepted || trace == nil || !trace.GetForwarded() {
		t.Fatalf("forwarded video: %d %s", r.status, r.body)
	}
	var job struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(r.body, &job) != nil || job.ID == "" {
		t.Fatalf("job id: %s", r.body)
	}
	r, _ = ask(t, nil, esrv.URL, http.MethodGet, videosPath+"/"+job.ID, "", nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"id":"`+job.ID+`"`) {
		t.Fatalf("poll through the entry node: %d %s", r.status, r.body)
	}
	r, _ = ask(t, nil, esrv.URL, http.MethodGet, videosPath+"/"+job.ID+"/content", "", nil)
	if r.status != http.StatusAccepted {
		t.Fatalf("content of a running job through the entry node: %d %s", r.status, r.body)
	}
	r, _ = ask(t, nil, esrv.URL, http.MethodDelete, videosPath+"/"+job.ID, "", nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"deleted":true`) {
		t.Fatalf("cancel through the entry node: %d %s", r.status, r.body)
	}
	if r, _ = ask(t, nil, esrv.URL, http.MethodGet, videosPath+"/"+job.ID, "", nil); r.status != http.StatusNotFound {
		t.Fatalf("a cancelled job is forgotten: %d", r.status)
	}
}

func relayRoute(t *testing.T, table *Table, runtime, handoff string, breakEven uint32, prefill, decode *v1.RouteSeat) *v1.Route {
	t.Helper()
	table.SetHandoffs(func(id string) string {
		if id == runtime {
			return handoff
		}
		return ""
	})
	f := &v1.Formation{Id: "f1", Name: "m", Shape: v1.Shape_SHAPE_RELAY, RuntimeId: runtime, Repo: "r", Group: "g", Plan: &v1.FormationPlan{RelayBreakEvenPrompt: breakEven}, Request: &v1.RunRequest{Params: map[string]string{"n_parallel": "1"}}}
	head := &v1.Instance{Id: decode.GetInstanceId(), Endpoint: decode.GetEndpoint(), Name: "m"}
	return table.ServeFormation(f, head, v1.ApiFlavor_API_FLAVOR_OPENAI, []*v1.RouteSeat{prefill, decode}, nil, nil)
}

// The vLLM relay: the prefill seat answers with the connector's parameters and the decode seat
// takes them; a prefill that fails sends the request to the decode seat alone
func TestRelayNIXL(t *testing.T) {
	fail := false
	prefill := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if fail {
			refuse(w, http.StatusInternalServerError)
			return
		}
		kv, _ := body["kv_transfer_params"].(map[string]any)
		if body["max_tokens"] != float64(1) || kv["do_remote_decode"] != true {
			t.Errorf("prefill request %v", body)
		}
		writeJSON(w, http.StatusOK, map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": "x"}}}, "kv_transfer_params": map[string]any{"remote_block_ids": []int{1, 2}, "remote_engine_id": "e1", "remote_host": "10.0.0.5", "remote_port": 5600}})
	})
	decode := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		chatAnswer(w, "decoded")
	})
	g, table := newGateway(t)
	g.SetPeers(&fakePeers{self: "n1"})
	relayRoute(t, table, "vllm", estimate.HandoffNIXL, 4, &v1.RouteSeat{InstanceId: "ip", Endpoint: prefill.URL, NodeId: "n1", Role: "prefill", State: v1.InstanceState_INSTANCE_STATE_READY}, &v1.RouteSeat{InstanceId: "id", Endpoint: decode.URL, NodeId: "n2", Role: "decode", State: v1.InstanceState_INSTANCE_STATE_READY})
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	r, trace := ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "decoded") {
		t.Fatalf("relay: %d %s", r.status, r.body)
	}
	if trace.GetRelay() != relayBoth || strings.Join(trace.GetSeats(), ",") != "ip,id" || trace.GetNodeId() != "n1" {
		t.Fatalf("trace relay %q seats %v node %q", trace.GetRelay(), trace.GetSeats(), trace.GetNodeId())
	}
	kv, _ := decode.call(0).body["kv_transfer_params"].(map[string]any)
	if kv["do_remote_prefill"] != true || kv["remote_engine_id"] != "e1" {
		t.Fatalf("decode request carried %v", decode.call(0).body)
	}
	fail = true
	r, trace = ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil)
	if r.status != http.StatusOK || trace.GetRelay() != relayNoPrefill || strings.Join(trace.GetSeats(), ",") != "id" {
		t.Fatalf("prefill failure: %d relay %q seats %v", r.status, trace.GetRelay(), trace.GetSeats())
	}
	if _, has := decode.call(1).body["kv_transfer_params"]; has {
		t.Fatal("the decode seat alone takes the request as rendered")
	}
	if table.InFlight("ip") != 0 || table.InFlight("id") != 0 {
		t.Fatal("seat counts return to zero")
	}
}

// A break even of zero means the relay never pays, so every prompt goes to the decode seat
// alone; a prompt under the break even does too
func TestRelayBreakEven(t *testing.T) {
	prefill := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Error("the prefill seat must not be asked")
	})
	decode := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		chatAnswer(w, "decoded")
	})
	g, table := newGateway(t)
	g.SetPeers(&fakePeers{self: "n1"})
	seatP := &v1.RouteSeat{InstanceId: "ip", Endpoint: prefill.URL, NodeId: "n1", Role: "prefill", State: v1.InstanceState_INSTANCE_STATE_READY}
	seatD := &v1.RouteSeat{InstanceId: "id", Endpoint: decode.URL, NodeId: "n2", Role: "decode", State: v1.InstanceState_INSTANCE_STATE_READY}
	relayRoute(t, table, "vllm", estimate.HandoffNIXL, 0, seatP, seatD)
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	r, trace := ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil)
	if r.status != http.StatusOK || trace.GetRelay() != relayDecode || strings.Join(trace.GetSeats(), ",") != "id" {
		t.Fatalf("break even 0: %d relay %q seats %v", r.status, trace.GetRelay(), trace.GetSeats())
	}
	relayRoute(t, table, "vllm", estimate.HandoffNIXL, 100000, seatP, seatD)
	if _, trace = ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil); trace.GetRelay() != relayDecode {
		t.Fatalf("short prompt: relay %q", trace.GetRelay())
	}
	if prefill.count() != 0 {
		t.Fatal("the prefill seat was asked")
	}
	// A runtime declaring no handoff cannot relay.
	relayRoute(t, table, "nemo", "", 4, seatP, seatD)
	if r, _ := ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil); r.status != http.StatusServiceUnavailable || !strings.Contains(string(r.body), "nemo declares no relay handoff") {
		t.Fatalf("no handoff: %d %s", r.status, r.body)
	}
}

// The SGLang relay: both seats take the request with the bootstrap room at once, and the
// decode seat's answer streams once the prefill seat's outcome is known. A prefill that fails
// sends the request to the decode seat alone, nothing having streamed
func TestRelayBootstrap(t *testing.T) {
	fail := false
	prefill := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if fail {
			refuse(w, http.StatusServiceUnavailable)
			return
		}
		if body["bootstrap_host"] != "10.0.0.5" || body["bootstrap_port"] != float64(8998) || body["bootstrap_room"] == nil || body["max_tokens"] != float64(1) {
			t.Errorf("prefill request %v", body)
		}
		chatAnswer(w, "x")
	})
	decode := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if _, bootstrapped := body["bootstrap_room"]; bootstrapped && fail {
			// The cache never arrives, so the seat holds the request until the gateway lets go.
			<-r.Context().Done()
			return
		}
		chatStream(w, "decoded")
	})
	g, table := newGateway(t)
	g.SetPeers(&fakePeers{self: "n1"})
	relayRoute(t, table, "sglang", estimate.HandoffSGLangBootstrap, 4, &v1.RouteSeat{InstanceId: "ip", Endpoint: prefill.URL, NodeId: "n1", Role: "prefill", State: v1.InstanceState_INSTANCE_STATE_READY, Address: "10.0.0.5", AuxPort: 8998}, &v1.RouteSeat{InstanceId: "id", Endpoint: decode.URL, NodeId: "n2", Role: "decode", State: v1.InstanceState_INSTANCE_STATE_READY})
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hello there, how are you today"}]}`
	r, trace := ask(t, g, srv.URL, http.MethodPost, openaiChat, body, nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "decoded") || trace.GetRelay() != relayBoth || strings.Join(trace.GetSeats(), ",") != "ip,id" {
		t.Fatalf("relay: %d relay %q seats %v %s", r.status, trace.GetRelay(), trace.GetSeats(), r.body)
	}
	p, d := prefill.call(0).body, decode.call(0).body
	if p["bootstrap_room"] != d["bootstrap_room"] || d["bootstrap_host"] != "10.0.0.5" || d["bootstrap_port"] != float64(8998) {
		t.Fatalf("seats met in different rooms: %v %v", p, d)
	}
	fail = true
	r, trace = ask(t, g, srv.URL, http.MethodPost, openaiChat, body, nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "decoded") || trace.GetRelay() != relayNoPrefill || strings.Join(trace.GetSeats(), ",") != "id" {
		t.Fatalf("prefill failure: %d relay %q seats %v %s", r.status, trace.GetRelay(), trace.GetSeats(), r.body)
	}
	if strings.Count(string(r.body), "decoded") != 1 {
		t.Fatalf("the client saw one answer: %s", r.body)
	}
	if _, bootstrapped := decode.call(decode.count() - 1).body["bootstrap_room"]; bootstrapped {
		t.Fatal("the decode seat alone takes the request as rendered")
	}
}

// The llama.cpp relay: the prefill seat computes into a slot of its own and saves it, the file
// moves and is dropped from the prefill node, the decode seat restores it into a slot of its
// own, the file is dropped there too, and the slots are given back. A seat with every slot in
// use refuses another relay instead of sharing a slot
func TestRelaySlot(t *testing.T) {
	block := make(chan struct{})
	blocking := false
	prefill := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		switch {
		case r.URL.Path == openaiChat:
			if body["id_slot"] != float64(0) || body["cache_prompt"] != true || body["max_tokens"] != float64(1) {
				t.Errorf("prefill request %v", body)
			}
			chatAnswer(w, "x")
		case r.URL.Path == slotsPath+"0" && r.URL.Query().Get("action") == "save":
			writeJSON(w, http.StatusOK, map[string]any{"filename": body["filename"], "n_saved": 12})
		default:
			t.Errorf("prefill seat asked %s?%s", r.URL.Path, r.URL.RawQuery)
			refuse(w, http.StatusNotFound)
		}
	})
	decode := newSeat(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		switch {
		case r.URL.Path == openaiChat:
			if _, relayed := body["id_slot"]; relayed && (body["id_slot"] != float64(0) || body["cache_prompt"] != true) {
				t.Errorf("decode request %v", body)
			}
			if blocking {
				<-block
			}
			chatAnswer(w, "decoded")
		case r.URL.Path == slotsPath+"0" && r.URL.Query().Get("action") == "restore":
			writeJSON(w, http.StatusOK, map[string]any{"filename": body["filename"], "n_restored": 12})
		default:
			t.Errorf("decode seat asked %s?%s", r.URL.Path, r.URL.RawQuery)
			refuse(w, http.StatusNotFound)
		}
	})
	g, table := newGateway(t)
	peers := &fakePeers{self: "n1"}
	g.SetPeers(peers)
	relayRoute(t, table, "llamacpp", estimate.HandoffLlamaSlot, 4, &v1.RouteSeat{InstanceId: "ip", Endpoint: prefill.URL, NodeId: "n1", Role: "prefill", State: v1.InstanceState_INSTANCE_STATE_READY}, &v1.RouteSeat{InstanceId: "id", Endpoint: decode.URL, NodeId: "n2", Role: "decode", State: v1.InstanceState_INSTANCE_STATE_READY})
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	r, trace := ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil)
	if r.status != http.StatusOK || trace.GetRelay() != relayBoth || strings.Join(trace.GetSeats(), ",") != "ip,id" {
		t.Fatalf("relay: %d relay %q seats %v %s", r.status, trace.GetRelay(), trace.GetSeats(), r.body)
	}
	file := "relay-" + trace.GetId() + ".bin"
	if got := strings.Join(peers.moved, ";"); got != "f1 n2 n1 "+file {
		t.Fatalf("moves %q", got)
	}
	if got := strings.Join(peers.drops, ";"); got != "n1 f1 "+file+";n2 f1 "+file {
		t.Fatalf("drops %q", got)
	}
	if prefill.call(1).body["filename"] != file || decode.call(0).body["filename"] != file {
		t.Fatalf("save %v restore %v", prefill.call(1).body, decode.call(0).body)
	}
	// A relay in flight holds the decode slot; the next relay finds every slot in use.
	blocking = true
	first := make(chan reply, 1)
	go func() {
		r, _ := ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil)
		first <- r
	}()
	deadline := time.Now().Add(5 * time.Second)
	for decode.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if r, _ := ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil); r.status != http.StatusTooManyRequests || !strings.Contains(string(r.body), "slot") {
		t.Fatalf("second relay with the slot held: %d %s", r.status, r.body)
	}
	close(block)
	if r := <-first; r.status != http.StatusOK {
		t.Fatalf("first relay: %d %s", r.status, r.body)
	}
	// The move failing drops the file where it was and decodes alone.
	blocking = false
	peers.move = errors.New("link down")
	peers.moved, peers.drops = nil, nil
	r, trace = ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil)
	if r.status != http.StatusOK || trace.GetRelay() != relayNoPrefill {
		t.Fatalf("move failure: %d relay %q", r.status, trace.GetRelay())
	}
	if got := strings.Join(peers.drops, ";"); got != "n1 f1 relay-"+trace.GetId()+".bin" {
		t.Fatalf("drops after a failed move %q", got)
	}
	if _, held := g.slots.used["ip"]; held {
		t.Fatal("slots are given back")
	}
	if _, held := g.slots.used["id"]; held {
		t.Fatal("slots are given back")
	}
}

// A seat that refuses or fails passes the request to the next, once: with three replicas, the
// third is never asked, and a refusal by the second reaches the client
func TestFailoverOnce(t *testing.T) {
	statuses := map[string]int{}
	var mu sync.Mutex
	answer := func(name string) func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		return func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			mu.Lock()
			status := statuses[name]
			mu.Unlock()
			if status != 0 {
				refuse(w, status)
				return
			}
			chatAnswer(w, "from "+name)
		}
	}
	a, b, c := newSeat(t, answer("a")), newSeat(t, answer("b")), newSeat(t, answer("c"))
	g, table := newGateway(t)
	g.SetPeers(&fakePeers{self: "n1"})
	policy := &v1.AffinityPolicy{WindowSeconds: 600, SystemMessage: true, FirstUserMessage: true}
	replicaRoute(table, []*v1.RouteSeat{readySeat("ia", a.URL, "n1", 0), readySeat("ib", b.URL, "n1", 1), readySeat("ic", c.URL, "n1", 2)}, policy, nil)
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	set := func(name string, status int) {
		mu.Lock()
		statuses[name] = status
		mu.Unlock()
	}
	set("a", http.StatusServiceUnavailable)
	r, trace := ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "from b") || strings.Join(trace.GetSeats(), ",") != "ib" {
		t.Fatalf("passed once: %d %s seats %v", r.status, r.body, trace.GetSeats())
	}
	// The conversation now sticks to b, which refuses: it passes to the next once and stops there.
	set("b", http.StatusTooManyRequests)
	set("a", 0)
	r, _ = ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "from a") {
		t.Fatalf("refusal by the pinned seat passes on: %d %s", r.status, r.body)
	}
	set("a", http.StatusInternalServerError)
	set("b", http.StatusInternalServerError)
	other := `{"model":"m","messages":[{"role":"user","content":"a fresh conversation"}]}`
	r, _ = ask(t, g, srv.URL, http.MethodPost, openaiChat, other, nil)
	if r.status != http.StatusInternalServerError || c.count() != 0 {
		t.Fatalf("two refusals reach the client, the third seat untouched: %d, c asked %d", r.status, c.count())
	}
	// A seat that never answers passes too, and the translated path passes the same way.
	a.Close()
	set("b", 0)
	anthropic := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"a fresh conversation"}]}`
	r, trace = ask(t, g, srv.URL, http.MethodPost, anthropicMessages, anthropic, map[string]string{"anthropic-version": "2023-06-01"})
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "from b") || strings.Join(trace.GetSeats(), ",") != "ib" || !trace.GetTranslated() {
		t.Fatalf("translated failover: %d %s seats %v", r.status, r.body, trace.GetSeats())
	}
	if table.InFlight("ia") != 0 || table.InFlight("ib") != 0 || table.InFlight("ic") != 0 {
		t.Fatal("seat counts return to zero")
	}
}

// Replicas: the seat with the fewest requests comes first, ties to the best link from the entry
// node, a conversation's prefix sticks to the seat it went to, the in flight cap applies per
// seat, the head is counted once, and a plan without an affinity policy refuses to serve
func TestReplicaPicking(t *testing.T) {
	answer := func(name string) func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		return func(w http.ResponseWriter, r *http.Request, body map[string]any) { chatAnswer(w, "from "+name) }
	}
	a, b := newSeat(t, answer("a")), newSeat(t, answer("b"))
	g, table := newGateway(t)
	g.SetPeers(&fakePeers{self: "n1", links: map[string]int{"n1>na": 1, "n1>nb": 3, "n3>nb": 1, "n3>na": 3}})
	policy := &v1.AffinityPolicy{WindowSeconds: 600, SystemMessage: true, FirstUserMessage: true}
	seats := []*v1.RouteSeat{readySeat("ia", a.URL, "na", 0), readySeat("ib", b.URL, "nb", 1)}
	replicaRoute(table, seats, nil, nil)
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	if r, _ := ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil); r.status != http.StatusBadGateway || !strings.Contains(string(r.body), "affinity policy") {
		t.Fatalf("no affinity policy: %d %s", r.status, r.body)
	}
	replicaRoute(table, seats, policy, &v1.Policy{MaxInFlight: 1})
	// Idle seats tie, and the link from the entry node decides: from here a is nearer, from n3 b is.
	r, trace := ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil)
	if !strings.Contains(string(r.body), "from a") || strings.Join(trace.GetSeats(), ",") != "ia" {
		t.Fatalf("nearest seat from here: %s %v", r.body, trace.GetSeats())
	}
	fresh := `{"model":"m","messages":[{"role":"user","content":"another conversation"}]}`
	if r, _ := ask(t, g, srv.URL, http.MethodPost, openaiChat, fresh, map[string]string{entryHeader: "n3"}); !strings.Contains(string(r.body), "from b") {
		t.Fatalf("nearest seat from the entry node: %s", r.body)
	}
	// The first conversation stays on a even while a is busier.
	releaseA := table.HoldSeat("ia")
	if r, _ := ask(t, g, srv.URL, http.MethodPost, openaiChat, `{"model":"m","messages":[{"role":"user","content":"yet another"}]}`, nil); !strings.Contains(string(r.body), "from b") {
		t.Fatalf("least loaded: %s", r.body)
	}
	releaseA()
	// The affinity pins the conversation to a even over the cap: with a at its cap, b takes it.
	releaseA = table.HoldSeat("ia")
	if r, _ := ask(t, g, srv.URL, http.MethodPost, openaiChat, chatBody, nil); !strings.Contains(string(r.body), "from b") {
		t.Fatalf("a seat at its cap takes nothing: %s", r.body)
	}
	releaseB := table.HoldSeat("ib")
	if r, _ := ask(t, g, srv.URL, http.MethodPost, openaiChat, fresh, nil); r.status != http.StatusTooManyRequests {
		t.Fatalf("every seat at its cap: %d %s", r.status, r.body)
	}
	rt, _ := table.Lookup("m")
	if rt.GetInFlight() != 0 || rt.GetSeats()[0].GetInFlight() != 1 || rt.GetSeats()[1].GetInFlight() != 1 {
		t.Fatalf("route %d, seats %d %d: seats are counted apart from the route", rt.GetInFlight(), rt.GetSeats()[0].GetInFlight(), rt.GetSeats()[1].GetInFlight())
	}
	releaseA()
	releaseB()
	if key := prefixKey("m", &Chat{Messages: []Message{{Role: "user", Parts: []Part{{Type: "text", Text: "hi"}}}}}, &v1.AffinityPolicy{}); key != "" {
		t.Fatal("a policy covering nothing pins nothing")
	}
}

// Replicas of a media route hold one job per seat, pass a job a seat refuses to the next once,
// and record the seat that took it
func TestMediaReplicas(t *testing.T) {
	refusing := true
	image := func(name string) func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		return func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			switch {
			case r.URL.Path == sdcppImageJob:
				if name == "a" && refusing {
					refuse(w, http.StatusServiceUnavailable)
					return
				}
				writeJSON(w, http.StatusAccepted, map[string]any{"id": "job-" + name, "status": "queued"})
			default:
				writeJSON(w, http.StatusOK, map[string]any{"id": "job-" + name, "status": "completed", "result": map[string]any{"output_format": "png", "images": []map[string]any{{"index": 0, "b64_json": "aGk="}}}})
			}
		}
	}
	a, b := newSeat(t, image("a")), newSeat(t, image("b"))
	g, table := newGateway(t)
	g.SetPeers(&fakePeers{self: "n1"})
	table.SetModes("ia", []string{"img_gen"})
	f := &v1.Formation{Id: "f1", Name: "v", Shape: v1.Shape_SHAPE_REPLICAS, RuntimeId: "sdcpp", Repo: "r", Group: "g", Plan: &v1.FormationPlan{Affinity: &v1.AffinityPolicy{}}}
	table.ServeFormation(f, &v1.Instance{Id: "ia", Endpoint: a.URL, Name: "v"}, v1.ApiFlavor_API_FLAVOR_SDCPP, []*v1.RouteSeat{readySeat("ia", a.URL, "n1", 0), readySeat("ib", b.URL, "n1", 1)}, nil, nil)
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	body := `{"model":"v","prompt":"a cat","size":"64x64"}`
	r, trace := ask(t, g, srv.URL, http.MethodPost, imagesPath, body, nil)
	if r.status != http.StatusOK || strings.Join(trace.GetSeats(), ",") != "ib" {
		t.Fatalf("passed once: %d %s seats %v", r.status, r.body, trace.GetSeats())
	}
	// One job per seat: with b busy only a is free, and a refuses, so nothing else is tried.
	releaseB := table.HoldSeat("ib")
	if r, _ := ask(t, g, srv.URL, http.MethodPost, imagesPath, body, nil); r.status != http.StatusServiceUnavailable {
		t.Fatalf("the one free seat refused: %d %s", r.status, r.body)
	}
	releaseA := table.HoldSeat("ia")
	if r, _ := ask(t, g, srv.URL, http.MethodPost, imagesPath, body, nil); r.status != http.StatusTooManyRequests {
		t.Fatalf("every seat has a job: %d %s", r.status, r.body)
	}
	releaseA()
	releaseB()
	refusing = false
	if r, trace := ask(t, g, srv.URL, http.MethodPost, imagesPath, body, nil); r.status != http.StatusOK || strings.Join(trace.GetSeats(), ",") != "ia" {
		t.Fatalf("free seats take the job: %d seats %v", r.status, trace.GetSeats())
	}
	if table.InFlight("ia") != 0 || table.InFlight("ib") != 0 {
		t.Fatal("job counts return to zero")
	}
}

// A forwarded formation's route follows the formation: starting is pending, ready is ready,
// degraded is draining, a collision is an error, and so is a formation without an endpoint
func TestForwardTransitions(t *testing.T) {
	_, table := newGateway(t)
	f := &v1.Formation{Id: "f1", Name: "big", Conductor: "n2", ConductorName: "box", Shape: v1.Shape_SHAPE_CHAIN, State: v1.FormationState_FORMATION_STATE_STARTING, Endpoint: "http://10.0.0.2:8485", Repo: "r", Group: "g", RuntimeId: "llamacpp", Seats: []*v1.Seat{{NodeId: "n2", Role: "head", InstanceId: "h"}}}
	r, err := table.Forward(f, v1.ApiFlavor_API_FLAVOR_OPENAI)
	if err != nil || r.GetState() != v1.RouteState_ROUTE_STATE_PENDING || !r.GetForwarded() || r.GetNodeId() != "n2" {
		t.Fatalf("starting: %v %v", r, err)
	}
	if _, _, _, err := table.Acquire("big", true); !errors.Is(err, ErrPending) {
		t.Fatalf("pending, got %v", err)
	}
	f.State = v1.FormationState_FORMATION_STATE_READY
	if r, err = table.Forward(f, v1.ApiFlavor_API_FLAVOR_OPENAI); err != nil || r.GetState() != v1.RouteState_ROUTE_STATE_READY {
		t.Fatalf("ready: %v %v", r, err)
	}
	got, _, release, err := table.Acquire("big", true)
	if err != nil || got.GetInFlight() != 1 {
		t.Fatalf("acquire %v %v", got, err)
	}
	release()
	f.State = v1.FormationState_FORMATION_STATE_DEGRADED
	if r, err = table.Forward(f, v1.ApiFlavor_API_FLAVOR_OPENAI); err != nil || r.GetState() != v1.RouteState_ROUTE_STATE_DRAINING {
		t.Fatalf("degraded: %v %v", r, err)
	}
	if _, _, _, err := table.Acquire("big", true); !errors.Is(err, ErrDraining) {
		t.Fatalf("draining, got %v", err)
	}
	other := &v1.Formation{Id: "f2", Name: "big", Conductor: "n3", ConductorName: "other", State: v1.FormationState_FORMATION_STATE_READY, Endpoint: "http://10.0.0.3:8485"}
	if _, err := table.Forward(other, v1.ApiFlavor_API_FLAVOR_OPENAI); err == nil || !strings.Contains(err.Error(), "f2 on other") {
		t.Fatalf("collision between formations: %v", err)
	}
	if _, err := table.Forward(&v1.Formation{Id: "f3", Name: "none", Conductor: "n3", ConductorName: "other", State: v1.FormationState_FORMATION_STATE_READY}, v1.ApiFlavor_API_FLAVOR_OPENAI); err == nil || !strings.Contains(err.Error(), "no gateway endpoint") {
		t.Fatalf("no endpoint: %v", err)
	}
	table.Set("local", "i1", "", "http://127.0.0.1:1", "r:g", "local", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)
	if _, err := table.Forward(&v1.Formation{Id: "f4", Name: "local", Conductor: "n3", ConductorName: "other", State: v1.FormationState_FORMATION_STATE_READY, Endpoint: "http://10.0.0.3:8485"}, v1.ApiFlavor_API_FLAVOR_OPENAI); err == nil || !strings.Contains(err.Error(), "served on this node") {
		t.Fatalf("collision with a local route: %v", err)
	}
	if gone := table.RemoveFormation("f1"); len(gone) != 1 {
		t.Fatalf("removed %v", gone)
	}
}

// Every ready route of a member is forwarded from its record, a route it drops leaves, a
// draining one drains, a name served here collides, and a member gone takes its routes with it
func TestForwardNode(t *testing.T) {
	bus := events.New()
	table, err := OpenTable(context.Background(), nil, bus, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	table.Set("local", "i1", "", "http://127.0.0.1:1", "r:g", "local", v1.ApiFlavor_API_FLAVOR_OPENAI, nil, nil)
	node := &v1.Node{Id: "n2", Name: "box", Address: "10.0.0.2:8485", State: v1.NodeState_NODE_STATE_READY, Routes: []*v1.Route{
		{Name: "solo", InstanceId: "x", Endpoint: "http://127.0.0.1:9000", State: v1.RouteState_ROUTE_STATE_READY, Model: "r:g", Api: v1.ApiFlavor_API_FLAVOR_OPENAI, Modes: []string{"img_gen"}},
		{Name: "pending", InstanceId: "", State: v1.RouteState_ROUTE_STATE_PENDING},
		{Name: "local", InstanceId: "y", Endpoint: "http://127.0.0.1:9001", State: v1.RouteState_ROUTE_STATE_READY},
		{Name: "theirs", FormationId: "f9", State: v1.RouteState_ROUTE_STATE_READY, Endpoint: "http://127.0.0.1:9002"},
	}}
	err = table.ForwardNode(node)
	if err == nil || !strings.Contains(err.Error(), "route local is served on this node") {
		t.Fatalf("collision: %v", err)
	}
	solo, ok := table.Lookup("solo")
	if !ok || !solo.GetForwarded() || solo.GetNodeId() != "n2" || solo.GetEndpoint() != "http://10.0.0.2:8485" || solo.GetState() != v1.RouteState_ROUTE_STATE_READY || solo.GetModes()[0] != "img_gen" || solo.GetInstanceId() != "" {
		t.Fatalf("solo %v", solo)
	}
	if _, ok := table.Lookup("pending"); ok {
		t.Fatal("a route not ready is not forwarded")
	}
	if _, ok := table.Lookup("theirs"); ok {
		t.Fatal("a formation's route is the formation record's")
	}
	if local, _ := table.Lookup("local"); local.GetForwarded() {
		t.Fatal("the local route stays")
	}
	node.Routes[0].State = v1.RouteState_ROUTE_STATE_DRAINING
	node.Routes = append(node.Routes, &v1.Route{Name: "second", InstanceId: "z", Endpoint: "http://127.0.0.1:9003", State: v1.RouteState_ROUTE_STATE_READY})
	table.ForwardNode(node)
	if solo, _ := table.Lookup("solo"); solo.GetState() != v1.RouteState_ROUTE_STATE_DRAINING {
		t.Fatalf("draining follows: %v", solo)
	}
	if _, ok := table.Lookup("second"); !ok {
		t.Fatal("a new route is forwarded")
	}
	node.Routes = node.Routes[1:]
	table.ForwardNode(node)
	if _, ok := table.Lookup("solo"); ok {
		t.Fatal("a route the member dropped leaves")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	table.Follow(ctx)
	bus.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_DELETED, "n2", node)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := table.Lookup("second"); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := table.Lookup("second"); ok {
		t.Fatal("a member gone takes its routes with it")
	}
	if err := table.ForwardNode(&v1.Node{Id: "n4", Name: "quiet"}); err == nil || !strings.Contains(err.Error(), "no mesh address") {
		t.Fatalf("no address: %v", err)
	}
}

func TestWithFieldsAndRetryable(t *testing.T) {
	out, err := withFields([]byte(`{"model":"m","max_tokens":5}`), map[string]any{"max_tokens": 1, "kv_transfer_params": map[string]any{"do_remote_decode": true}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["max_tokens"]) != "1" || string(got["model"]) != `"m"` || len(got["kv_transfer_params"]) == 0 {
		t.Fatalf("%s", out)
	}
	if _, err := withFields([]byte(`[]`), nil); err == nil {
		t.Fatal("a list is not a request object")
	}
	if !retryable(&net.OpError{Op: "dial", Err: errors.New("refused")}) || retryable(context.Canceled) || retryable(nil) {
		t.Fatal("dial failures pass to the next seat, cancellations do not")
	}
	if !refused(http.StatusTooManyRequests) || !refused(http.StatusServiceUnavailable) || !refused(http.StatusBadGateway) || refused(http.StatusBadRequest) || refused(http.StatusOK) {
		t.Fatal("429 and 5xx are refusals, 4xx answers are not")
	}
	pool := newSlotPool()
	if id, ok := pool.take("s", 2); !ok || id != 0 {
		t.Fatalf("first slot %d %v", id, ok)
	}
	if id, ok := pool.take("s", 2); !ok || id != 1 {
		t.Fatalf("second slot %d %v", id, ok)
	}
	if _, ok := pool.take("s", 2); ok {
		t.Fatal("a third slot of two")
	}
	pool.give("s", 0)
	if id, ok := pool.take("s", 2); !ok || id != 0 {
		t.Fatalf("slot given back %d %v", id, ok)
	}
	if id, ok := pool.take("t", 0); !ok || id != 0 {
		t.Fatalf("a seat without a slot count has one slot: %d %v", id, ok)
	}
	var buf bytes.Buffer
	jobs := newForwardedJobs()
	resp := &http.Response{StatusCode: http.StatusAccepted, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"video_1","status":"queued"}`))}
	if err := jobs.remember(resp, "n2"); err != nil {
		t.Fatal(err)
	}
	io.Copy(&buf, resp.Body)
	if node, ok := jobs.node("video_1"); !ok || node != "n2" || !strings.Contains(buf.String(), "video_1") {
		t.Fatalf("remembered %q %v body %s", node, ok, buf.String())
	}
}
