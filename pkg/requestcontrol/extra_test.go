package requestcontrol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

func TestSchedulerErrorsBeyondFailureMode(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		cancel bool
		want   error
	}{
		{"cancelled while scheduling", errors.New("aborted"), true, context.Canceled},
		{"scheduler sheds", fmt.Errorf("policy: %w", ErrShed), false, ErrShed},
		{"scheduler finds nothing ready", fmt.Errorf("probe: %w", ErrNoReadyEndpoints), false, ErrNoReadyEndpoints},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.store.PutPool(datalayer.Pool{ID: "p1", Model: "llama", FailureMode: datalayer.FailOpen})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.sched.fn = func(context.Context, *framework.Request, []datalayer.Endpoint) (*framework.Result, error) {
				if c.cancel {
					cancel()
				}
				return nil, c.err
			}
			dec, err := e.dir.Decide(ctx, inbound(t, chat("llama")))
			if !errors.Is(err, c.want) || dec != nil {
				t.Fatalf("err %v, want %v without failing open", err, c.want)
			}
			e.settled(t)
		})
	}
}

// A document whose model read fails with an error other than a missing model
type brokenDocument struct{ bodyparse.Document }

func (brokenDocument) Model() (string, error) { return "", errors.New("read failed") }

func TestBodyModelReadError(t *testing.T) {
	e := newEnv(t)
	in := inbound(t, chat("llama"))
	in.Body = brokenDocument{in.Body}
	if _, err := e.dir.Decide(context.Background(), in); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("err %v, want the read failure", err)
	}
	if e.flow.admitted.Load() != 0 {
		t.Fatal("admitted after a body read failure")
	}
}

func TestPluginClearingTargetModel(t *testing.T) {
	wipe := &prePlugin{name: "wipe", fn: func(_ context.Context, _ *Inbound, r *framework.Request) error {
		r.TargetModel = ""
		return nil
	}}
	e := newEnv(t, withTopology(t, Registration{Plugin: wipe}))
	if _, err := e.dir.Decide(context.Background(), inbound(t, chat("llama"))); !errors.Is(err, bodyparse.ErrNoModel) {
		t.Fatalf("err %v, want a refused empty model", err)
	}
	e.settled(t)
}

// A lifecycle plugin whose every hook panics
type panicHook struct{}

func (panicHook) TypedName() framework.TypedName { return framework.TypedName{Type: "panic"} }
func (panicHook) PreRequest(context.Context, *framework.Request, *framework.Result) {
	panic("pre")
}
func (panicHook) ResponseComplete(context.Context, *framework.Request, *framework.Response) {
	panic("complete")
}

func TestPanickingHooksStillComplete(t *testing.T) {
	rec := &recorder{}
	var logs bytes.Buffer
	e := newEnv(t,
		withTopology(t, Registration{Plugin: &donePlugin{name: "d1", rec: rec}}, Registration{Plugin: &donePlugin{name: "d2", rec: rec}}),
		func(d *Deps) {
			d.Lifecycle = []framework.Plugin{panicHook{}, &lifeHook{name: "a", rec: rec}}
			d.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		})
	dec, err := e.dir.Decide(context.Background(), inbound(t, chat("llama")))
	if err != nil {
		t.Fatal(err)
	}
	e.dir.ResponseComplete(context.Background(), dec, nil)
	for _, ev := range []string{"a.pre", "a.complete", "d1.done", "d2.done"} {
		if rec.count(ev) != 1 {
			t.Fatalf("%s ran %d times in %v", ev, rec.count(ev), rec.list())
		}
	}
	if n := strings.Count(logs.String(), "panicked"); n != 2 {
		t.Fatalf("logged %d panics, want 2 in %s", n, logs.String())
	}
	e.settled(t)
}

func TestIsolateAndMerge(t *testing.T) {
	type inner struct {
		A int
		M map[string]int
	}
	type opaque struct{ at time.Time }
	type rec struct {
		P     *inner
		N     int
		S     []int
		M     map[string]string
		In    inner
		Op    opaque
		Empty map[string]string
	}
	at := time.Unix(10, 0)
	live := &rec{P: &inner{A: 3, M: map[string]int{"p": 1}}, N: 1, S: []int{1, 2}, M: map[string]string{"k": "v", "gone": "x"}, In: inner{A: 1, M: map[string]int{"a": 1}}}
	base := isolate(live)
	one, two := isolate(live), isolate(live)
	one.S[0] = 99
	one.In.A = 5
	one.M["new"] = "n"
	delete(one.M, "gone")
	one.Op = opaque{at: at}
	two.N = 7
	two.P.A = 4
	two.P.M["q"] = 2
	two.In.M["b"] = 2
	two.Empty = map[string]string{"z": "z"}
	if live.S[0] != 1 || live.M["gone"] != "x" || live.In.M["b"] != 0 || live.P.A != 3 || live.P.M["q"] != 0 {
		t.Fatal("an isolated copy shares storage with the source")
	}
	merge(live, base, one)
	merge(live, base, two)
	if live.P.A != 4 || live.P.M["q"] != 2 || live.N != 7 || live.S[0] != 99 || live.In.A != 5 || live.In.M["b"] != 2 || live.In.M["a"] != 1 {
		t.Fatalf("merged %+v", live)
	}
	if live.M["new"] != "n" || live.M["k"] != "v" || live.M["gone"] != "" || live.Op.at != at || live.Empty["z"] != "z" {
		t.Fatalf("merged %+v", live)
	}
}

func TestMergeHeader(t *testing.T) {
	live := http.Header{"A": {"1"}, "B": {"2"}, "C": {"3"}}
	base := live.Clone()
	got := live.Clone()
	got.Set("A", "changed")
	got.Del("B")
	got.Add("D", "4")
	mergeHeader(live, base, got)
	if live.Get("A") != "changed" || live.Get("B") != "" || live.Get("C") != "3" || live.Get("D") != "4" {
		t.Fatalf("merged %v", live)
	}
}

func TestSharedNameCatalogEdges(t *testing.T) {
	table, store := newTable(t)
	store.PutPool(datalayer.Pool{ID: "p4", Model: "mistral"})
	table.Put("p4", []string{"mistral"}, []string{"mistral-lora"})
	store.UpsertEndpoint(endpoint("e2", "p2", "10.0.0.2", false))
	if pool, _, err := table.Lookup(context.Background(), "mistral"); err != nil || pool.ID != "p2" {
		t.Fatalf("with no pool ready Lookup = %s %v, want the first pool by id", pool.ID, err)
	}
	table.Delete("p2")
	if pool, _, err := table.Lookup(context.Background(), "mistral"); err != nil || pool.ID != "p4" {
		t.Fatalf("after removing p2 Lookup = %s %v, want p4", pool.ID, err)
	}
	if _, adapter, err := table.Lookup(context.Background(), "mistral-lora"); err != nil || adapter != "mistral-lora" {
		t.Fatalf("adapter %q %v", adapter, err)
	}
}

func TestTopologyEdges(t *testing.T) {
	a := pre("a")
	if _, err := NewTopology(Registration{Plugin: a, Before: []framework.TypedName{{Type: "pre", Name: "ghost"}}}); err == nil {
		t.Fatal("unknown before dependency accepted")
	}
	topo, err := NewTopology(Registration{Plugin: a})
	if err != nil {
		t.Fatal(err)
	}
	if topo.Batches(Stage(-1)) != nil || topo.Batches(Stage(stageCount)) != nil {
		t.Fatal("an unknown stage has batches")
	}
}

func TestStatusOfBodyErrors(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
	}{
		{bodyparse.ErrTooLarge, http.StatusRequestEntityTooLarge},
		{bodyparse.ErrNotJSON, http.StatusBadRequest},
		{bodyparse.ErrNoModel, http.StatusBadRequest},
	} {
		if code, _ := Status(fmt.Errorf("wrapped: %w", c.err)); code != c.code {
			t.Fatalf("Status(%v) = %d, want %d", c.err, code, c.code)
		}
	}
}
