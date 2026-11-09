package requestcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// A relay recording what a handoff asks of the gateway, answering from scripted seats
type fakeRelay struct {
	mu       sync.Mutex
	body     []byte
	wholeErr error
	prefill  string
	besideCh chan []byte
	posts    []string
	moves    []string
	drops    []string
	outcomes []string
	sent     []byte
	sentTo   []byte
	files    SlotFiles
	postErr  map[string]error
	hold     chan struct{}
	warns    []string
}

func (r *fakeRelay) Path() string { return "/v1/chat/completions" }
func (r *fakeRelay) Body() []byte { return r.body }

func (r *fakeRelay) Whole(_ context.Context, body []byte, eps []datalayer.Endpoint) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.posts = append(r.posts, "whole "+string(eps[0].ID())+" "+string(body))
	return []byte(r.prefill), r.wholeErr
}

func (r *fakeRelay) Beside(_ context.Context, body []byte, ep datalayer.Endpoint) ([]byte, error) {
	r.besideCh <- body
	return []byte(`{}`), r.wholeErr
}

func (r *fakeRelay) Send(_ context.Context, body []byte, eps []datalayer.Endpoint) (*http.Response, datalayer.Endpoint, error) {
	r.mu.Lock()
	r.sentTo = body
	r.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("answer"))}, eps[0], nil
}

func (r *fakeRelay) Relayed(outcome string, sent []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes = append(r.outcomes, outcome)
	if sent != nil {
		r.sent = sent
	}
}

func (r *fakeRelay) PrefillFailed(why error) error {
	r.Relayed(RelayNoPrefill, nil)
	return errors.Join(errPrefill, why)
}

func (r *fakeRelay) TraceID() string { return "t1" }

func (r *fakeRelay) Post(_ context.Context, ep datalayer.Endpoint, path string, body []byte) error {
	if r.hold != nil && strings.Contains(path, "restore") {
		<-r.hold
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.posts = append(r.posts, string(ep.ID())+" "+path)
	return r.postErr[path]
}

func (r *fakeRelay) SlotFiles() SlotFiles { return r.files }

func (r *fakeRelay) Warn(msg string, _ ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warns = append(r.warns, msg)
}

func (r *fakeRelay) MoveSlotTo(_ context.Context, formation, to, from, file string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.moves = append(r.moves, formation+":"+from+">"+to+":"+file)
	return nil
}

func (r *fakeRelay) DropSlotOn(_ context.Context, node, formation, file string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drops = append(r.drops, node+":"+formation+":"+file)
	return nil
}

func (r *fakeRelay) recorded() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.outcomes), slices.Clone(r.posts)
}

var errPrefill = errors.New("prefill refused")

func seatAt(id, node string, port int, labels map[string]string) *fakeEndpoint {
	l := map[string]string{datalayer.LabelInstance: id, datalayer.LabelNode: node, datalayer.LabelFormation: "f1"}
	for k, v := range labels {
		l[k] = v
	}
	return &fakeEndpoint{spec: datalayer.EndpointSpec{ID: datalayer.EndpointID(id), Address: datalayer.Address{Host: "10.0.0." + id[len(id)-1:], Port: port}, Labels: l, Ready: true}}
}

// Every protocol is a registered handoff plugin type and an unknown type is refused
func TestHandoffRegistry(t *testing.T) {
	if got := HandoffTypes(); !slices.Equal(got, []string{HandoffLlamaSlot, HandoffNIXL, HandoffSGLangBootstrap}) {
		t.Fatalf("types %v", got)
	}
	for _, typ := range HandoffTypes() {
		h, err := NewHandoff(typ)
		if err != nil || h.TypedName().Type != typ {
			t.Fatalf("handoff %s built %v %v", typ, h, err)
		}
	}
	if _, err := NewHandoff("mooncake"); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("an unknown handoff %v", err)
	}
	a, _ := NewHandoff(HandoffLlamaSlot)
	b, _ := NewHandoff(HandoffLlamaSlot)
	if a.(*slotWarm).slots == b.(*slotWarm).slots {
		t.Fatal("two slot handoffs share one slot pool")
	}
}

// The NIXL handoff prefills one token for connector parameters and hands them to decode, refusing a prefill that gives none
func TestNIXLHandoff(t *testing.T) {
	prefill, decode := []datalayer.Endpoint{seatAt("p1", "n1", 1, nil)}, []datalayer.Endpoint{seatAt("d1", "n1", 2, nil)}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	r := &fakeRelay{body: body, prefill: `{"kv_transfer_params":{"remote_engine_id":"e1","remote_block_ids":[1]}}`}
	h, _ := NewHandoff(HandoffNIXL)
	resp, ep, err := h.Relay(context.Background(), r, prefill, decode)
	if err != nil || ep.ID() != "d1" {
		t.Fatalf("relay %v %v", ep, err)
	}
	resp.Body.Close()
	_, posts := r.recorded()
	var pre, dec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(posts[0], "whole p1 ")), &pre); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(r.sentTo, &dec); err != nil {
		t.Fatal(err)
	}
	pkv, dkv := pre["kv_transfer_params"].(map[string]any), dec["kv_transfer_params"].(map[string]any)
	if pre["max_tokens"] != float64(1) || pre["stream"] != false || pkv["do_remote_decode"] != true || dkv["do_remote_prefill"] != true || dkv["do_remote_decode"] != false || dkv["remote_engine_id"] != "e1" || dec["stream"] != true {
		t.Fatalf("prefill %v decode %v", pre, dec)
	}
	if outcomes, _ := r.recorded(); !slices.Equal(outcomes, []string{RelayBoth}) || string(r.sent) != string(r.sentTo) {
		t.Fatalf("outcomes %v sent %s", outcomes, r.sent)
	}
	for _, c := range []struct {
		name    string
		answer  string
		err     error
		wantErr bool
	}{
		{"prefill refused", "", errors.New("503"), true},
		{"no connector parameters", `{"choices":[]}`, nil, true},
		{"not json", `nope`, nil, true},
	} {
		r := &fakeRelay{body: body, prefill: c.answer, wholeErr: c.err}
		if _, _, err := h.Relay(context.Background(), r, prefill, decode); !errors.Is(err, errPrefill) {
			t.Fatalf("%s: %v", c.name, err)
		}
		if outcomes, _ := r.recorded(); !slices.Equal(outcomes, []string{RelayNoPrefill}) || r.sentTo != nil {
			t.Fatalf("%s: outcomes %v decode got %s", c.name, outcomes, r.sentTo)
		}
	}
}

// The bootstrap handoff sends both seats one room beside each other and refuses a prefill seat without its port
func TestBootstrapHandoff(t *testing.T) {
	decode := []datalayer.Endpoint{seatAt("d1", "n1", 2, nil)}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	h, _ := NewHandoff(HandoffSGLangBootstrap)
	r := &fakeRelay{body: body, besideCh: make(chan []byte, 1)}
	resp, ep, err := h.Relay(context.Background(), r, []datalayer.Endpoint{seatAt("p1", "n1", 1, map[string]string{LabelBootstrapPort: "8998"})}, decode)
	if err != nil || ep.ID() != "d1" {
		t.Fatalf("relay %v %v", ep, err)
	}
	resp.Body.Close()
	var pre, dec map[string]any
	json.Unmarshal(<-r.besideCh, &pre)
	json.Unmarshal(r.sentTo, &dec)
	if pre["bootstrap_room"] != dec["bootstrap_room"] || pre["bootstrap_port"] != float64(8998) || dec["bootstrap_host"] != "10.0.0.1" || pre["stream"] != false || pre["max_tokens"] != float64(1) || dec["max_tokens"] != nil {
		t.Fatalf("prefill %v decode %v", pre, dec)
	}
	if outcomes, _ := r.recorded(); !slices.Equal(outcomes, []string{RelayBoth}) {
		t.Fatalf("outcomes %v", outcomes)
	}
	unlabeled := &fakeRelay{body: body, besideCh: make(chan []byte, 1)}
	if _, _, err := h.Relay(context.Background(), unlabeled, []datalayer.Endpoint{seatAt("p1", "n1", 1, nil)}, decode); !errors.Is(err, errPrefill) || unlabeled.sentTo != nil {
		t.Fatalf("a prefill seat with no bootstrap port %v, decode got %s", err, unlabeled.sentTo)
	}
	failing := &fakeRelay{body: body, besideCh: make(chan []byte, 1), wholeErr: errors.New("prefill down")}
	if _, _, err := h.Relay(context.Background(), failing, []datalayer.Endpoint{seatAt("p1", "n1", 1, map[string]string{LabelBootstrapPort: "8998"})}, decode); !errors.Is(err, errPrefill) {
		t.Fatalf("a failed prefill beside decode %v", err)
	}
}

// The slot handoff answers from decode and records the warm's outcome once the answer closes
func TestSlotHandoff(t *testing.T) {
	prefill := []datalayer.Endpoint{seatAt("p1", "n1", 1, nil)}
	decode := []datalayer.Endpoint{seatAt("d1", "n2", 2, nil)}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	cases := []struct {
		name    string
		meshed  bool
		postErr map[string]error
		want    string
		posts   int
	}{
		{"warmed", true, nil, RelayWarm, 3},
		{"restore failed", true, map[string]error{"/slots/0?action=restore": errors.New("no slot")}, RelayWarmFailed, 3},
		{"save failed", true, map[string]error{"/slots/0?action=save": errors.New("disk full")}, RelayWarmFailed, 2},
		{"no mesh", false, nil, RelayWarmFailed, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _ := NewHandoff(HandoffLlamaSlot)
			r := &fakeRelay{body: body, postErr: c.postErr}
			if c.meshed {
				r.files = r
			}
			resp, ep, err := h.Relay(context.Background(), r, prefill, decode)
			if err != nil || ep.ID() != "d1" || string(r.sentTo) != string(body) {
				t.Fatalf("relay %v %v decode got %s", ep, err, r.sentTo)
			}
			if outcomes, _ := r.recorded(); !slices.Equal(outcomes, []string{RelayWarmPending}) {
				t.Fatalf("outcomes before close %v", outcomes)
			}
			resp.Body.Close()
			outcomes, posts := r.recorded()
			if !slices.Equal(outcomes, []string{RelayWarmPending, c.want}) || len(posts) != c.posts {
				t.Fatalf("outcomes %v posts %v", outcomes, posts)
			}
			if c.want == RelayWarm && (len(r.moves) != 1 || r.moves[0] != "f1:n1>n2:relay-t1.bin" || len(r.drops) != 2) {
				t.Fatalf("moves %v drops %v", r.moves, r.drops)
			}
			if c.want == RelayWarmFailed && len(r.warns) != 1 {
				t.Fatalf("warnings %v", r.warns)
			}
		})
	}
	t.Run("request ended first", func(t *testing.T) {
		h, _ := NewHandoff(HandoffLlamaSlot)
		r := &fakeRelay{body: body, hold: make(chan struct{})}
		r.files = r
		ctx, cancel := context.WithCancel(context.Background())
		resp, _, err := h.Relay(ctx, r, prefill, decode)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		resp.Body.Close()
		if outcomes, _ := r.recorded(); !slices.Equal(outcomes, []string{RelayWarmPending}) {
			t.Fatalf("outcomes once the request ended %v", outcomes)
		}
		close(r.hold)
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, posts := r.recorded()
			if len(posts) == 3 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the warm stopped with the request, posts %v", posts)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	t.Run("one warm per slot", func(t *testing.T) {
		h, _ := NewHandoff(HandoffLlamaSlot)
		busy := &fakeRelay{body: body, hold: make(chan struct{})}
		busy.files = busy
		first, _, _ := h.Relay(context.Background(), busy, prefill, decode)
		deadline := time.Now().Add(5 * time.Second)
		for _, posts := busy.recorded(); len(posts) < 2; _, posts = busy.recorded() {
			if time.Now().After(deadline) {
				t.Fatalf("the first warm never saved its slot, posts %v", posts)
			}
			time.Sleep(10 * time.Millisecond)
		}
		second := &fakeRelay{body: body}
		second.files = second
		resp, _, _ := h.Relay(context.Background(), second, prefill, decode)
		resp.Body.Close()
		if outcomes, _ := second.recorded(); !slices.Equal(outcomes, []string{RelayWarmPending, RelayWarmFailed}) {
			t.Fatalf("a second warm on a one slot seat %v", outcomes)
		}
		close(busy.hold)
		first.Body.Close()
		if outcomes, _ := busy.recorded(); !slices.Equal(outcomes, []string{RelayWarmPending, RelayWarm}) {
			t.Fatalf("the first warm %v", outcomes)
		}
	})
}
