package gateway

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/stub"
)

// Sends n chat requests naming model over a few connections at once and counts the served endpoint header of each
func (h *harness) spread(model string, n int) map[string]int {
	h.t.Helper()
	body := strings.Replace(chatBody, `"model":"chat"`, `"model":"`+model+`"`, 1)
	var (
		mu     sync.Mutex
		counts = map[string]int{}
		fails  []string
		wg     sync.WaitGroup
	)
	next := make(chan struct{})
	for range 8 {
		wg.Go(func() {
			for range next {
				req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/chat/completions", strings.NewReader(body))
				if err != nil {
					panic(err)
				}
				req.Header.Set("Content-Type", "application/json")
				resp, err := h.srv.Client().Do(req)
				mu.Lock()
				switch {
				case err != nil:
					fails = append(fails, err.Error())
				case resp.StatusCode != http.StatusOK:
					fails = append(fails, fmt.Sprintf("status %d from %q", resp.StatusCode, resp.Header.Get(requestcontrol.HeaderServed)))
				default:
					counts[resp.Header.Get(requestcontrol.HeaderServed)]++
				}
				mu.Unlock()
				if err == nil {
					resp.Body.Close()
				}
			}
		})
	}
	for range n {
		next <- struct{}{}
	}
	close(next)
	wg.Wait()
	if len(fails) > 0 {
		h.t.Fatalf("%d of %d requests for %s failed, first %s", len(fails), n, model, fails[0])
	}
	return counts
}

// One pool holds the old instance served as foo and the new as foo-v2, a slot split route steering between
func TestCanarySplitInOnePool(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, oldSrv := h.stub("foo")
	_, newSrv := h.stub("foo-v2")
	mem := pools.Membership{Model: key("base"), Runtime: "vllm", Protocol: datalayer.AppProtocolHTTP, Concurrency: 8, Metrics: &v1.MetricNames{HealthPath: "/health"}}
	pool := pools.PoolIDOf(key("base"), "vllm", "")
	slot := pools.OwnerRef{Kind: pools.OwnerSlot, ID: "s1"}
	if err := h.mgr.Publish(ctx, slot, mem, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.NameRoute(ctx, slot, "foo", pools.RouteSpec{ServedName: "foo"}); err != nil {
		t.Fatal(err)
	}
	for served, srv := range map[string]string{"foo": oldSrv.URL, "foo-v2": newSrv.URL} {
		addr, err := datalayer.ParseAddress(strings.TrimPrefix(srv, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		owner := pools.OwnerRef{Kind: pools.OwnerInstance, ID: served}
		if err := h.mgr.Publish(ctx, owner, mem, []pools.Published{{InstanceID: served, NodeID: testNode, Address: addr, Ready: true, Labels: map[string]string{instances.LabelServed: served}}}); err != nil {
			t.Fatal(err)
		}
	}
	if eps := h.store.Endpoints(pool, nil); len(eps) != 2 {
		t.Fatalf("the pool holds %d endpoints, want both instances", len(eps))
	}
	oldHost, newHost := hostOf(oldSrv), hostOf(newSrv)
	cases := []struct {
		name     string
		old, new uint32
		n        int
		share    float64
		slack    float64
	}{
		{"ninety ten", 90, 10, 1000, 0.10, 0.04},
		{"all to v2", 0, 100, 200, 1, 0},
		{"all to old", 100, 0, 200, 0, 0},
		{"half", 50, 50, 1000, 0.5, 0.06},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := h.mgr.SplitRoute(ctx, slot, "foo", []pools.WeightedTarget{{Pool: pool, Model: "foo", Weight: c.old}, {Pool: pool, Model: "foo-v2", Weight: c.new}}); err != nil {
				t.Fatal(err)
			}
			before := map[string]pools.Outcome{"foo": h.g.Outcome(pool, "foo"), "foo-v2": h.g.Outcome(pool, "foo-v2")}
			counts := h.spread("foo", c.n)
			if counts[oldHost]+counts[newHost] != c.n {
				t.Fatalf("served by %v, want only %s and %s", counts, oldHost, newHost)
			}
			if share := float64(counts[newHost]) / float64(c.n); math.Abs(share-c.share) > c.slack {
				t.Fatalf("v2 took %.3f of %d requests, want %.2f within %.2f", share, c.n, c.share, c.slack)
			}
			waitFor(t, "every request counted on its target", func() bool {
				return h.g.Outcome(pool, "foo").Since(before["foo"]).Requests+h.g.Outcome(pool, "foo-v2").Since(before["foo-v2"]).Requests == uint64(c.n)
			})
			for served, host := range map[string]string{"foo": oldHost, "foo-v2": newHost} {
				if o := h.g.Outcome(pool, served).Since(before[served]); o.Requests != uint64(counts[host]) || o.Errors != 0 {
					t.Fatalf("target %s counted %+v, its endpoint answered %d", served, o, counts[host])
				}
			}
		})
	}
	if err := h.mgr.SplitRoute(ctx, slot, "foo", []pools.WeightedTarget{{Pool: pool, Model: "foo", Weight: 90}, {Pool: pool, Model: "foo-v2", Weight: 10}}); err != nil {
		t.Fatal(err)
	}
	if got, adapter, err := h.mgr.Lookup(ctx, "foo-v2"); err != nil || got.ID != pool || adapter != "" {
		t.Fatalf("Lookup(foo-v2) = %s %q %v, want the shared pool", got.ID, adapter, err)
	}
	if counts := h.spread("foo-v2", 50); counts[newHost] != 50 {
		t.Fatalf("the canary's own name was served by %v", counts)
	}
}

// A user route splits one public name over two adapters of one instance by weight, each request reaching its adapter
func TestAdapterSplit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st, err := stub.New(stub.Config{ID: "ab", Model: "base", Adapters: []stub.Adapter{{Name: "lora-a", Path: "/adapters/a"}, {Name: "lora-b", Path: "/adapters/b"}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(st)
	t.Cleanup(func() {
		srv.Close()
		st.Close()
	})
	addr, err := datalayer.ParseAddress(hostOf(srv))
	if err != nil {
		t.Fatal(err)
	}
	mem := pools.Membership{Model: key("ab"), Runtime: "vllm", Protocol: datalayer.AppProtocolHTTP, Concurrency: 8, Metrics: &v1.MetricNames{HealthPath: "/health"}}
	labels := map[string]string{instances.LabelServed: "base"}
	eps := []pools.Published{{InstanceID: "i", NodeID: testNode, Address: addr, Ready: true, Labels: labels}}
	for _, a := range []string{"lora-a", "lora-b"} {
		eps = append(eps, pools.Published{InstanceID: "i-" + a, NodeID: testNode, Address: addr, Ready: true, Labels: labels, Adapter: a})
	}
	if err := h.mgr.Publish(ctx, pools.OwnerRef{Kind: pools.OwnerInstance, ID: "i"}, mem, eps); err != nil {
		t.Fatal(err)
	}
	pool := string(pools.PoolIDOf(key("ab"), "vllm", ""))
	spec := &v1.RouteSpec{Rules: []*v1.RewriteRule{{Match: "pub", Targets: []*v1.RewriteTarget{{PoolId: pool, Model: "lora-a", Weight: 70}, {PoolId: pool, Model: "lora-b", Weight: 30}}}}}
	if _, err := h.mgr.PutRoute(ctx, "ab", spec); err != nil {
		t.Fatal(err)
	}
	const n = 1000
	if counts := h.spread("pub", n); counts[hostOf(srv)] != n {
		t.Fatalf("served by %v", counts)
	}
	byModel := map[string]int{}
	for _, r := range st.Stats().Records {
		byModel[r.Model]++
	}
	if byModel["lora-a"]+byModel["lora-b"] != n {
		t.Fatalf("the stub served %v, want every request on an adapter", byModel)
	}
	if share := float64(byModel["lora-b"]) / n; math.Abs(share-0.3) > 0.05 {
		t.Fatalf("lora-b took %.3f, want 0.30", share)
	}
}

// Requests keep to endpoints labelled with their target, the pool's model too once an endpoint carries it as a label
func TestServingConfinesToTarget(t *testing.T) {
	store := datalayer.NewStore()
	store.PutPool(datalayer.Pool{ID: "p", Model: "base"})
	store.PutPool(datalayer.Pool{ID: "bare", Model: "base"})
	store.PutPool(datalayer.Pool{ID: "named", Model: "base"})
	put := func(id string, pool datalayer.PoolID, port int, labels map[string]string) {
		store.UpsertEndpoint(datalayer.EndpointSpec{ID: datalayer.EndpointID(id), Pool: pool, Address: datalayer.Address{Host: "10.0.0.1", Port: port}, Labels: labels, Ready: true})
	}
	put("base", "p", 1, map[string]string{instances.LabelServed: "base"})
	put("v2", "p", 2, map[string]string{instances.LabelServed: "base-v2"})
	put("v2-lora", "p", 2, map[string]string{instances.LabelServed: "base-v2", datalayer.LabelAdapter: "lora"})
	put("plain", "bare", 3, nil)
	put("plain-2", "bare", 4, nil)
	put("foo", "named", 5, map[string]string{instances.LabelServed: "foo"})
	put("foo-v2", "named", 6, map[string]string{instances.LabelServed: "foo-v2"})
	g := &Gateway{store: store}
	cases := []struct {
		name    string
		pool    datalayer.PoolID
		target  string
		adapter string
		want    []string
	}{
		{"old served name", "p", "base", "", []string{"base"}},
		{"new served name", "p", "base-v2", "", []string{"v2", "v2-lora"}},
		{"adapter", "p", "lora", "lora", []string{"v2-lora"}},
		{"unserved name", "p", "other", "", nil},
		{"pool model with no label naming it", "bare", "base", "", []string{"plain", "plain-2"}},
		{"unlabelled pool takes any target", "bare", "anything", "", []string{"plain", "plain-2"}},
		{"pool model no label names", "named", "base", "", []string{"foo", "foo-v2"}},
		{"one of two served names", "named", "foo-v2", "", []string{"foo-v2"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := g.serving(&framework.Request{TargetModel: c.target, Adapter: c.adapter}, store.Endpoints(c.pool, nil))
			ids := make([]string, len(got))
			for i, ep := range got {
				ids[i] = string(ep.ID())
			}
			slices.Sort(ids)
			if !slices.Equal(ids, c.want) {
				t.Fatalf("serving %v, want %v", ids, c.want)
			}
		})
	}
	store.PutPool(datalayer.Pool{ID: "p", Model: "base-v2"})
	got := g.serving(&framework.Request{TargetModel: "base-v2"}, store.Endpoints("p", nil))
	if len(got) != 2 || slices.ContainsFunc(got, func(ep datalayer.Endpoint) bool { return ep.ID() == "base" }) {
		t.Fatalf("the pool's model reached %v, want only the endpoints labelled with it", got)
	}
}
