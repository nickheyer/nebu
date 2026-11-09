package gateway

import (
	"context"
	"encoding/json"
	"errors"
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

// The disaggregated test configuration with its pd handler naming a handoff, none when empty
func pdNaming(handoff string) string {
	if handoff == "" {
		return pdTest
	}
	return strings.Replace(pdTest, "    threshold: 0\n", "    threshold: 0\n    handoff: "+handoff+"\n", 1)
}

// Hosts a configuration and publishes a prefill and a decode seat of the runtime under it, naming the chat route
func relaySeats(t *testing.T, h *harness, runtime, config, handoff string, prefill, decode string, prefillLabels map[string]string) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.mgr.PutSchedulerConfig(ctx, &v1.SchedulerConfig{Name: config, Spec: &v1.SchedulerConfigSpec{Yaml: pdNaming(handoff)}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "configuration hosted", func() bool { _, ok := h.g.hosting.get(config); return ok })
	owner := pools.OwnerRef{Kind: pools.OwnerFormation, ID: "f1"}
	mem := pools.Membership{Model: key("pd"), Runtime: runtime, Protocol: datalayer.AppProtocolHTTP, Concurrency: 2, SchedulerConfig: config, Metrics: &v1.MetricNames{HealthPath: "/health"}}
	pa, _ := datalayer.ParseAddress(prefill)
	da, _ := datalayer.ParseAddress(decode)
	if err := h.mgr.Publish(ctx, owner, mem, []pools.Published{
		{InstanceID: "p1", NodeID: testNode, Address: pa, Role: datalayer.RolePrefill, Ready: true, Labels: prefillLabels},
		{InstanceID: "d1", NodeID: testNode, Address: da, Role: datalayer.RoleDecode, Ready: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.NameRoute(ctx, owner, "chat", pools.RouteSpec{ServedName: "m"}); err != nil {
		t.Fatal(err)
	}
}

// A prefill seat answering with NIXL connector parameters, recording the bodies it got
type nixlSeat struct {
	mu     sync.Mutex
	bodies []map[string]any
	srv    *httptest.Server
}

func newNIXLSeat(t *testing.T) *nixlSeat {
	s := &nixlSeat{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"p"},"finish_reason":"length"}],"kv_transfer_params":{"remote_engine_id":"e9","remote_block_ids":[3]},"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// A profile's handoff drives the relay for a runtime declaring none, one disagreeing with the runtime is refused
func TestProfileSelectsHandoff(t *testing.T) {
	t.Run("profile names nixl", func(t *testing.T) {
		h := newHarness(t, func(s *setup) { s.runtimes.api["plain"] = v1.ApiFlavor_API_FLAVOR_OPENAI })
		prefill, decode := newNIXLSeat(t), newSeat(t)
		relaySeats(t, h, "plain", "pd-nixl", requestcontrol.HandoffNIXL, hostOf(prefill.srv), hostOf(decode.srv), nil)
		r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
		if r.status != http.StatusOK || !strings.Contains(string(r.body), "seated") {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		_, db := decode.got()
		prefill.mu.Lock()
		pb := prefill.bodies
		prefill.mu.Unlock()
		pkv, _ := pb[0]["kv_transfer_params"].(map[string]any)
		dkv, _ := db[0]["kv_transfer_params"].(map[string]any)
		if len(pb) != 1 || len(db) != 1 || pkv["do_remote_decode"] != true || dkv["do_remote_prefill"] != true || dkv["remote_engine_id"] != "e9" {
			t.Fatalf("prefill got %v, decode got %v", pb, db)
		}
		if tr := traceOf(t, h, r.header.Get(TraceHeader)); tr.GetRelay() != requestcontrol.RelayBoth {
			t.Fatalf("relay %q", tr.GetRelay())
		}
	})
	t.Run("profile names the bootstrap room", func(t *testing.T) {
		h := newHarness(t, func(s *setup) { s.runtimes.api["plain"] = v1.ApiFlavor_API_FLAVOR_OPENAI })
		prefill, decode := newSeat(t), newSeat(t)
		relaySeats(t, h, "plain", "pd-bootstrap", requestcontrol.HandoffSGLangBootstrap, hostOf(prefill.srv), hostOf(decode.srv), map[string]string{requestcontrol.LabelBootstrapPort: "8998"})
		r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
		if r.status != http.StatusOK {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		_, pb := prefill.got()
		_, db := decode.got()
		if len(pb) != 1 || len(db) != 1 || pb[0]["bootstrap_room"] == nil || pb[0]["bootstrap_room"] != db[0]["bootstrap_room"] || db[0]["kv_transfer_params"] != nil {
			t.Fatalf("prefill got %v, decode got %v", pb, db)
		}
	})
	for _, c := range []struct {
		name, declared, named, refusal string
	}{
		{"profile disagrees with the runtime", estimate.HandoffNIXL, requestcontrol.HandoffLlamaSlot, "whose seats hand off over"},
		{"neither names a handoff", "", "", "names a relay handoff"},
		{"runtime declares a handoff no plugin drives", "mooncake", "", "the gateway drives only"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, func(s *setup) {
				s.runtimes.api["declared"] = v1.ApiFlavor_API_FLAVOR_OPENAI
				s.runtimes.handoff["declared"] = c.declared
			})
			prefill, decode := newSeat(t), newSeat(t)
			relaySeats(t, h, "declared", "pd-named", c.named, hostOf(prefill.srv), hostOf(decode.srv), nil)
			r := h.do(http.MethodPost, "/v1/chat/completions", chatBody)
			if r.status != http.StatusServiceUnavailable || !strings.Contains(string(r.body), c.refusal) {
				t.Fatalf("status %d: %s", r.status, r.body)
			}
			if pp, _ := prefill.got(); len(pp) != 0 {
				t.Fatalf("prefill seat got %v", pp)
			}
			if dp, _ := decode.got(); len(dp) != 0 {
				t.Fatalf("decode seat got %v", dp)
			}
		})
	}
	t.Run("an unknown handoff is refused at validation", func(t *testing.T) {
		h := newHarness(t)
		_, err := h.mgr.PutSchedulerConfig(context.Background(), &v1.SchedulerConfig{Name: "pd-unknown", Spec: &v1.SchedulerConfigSpec{Yaml: pdNaming("mooncake")}})
		if !errors.Is(err, pools.ErrInvalid) || !strings.Contains(err.Error(), "mooncake") {
			t.Fatalf("put %v", err)
		}
		if err := h.mgr.ValidateSchedulerConfig(pdNaming("mooncake")); !errors.Is(err, pools.ErrInvalid) {
			t.Fatalf("validate only %v", err)
		}
	})
}
