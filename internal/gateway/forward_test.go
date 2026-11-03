package gateway

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/stub"
)

// Gateways in a line, each forwarding the model's route to the next member, the last serving it from a stub
func forwardChain(t *testing.T, length int) (*harness, *stub.Stub) {
	t.Helper()
	last := newHarness(t)
	st, srv := last.stub("stub-model")
	last.publish("stub-model", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model"}, srv.URL)
	entry := last
	for i := length - 1; i >= 1; i-- {
		next := entry
		entry = newHarness(t, func(s *setup) { s.peers = &fakePeers{base: next.srv.URL, client: next.srv.Client()} })
		entry.publish("stub-model", "stub", "vllm", datalayer.AppProtocolHTTP, true, pools.RouteSpec{ServedName: "stub-model", NodeID: fmt.Sprintf("n%d", i+1)}, "127.0.0.1:1")
	}
	return entry, st
}

// Forwards a route hop by hop, serving under the hop limit and refusing with the loop status at it
func TestForwardAcrossGateways(t *testing.T) {
	t.Run("two forwards under the limit reach the serving gateway", func(t *testing.T) {
		entry, st := forwardChain(t, mesh.MaxHops)
		r := entry.do(http.MethodPost, "/v1/chat/completions", modelBody("stub-model"))
		if r.status != http.StatusOK {
			t.Fatalf("status %d after two forwards: %s", r.status, r.body)
		}
		if served := st.Stats().Served; served != 1 {
			t.Fatalf("the stub served %d requests", served)
		}
		tr := entry.trace(r)
		if !tr.GetForwarded() || tr.GetNodeId() != "n2" {
			t.Fatalf("the entry trace names the first forward: forwarded %v node %q", tr.GetForwarded(), tr.GetNodeId())
		}
	})
	t.Run("a forward reaching the limit is refused with the loop status", func(t *testing.T) {
		entry, st := forwardChain(t, mesh.MaxHops+1)
		r := entry.do(http.MethodPost, "/v1/chat/completions", modelBody("stub-model"))
		if r.status != statusLoop {
			t.Fatalf("status %d past the hop limit: %s", r.status, r.body)
		}
		if served := st.Stats().Served; served != 0 {
			t.Fatalf("the stub served %d requests past the hop limit", served)
		}
	})
}
