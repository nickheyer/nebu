package assumed_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/internal/schedtest"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/assumed"
)

var ctx = context.Background()

func stored(t *testing.T, s *schedtest.Store, id datalayer.EndpointID) int {
	t.Helper()
	v, ok := s.Get(id, datalayer.CollectorAssumed)
	if !ok {
		return -1
	}
	n, ok := v.(int)
	if !ok {
		t.Fatalf("attribute of %s is %T, want int", id, v)
	}
	return n
}

func TestAssumedLoadCounts(t *testing.T) {
	d1, d2, p1 := schedtest.EP("d1"), schedtest.EP("d2"), schedtest.EP("p1")
	pd := &framework.Result{
		PrimaryProfile: "decode",
		ProfileResults: map[string]*framework.ProfileRunResult{
			"decode":  {Endpoints: schedtest.Eps(d1, d2)},
			"prefill": {Endpoints: schedtest.Eps(p1)},
		},
	}
	tests := []struct {
		name string
		res  *framework.Result
		want map[datalayer.EndpointID]int
	}{
		{"primary only, fallbacks untouched", schedtest.Result("p", d1, d2), map[datalayer.EndpointID]int{"d1": 1, "d2": -1, "p1": -1}},
		{"every routed profile head", pd, map[datalayer.EndpointID]int{"d1": 1, "d2": -1, "p1": 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := schedtest.NewStore()
			a := assumed.New(store)
			req := &framework.Request{ID: "r"}
			a.PreRequest(ctx, req, tc.res)
			for id, want := range tc.want {
				if got := stored(t, store, id); got != want {
					t.Fatalf("%s stored %d, want %d", id, got, want)
				}
			}
			a.ResponseComplete(ctx, req, &framework.Response{Endpoint: d2})
			for id, want := range tc.want {
				if want < 0 {
					continue
				}
				if got := stored(t, store, id); got != 0 || a.Count(id) != 0 {
					t.Fatalf("%s stored %d counted %d after completion", id, got, a.Count(id))
				}
			}
			a.ResponseComplete(ctx, req, &framework.Response{Endpoint: d1})
			if a.Count("d1") != 0 {
				t.Fatalf("second completion released again: %d", a.Count("d1"))
			}
		})
	}
}

func TestAssumedLoadConcurrent(t *testing.T) {
	store := schedtest.NewStore()
	a := assumed.New(store)
	ep := schedtest.EP("e")
	const workers, each = 32, 100
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				a.PreRequest(ctx, &framework.Request{ID: fmt.Sprintf("%d-%d", w, i)}, schedtest.Result("p", ep))
			}
		}()
	}
	wg.Wait()
	if got := stored(t, store, "e"); got != workers*each || a.Count("e") != workers*each {
		t.Fatalf("stored %d counted %d, want %d", got, a.Count("e"), workers*each)
	}
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				a.ResponseComplete(ctx, &framework.Request{ID: fmt.Sprintf("%d-%d", w, i)}, &framework.Response{Endpoint: ep})
			}
		}()
	}
	wg.Wait()
	if got := stored(t, store, "e"); got != 0 || a.Count("e") != 0 {
		t.Fatalf("stored %d counted %d after every completion", got, a.Count("e"))
	}
}

func TestReserveAgainstCapacity(t *testing.T) {
	store := schedtest.NewStore()
	a := assumed.New(store)
	state := framework.NewCycleState()
	req := func(id string, prompt, out int) *framework.Request {
		return &framework.Request{ID: id, PromptTokens: prompt, ExpectedOutputTokens: out}
	}
	seat := schedtest.EP("seat").With(datalayer.CollectorCapacity, 2)
	kv := schedtest.EP("kv").Metrics(datalayer.ServerMetrics{NumGPUBlocks: 10, BlockSize: 16}).With(datalayer.CollectorCapacity, 9)
	labeled := schedtest.EP("labeled").Label(datalayer.LabelKVCapacity, "100")
	tests := []struct {
		name string
		ep   *schedtest.Endpoint
		reqs []*framework.Request
		want []bool
	}{
		{"request capacity", seat, []*framework.Request{req("seat-a", 1, 1), req("seat-b", 1, 1), req("seat-c", 1, 1)}, []bool{true, true, false}},
		{"kv tokens from blocks, an empty endpoint always fits", kv, []*framework.Request{req("kv-a", 200, 100), req("kv-b", 1, 1)}, []bool{true, false}},
		{"kv tokens from the label", labeled, []*framework.Request{req("lab-a", 40, 20), req("lab-b", 30, 10), req("lab-c", 1, 1)}, []bool{true, true, false}},
		{"no capacity known", schedtest.EP("free"), []*framework.Request{req("free-a", 1000, 1000), req("free-b", 1000, 1000)}, []bool{true, true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for i, r := range tc.reqs {
				if got := a.Reserve(ctx, state, r, tc.ep); got != tc.want[i] {
					t.Fatalf("request %d reserved %v, want %v", i, got, tc.want[i])
				}
			}
		})
	}
	if a.Count("kv") != 1 || a.Tokens("kv") != 300 || stored(t, store, "kv") != 1 {
		t.Fatalf("kv counted %d requests %d tokens stored %d", a.Count("kv"), a.Tokens("kv"), stored(t, store, "kv"))
	}
	if v, _ := store.Get("labeled", datalayer.CollectorAssumedTokens); v != 100 {
		t.Fatalf("labeled stored tokens %v", v)
	}

	t.Run("pre request does not count a reserved head twice and completion releases all", func(t *testing.T) {
		r := req("seat-a", 1, 1)
		a.PreRequest(ctx, r, schedtest.Result("p", seat))
		if a.Count("seat") != 2 {
			t.Fatalf("seat counted %d after pre request of a reserved head", a.Count("seat"))
		}
		a.ResponseComplete(ctx, r, &framework.Response{Endpoint: seat})
		if a.Count("seat") != 1 || a.Tokens("seat") != 2 {
			t.Fatalf("seat %d requests %d tokens after one completion", a.Count("seat"), a.Tokens("seat"))
		}
	})

	t.Run("release gives back one hold", func(t *testing.T) {
		a.Release(ctx, state, req("lab-b", 30, 10), labeled)
		if a.Count("labeled") != 1 || a.Tokens("labeled") != 60 {
			t.Fatalf("labeled %d requests %d tokens after release", a.Count("labeled"), a.Tokens("labeled"))
		}
		if !a.Reserve(ctx, state, req("lab-x", 5, 5), labeled) {
			t.Fatal("labeled has room once a hold is released")
		}
		a.Release(ctx, state, req("lab-x", 5, 5), labeled)
		a.Release(ctx, state, req("never", 5, 5), labeled)
		if a.Count("labeled") != 1 || a.Tokens("labeled") != 60 {
			t.Fatalf("labeled %d requests %d tokens after releasing the new hold", a.Count("labeled"), a.Tokens("labeled"))
		}
	})
}

func TestFallbackMovesTheCount(t *testing.T) {
	store := schedtest.NewStore()
	a := assumed.New(store)
	picked, served, prefill := schedtest.EP("picked"), schedtest.EP("served"), schedtest.EP("prefill")
	req := &framework.Request{ID: "r", PromptTokens: 10, ExpectedOutputTokens: 10}
	res := &framework.Result{PrimaryProfile: "decode", ProfileResults: map[string]*framework.ProfileRunResult{
		"decode":  {Endpoints: schedtest.Eps(picked, served)},
		"prefill": {Endpoints: schedtest.Eps(prefill)},
	}}
	a.PreRequest(ctx, req, res)
	a.ResponseReceived(ctx, req, &framework.Response{Endpoint: served})
	if a.Count("picked") != 0 || a.Count("served") != 1 || a.Tokens("served") != 20 || a.Count("prefill") != 1 {
		t.Fatalf("picked %d served %d tokens %d prefill %d", a.Count("picked"), a.Count("served"), a.Tokens("served"), a.Count("prefill"))
	}
	a.ResponseReceived(ctx, req, &framework.Response{Endpoint: served})
	a.ResponseComplete(ctx, req, &framework.Response{Endpoint: served})
	for _, id := range []datalayer.EndpointID{"picked", "served", "prefill"} {
		if a.Count(id) != 0 || a.Tokens(id) != 0 {
			t.Fatalf("%s holds %d requests %d tokens after completion", id, a.Count(id), a.Tokens(id))
		}
	}
}

func TestPluginsOverOneStoreShareCounts(t *testing.T) {
	store := schedtest.NewStore()
	a, b := assumed.New(store), assumed.New(store)
	ep := schedtest.EP("e").With(datalayer.CollectorCapacity, 1)
	if !a.Reserve(ctx, framework.NewCycleState(), &framework.Request{ID: "1"}, ep) {
		t.Fatal("first reservation refused")
	}
	if b.Reserve(ctx, framework.NewCycleState(), &framework.Request{ID: "2"}, ep) {
		t.Fatal("second plugin over the same store reserved past the capacity")
	}
	b.ResponseComplete(ctx, &framework.Request{ID: "1"}, nil)
	if a.Count("e") != 0 {
		t.Fatalf("completion through the other plugin left %d", a.Count("e"))
	}
	if assumed.New(schedtest.NewStore()).Count("e") != 0 {
		t.Fatal("another store shares counts")
	}
}
