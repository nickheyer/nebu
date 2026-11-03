package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
)

// A stable-diffusion.cpp server finishing every image and video job at once
func sdcppServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == sdcppImageJob:
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"id":"img1","status":"queued"}`)
		case r.Method == http.MethodPost && r.URL.Path == sdcppVideoJob:
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"id":"vid1","status":"queued"}`)
		case r.URL.Path == sdcppJobs+"img1":
			io.WriteString(w, `{"id":"img1","status":"completed","result":{"output_format":"png","images":[{"index":0,"b64_json":"aW1n"}]}}`)
		case r.URL.Path == sdcppJobs+"vid1":
			io.WriteString(w, `{"id":"vid1","status":"completed","result":{"output_format":"webm","mime_type":"video/webm","fps":16,"frame_count":17,"b64_json":"dmlkZW8="}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Images and videos run on media pools through the media configuration, one job per endpoint, videos kept per account
func TestMedia(t *testing.T) {
	h := newHarness(t, func(s *setup) { s.keys = []string{"alice-key", "bob-key"} })
	srv := sdcppServer(t)
	pool := h.publish("draw", "sd", "sdcpp", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "sd"}, srv.URL)
	h.inst.mu.Lock()
	h.inst.modes["draw-"+hostOf(srv)] = []string{"img_gen", "vid_gen"}
	h.inst.mu.Unlock()
	waitFor(t, "media capacity", func() bool {
		eps := h.store.Endpoints(pool, nil)
		n, ok := datalayer.Attribute[int](eps[0], datalayer.CollectorCapacity)
		return ok && n == 1
	})
	alice := []string{"Authorization", "Bearer alice-key"}
	r := h.do(http.MethodPost, "/v1/images/generations", `{"model":"draw","prompt":"a cat","size":"512x512"}`, alice...)
	if r.status != http.StatusOK || r.json(t)["data"].([]any)[0].(map[string]any)["b64_json"] != "aW1n" {
		t.Fatalf("image %d: %s", r.status, r.body)
	}
	if r.header.Get(requestcontrol.HeaderServed) != hostOf(srv) {
		t.Fatalf("served %q", r.header.Get(requestcontrol.HeaderServed))
	}
	id := r.header.Get(TraceHeader)
	waitFor(t, "image trace", func() bool {
		tr, ok, _ := h.g.Traces().Get(context.Background(), Viewer{All: true}, id)
		return ok && tr.GetFinishedAt() != nil && tr.GetProfile() == ConfigMedia
	})
	r = h.do(http.MethodPost, "/v1/videos", `{"model":"draw","prompt":"a cat running","seconds":1}`, alice...)
	if r.status != http.StatusAccepted {
		t.Fatalf("video %d: %s", r.status, r.body)
	}
	vid := r.json(t)["id"].(string)
	waitFor(t, "video done", func() bool {
		got := h.do(http.MethodGet, "/v1/videos/"+vid, "", alice...)
		return got.status == http.StatusOK && strings.Contains(string(got.body), `"status":"completed"`)
	})
	if got := h.do(http.MethodGet, "/v1/videos/"+vid+"/content", "", alice...); got.status != http.StatusOK || string(got.body) != "video" {
		t.Fatalf("content %d %q", got.status, got.body)
	}
	if got := h.do(http.MethodGet, "/v1/videos/"+vid, "", "Authorization", "Bearer bob-key"); got.status != http.StatusNotFound {
		t.Fatalf("bob reached alice's video: %d", got.status)
	}
	var list struct{ Data []json.RawMessage }
	json.Unmarshal(h.do(http.MethodGet, "/v1/videos", "", "Authorization", "Bearer bob-key").body, &list)
	if len(list.Data) != 0 {
		t.Fatalf("bob lists %d videos", len(list.Data))
	}
	if got := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"draw","messages":[{"role":"user","content":"hi"}]}`, alice...); got.status < http.StatusBadRequest {
		t.Fatalf("a chat to a media pool answered %d", got.status)
	}
}

// A disaggregated configuration without metric filters, prefill joining every request
const pdTest = `
plugins:
- type: ready-filter
  name: ready
- type: role-filter
  name: prefill-role
  parameters:
    role: prefill
- type: role-filter
  name: decode-role
  parameters:
    role: decode
- type: max-score-picker
  name: picker
- type: pd-profile-handler
  name: handler
  parameters:
    prefillProfile: prefill
    decodeProfile: decode
    threshold: 0
profiles:
- name: prefill
  plugins:
  - pluginRef: ready
  - pluginRef: prefill-role
  - pluginRef: picker
- name: decode
  plugins:
  - pluginRef: ready
  - pluginRef: decode-role
  - pluginRef: picker
profileHandler:
  pluginRef: handler
`

// A pool of prefill and decode seats relays the first round through the vLLM handoff
func TestRelayNIXL(t *testing.T) {
	h := newHarness(t, func(s *setup) {
		s.runtimes.api["nixl"] = v1.ApiFlavor_API_FLAVOR_OPENAI
		s.runtimes.handoff["nixl"] = estimate.HandoffNIXL
	})
	var mu sync.Mutex
	var prefilled, decoded map[string]any
	answer := `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"relayed"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
	prefill := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		json.NewDecoder(r.Body).Decode(&prefilled)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, strings.Replace(answer, `"usage"`, `"kv_transfer_params":{"remote_engine_id":"e1","remote_block_ids":[1,2]},"usage"`, 1))
	}))
	defer prefill.Close()
	decode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		json.NewDecoder(r.Body).Decode(&decoded)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, answer)
	}))
	defer decode.Close()
	ctx := context.Background()
	if _, err := h.mgr.PutSchedulerConfig(ctx, &v1.SchedulerConfig{Name: "pdtest", Spec: &v1.SchedulerConfigSpec{Yaml: pdTest}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pd configuration hosted", func() bool { _, ok := h.g.hosting.get("pdtest"); return ok })
	owner := pools.OwnerRef{Kind: pools.OwnerFormation, ID: "f1"}
	mem := pools.Membership{Model: key("pd"), Runtime: "nixl", Protocol: datalayer.AppProtocolHTTP, Concurrency: 4, SchedulerConfig: "pdtest", Metrics: &v1.MetricNames{HealthPath: "/health"}}
	pa, _ := datalayer.ParseAddress(hostOf(prefill))
	da, _ := datalayer.ParseAddress(hostOf(decode))
	if err := h.mgr.Publish(ctx, owner, mem, []pools.Published{
		{InstanceID: "p1", NodeID: testNode, Address: pa, Role: datalayer.RolePrefill, Ready: true},
		{InstanceID: "d1", NodeID: testNode, Address: da, Role: datalayer.RoleDecode, Ready: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.NameRoute(ctx, owner, "chat", pools.RouteSpec{ServedName: "m"}); err != nil {
		t.Fatal(err)
	}
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "relayed") || r.header.Get(requestcontrol.HeaderServed) != hostOf(decode) {
		t.Fatalf("status %d served %q: %s", r.status, r.header.Get(requestcontrol.HeaderServed), r.body)
	}
	mu.Lock()
	defer mu.Unlock()
	pkv, _ := prefilled["kv_transfer_params"].(map[string]any)
	dkv, _ := decoded["kv_transfer_params"].(map[string]any)
	if prefilled["max_tokens"] != float64(1) || pkv["do_remote_decode"] != true || dkv["do_remote_prefill"] != true || dkv["remote_engine_id"] != "e1" {
		t.Fatalf("prefill got %v, decode got %v", prefilled, decoded)
	}
	id := r.header.Get(TraceHeader)
	waitFor(t, "relay trace", func() bool {
		tr, ok, _ := h.g.Traces().Get(ctx, Viewer{All: true}, id)
		return ok && tr.GetFinishedAt() != nil && tr.GetRelay() == relayBoth && len(tr.GetSeats()) == 2
	})
}

// Records slot file moves and drops a relay asks of the mesh
type slotPeers struct {
	mu    sync.Mutex
	moves []string
	drops []string
}

func (p *slotPeers) Self() string { return testNode }

func (p *slotPeers) Dial(context.Context, string) (string, *http.Client, error) {
	return "", nil, io.EOF
}

func (p *slotPeers) MoveSlotTo(_ context.Context, formation, to, from, file string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.moves = append(p.moves, formation+":"+from+">"+to+":"+file)
	return nil
}

func (p *slotPeers) DropSlotOn(_ context.Context, node, formation, file string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.drops = append(p.drops, node+":"+formation+":"+file)
	return nil
}

// Publishes a prefill and a decode seat of one formation under the disaggregated test configuration
func (h *harness) seats(runtime string, prefill, decode string, prefillLabels map[string]string) {
	h.t.Helper()
	ctx := context.Background()
	if _, err := h.mgr.PutSchedulerConfig(ctx, &v1.SchedulerConfig{Name: "pdtest", Spec: &v1.SchedulerConfigSpec{Yaml: pdTest}}); err != nil {
		h.t.Fatal(err)
	}
	waitFor(h.t, "pd configuration hosted", func() bool { _, ok := h.g.hosting.get("pdtest"); return ok })
	owner := pools.OwnerRef{Kind: pools.OwnerFormation, ID: "f1"}
	mem := pools.Membership{Model: key("pd"), Runtime: runtime, Protocol: datalayer.AppProtocolHTTP, Concurrency: 2, SchedulerConfig: "pdtest", Metrics: &v1.MetricNames{HealthPath: "/health"}}
	pa, _ := datalayer.ParseAddress(prefill)
	da, _ := datalayer.ParseAddress(decode)
	if err := h.mgr.Publish(ctx, owner, mem, []pools.Published{
		{InstanceID: "p1", NodeID: testNode, Address: pa, Role: datalayer.RolePrefill, Ready: true, Labels: prefillLabels},
		{InstanceID: "d1", NodeID: testNode, Address: da, Role: datalayer.RoleDecode, Ready: true},
	}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.mgr.NameRoute(ctx, owner, "chat", pools.RouteSpec{ServedName: "m"}); err != nil {
		h.t.Fatal(err)
	}
}

// Records every request a seat gets by path and answers a chat completion
type seat struct {
	mu    sync.Mutex
	paths []string
	body  []map[string]any
	srv   *httptest.Server
}

func newSeat(t *testing.T) *seat {
	s := &seat{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.paths, s.body = append(s.paths, r.URL.RequestURI()), append(s.body, body)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"seated"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *seat) got() ([]string, []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...), append([]map[string]any(nil), s.body...)
}

// The llama.cpp relay answers from the decode seat at once and warms a decode slot through save, move and restore
func TestRelaySlotWarm(t *testing.T) {
	peers := &slotPeers{}
	h := newHarness(t, func(s *setup) {
		s.peers = peers
		s.runtimes.api["llamaslot"] = v1.ApiFlavor_API_FLAVOR_OPENAI
		s.runtimes.handoff["llamaslot"] = estimate.HandoffLlamaSlot
	})
	prefill, decode := newSeat(t), newSeat(t)
	h.seats("llamaslot", hostOf(prefill.srv), hostOf(decode.srv), nil)
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), "seated") || r.header.Get(requestcontrol.HeaderServed) != hostOf(decode.srv) {
		t.Fatalf("status %d served %q: %s", r.status, r.header.Get(requestcontrol.HeaderServed), r.body)
	}
	waitFor(t, "slot restored on the decode seat", func() bool {
		paths, _ := decode.got()
		return len(paths) == 2 && strings.Contains(paths[1], "action=restore")
	})
	paths, bodies := prefill.got()
	if len(paths) != 2 || paths[0] != "/v1/chat/completions" || bodies[0]["max_tokens"] != float64(1) || bodies[0]["cache_prompt"] != true || !strings.Contains(paths[1], "action=save") {
		t.Fatalf("prefill seat got %v %v", paths, bodies)
	}
	peers.mu.Lock()
	defer peers.mu.Unlock()
	if len(peers.moves) != 1 || len(peers.drops) != 1 {
		t.Fatalf("moves %v drops %v", peers.moves, peers.drops)
	}
	id := r.header.Get(TraceHeader)
	waitFor(t, "warm trace", func() bool {
		tr, ok, _ := h.g.Traces().Get(context.Background(), Viewer{All: true}, id)
		return ok && tr.GetFinishedAt() != nil && tr.GetRelay() == relayWarm
	})
}

// The SGLang relay sends both seats the request at once with the prefill seat's bootstrap address and one room
func TestRelayBootstrap(t *testing.T) {
	h := newHarness(t, func(s *setup) {
		s.runtimes.api["sgpd"] = v1.ApiFlavor_API_FLAVOR_OPENAI
		s.runtimes.handoff["sgpd"] = estimate.HandoffSGLangBootstrap
	})
	prefill, decode := newSeat(t), newSeat(t)
	h.seats("sgpd", hostOf(prefill.srv), hostOf(decode.srv), map[string]string{LabelBootstrapPort: "8998"})
	r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
	if r.status != http.StatusOK || r.header.Get(requestcontrol.HeaderServed) != hostOf(decode.srv) {
		t.Fatalf("status %d served %q: %s", r.status, r.header.Get(requestcontrol.HeaderServed), r.body)
	}
	_, pb := prefill.got()
	_, db := decode.got()
	if len(pb) != 1 || len(db) != 1 || pb[0]["bootstrap_room"] != db[0]["bootstrap_room"] || pb[0]["bootstrap_port"] != float64(8998) || db[0]["bootstrap_host"] != "127.0.0.1" || pb[0]["stream"] != false {
		t.Fatalf("prefill got %v, decode got %v", pb, db)
	}
}
