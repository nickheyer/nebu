package bench

import (
	"context"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/stub"
)

// Fast ratio mode stub settings for local runs
func fastStub() stub.Config {
	return stub.Config{Mode: stub.ModeRatio, Speedup: 50, BaseLatency: 100 * time.Millisecond, DecodePerToken: 50 * time.Millisecond}
}

// A short synthesized trace with adapters, fairness ids and integer objectives
func localTrace(t *testing.T) []Request {
	t.Helper()
	reqs, err := Synthesize(5, Spec{
		Requests: 40, Rate: 400, Model: "bench-model",
		PromptTokens: Dist{Kind: DistUniform, A: 8, B: 64}, OutputTokens: Dist{Kind: DistUniform, A: 2, B: 6},
		PrefixShare: 0.5, Prefixes: 2, PrefixTokens: 16, FairnessIDs: 3, Objectives: []string{"0", "5"},
		Adapters: []string{"lora-a"}, AdapterShare: 0.3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reqs
}

func TestLocalRunsEveryConfigOnFreshStubs(t *testing.T) {
	g := newFakeGateway(t, "default", "default-nometrics", "pd")
	reqs := localTrace(t)
	ctx := context.Background()
	l, err := StartLocal(ctx, g.api, reqs, LocalOptions{Stubs: 3, Stub: fastStub(), Pool: "bench"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Pool() != "bench" || !slices.Equal(l.Routes(), []string{"bench-model"}) || len(l.Addresses()) != 3 {
		t.Fatalf("pool %s routes %v addresses %v", l.Pool(), l.Routes(), l.Addresses())
	}
	pool, err := g.api.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: "bench"}))
	if err != nil {
		t.Fatal(err)
	}
	spec := pool.Msg.GetPool().GetSpec()
	if spec.GetModel() != "stub-model" || spec.GetRuntimeId() != "vllm" || len(spec.GetTargetPorts()) != 3 || len(spec.GetStaticEndpoints()) != 3 {
		t.Fatalf("pool spec %v", spec)
	}
	for _, ep := range pool.Msg.GetPool().GetStatus().GetEndpoints() {
		if !slices.Equal(ep.GetMetrics().GetRunningAdapters(), []string{"lora-a"}) {
			t.Fatalf("endpoint %s runs adapters %v", ep.GetAddress(), ep.GetMetrics().GetRunningAdapters())
		}
	}
	var before []int
	resets := 0
	plan := Plan{Pool: l.Pool(), Configs: []string{"default-nometrics", "pd"}, Before: func(ctx context.Context, config string) error {
		before = append(before, len(g.servedConfigs()))
		resets++
		return l.Reset(ctx)
	}}
	reps, err := RunConfigs(ctx, g.api, g.url, "", reqs, Config{}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 2 || reps[0].Name != "default-nometrics" || reps[1].Name != "pd" || resets != 2 || !slices.Equal(before, []int{0, 40}) {
		t.Fatalf("%d reports, %d resets, served before each %v", len(reps), resets, before)
	}
	for _, rep := range reps {
		if rep.Succeeded != 40 || rep.Streamed != 40 || len(rep.Endpoints) != 3 || rep.TTFT.Count != 40 {
			t.Fatalf("%s report %+v", rep.Name, rep)
		}
	}
	served := g.servedConfigs()
	want := append(slices.Repeat([]string{"default-nometrics"}, 40), slices.Repeat([]string{"pd"}, 40)...)
	if !slices.Equal(served, want) {
		t.Fatalf("requests ran under %v", served)
	}
	total := 0
	for _, ls := range l.stubs {
		total += ls.cur.Load().Stats().Served
	}
	if total != 40 {
		t.Fatalf("stubs served %d since the last reset, the second run sent 40", total)
	}
	g.locked(func() {
		if got := g.pools["bench"].GetSpec().GetSchedulerConfig(); got != "default" {
			t.Fatalf("pool left on %s, it started on default", got)
		}
	})
	addrs := l.Addresses()
	if err := l.Close(ctx); err != nil {
		t.Fatal(err)
	}
	g.locked(func() {
		if len(g.pools) != 0 || len(g.routes) != 0 {
			t.Fatalf("left pools %v routes %v", g.pools, g.routes)
		}
	})
	for _, addr := range addrs {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			t.Fatalf("stub %s still listens", addr)
		}
	}
}

func TestLocalRefuses(t *testing.T) {
	reqs := localTrace(t)
	cases := []struct {
		name  string
		setup func(*fakeGateway)
		opts  LocalOptions
		reqs  []Request
		want  string
	}{
		{name: "no stubs", opts: LocalOptions{}, want: "0 stubs"},
		{name: "nine stubs", opts: LocalOptions{Stubs: 9}, want: "one to 8"},
		{name: "adapters given", opts: LocalOptions{Stubs: 1, Stub: stub.Config{Adapters: []stub.Adapter{{Name: "x", Path: "y"}}}}, want: "come from the trace"},
		{name: "pool held", setup: func(g *fakeGateway) { g.pools["nebu-bench"] = &v1.Pool{Id: "nebu-bench"} }, opts: LocalOptions{Stubs: 1}, want: "already holds pool"},
		{name: "route held", setup: func(g *fakeGateway) { g.routes["bench-model"] = &v1.Route{Name: "bench-model"} }, opts: LocalOptions{Stubs: 1}, want: "already routes bench-model"},
		{name: "unknown runtime", opts: LocalOptions{Stubs: 1, Runtime: "nemo"}, want: "none the stub emulates"},
		{name: "names against runtime", opts: LocalOptions{Stubs: 1, Stub: stub.Config{Names: stub.NamesSGLang}}, want: "differ from runtime vllm"},
		{name: "images", opts: LocalOptions{Stubs: 1}, reqs: []Request{{Messages: []Message{{Role: "user", Images: []string{"https://x/a.png"}}}}}, want: "request 0 sends images"},
		{name: "unknown objective", opts: LocalOptions{Stubs: 1}, reqs: []Request{{Prompt: "a", Objective: "interactive"}}, want: "[interactive]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGateway(t, "default")
			if tc.setup != nil {
				g.locked(func() { tc.setup(g) })
			}
			trace := reqs
			if tc.reqs != nil {
				trace = tc.reqs
			}
			_, err := StartLocal(context.Background(), g.api, trace, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want it to mention %q", err, tc.want)
			}
			g.locked(func() {
				if tc.setup == nil && (len(g.pools) != 0 || len(g.routes) != 0) {
					t.Fatalf("refusal left pools %v routes %v", g.pools, g.routes)
				}
			})
		})
	}
}

func TestLocalTakesDefinedObjectives(t *testing.T) {
	g := newFakeGateway(t, "default")
	g.locked(func() { g.objectives = []string{"interactive"} })
	l, err := StartLocal(context.Background(), g.api, []Request{{Prompt: "a", Objective: "interactive"}}, LocalOptions{Stubs: 1, Stub: fastStub(), Runtime: "llamacpp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Routes()) != 0 {
		t.Fatalf("a trace without models created routes %v", l.Routes())
	}
	g.locked(func() {
		if rt := g.pools[l.Pool()].GetSpec().GetRuntimeId(); rt != "llamacpp" {
			t.Fatalf("pool runtime %s", rt)
		}
	})
	resp, err := http.Get("http://" + l.Addresses()[0] + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	metrics, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(metrics), "llamacpp:requests_processing") {
		t.Fatalf("stub serves metrics\n%s", metrics)
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRunConfigsRefuses(t *testing.T) {
	g := newFakeGateway(t, "default", "broken")
	g.locked(func() { g.broken["broken"] = true })
	l, err := StartLocal(context.Background(), g.api, localTrace(t), LocalOptions{Stubs: 1, Stub: fastStub()})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close(context.Background())
	cases := []struct {
		name string
		plan Plan
		want string
	}{
		{"no pool", Plan{Configs: []string{"default"}}, "needs a pool"},
		{"no configs", Plan{Pool: l.Pool()}, "names no scheduler"},
		{"empty name", Plan{Pool: l.Pool(), Configs: []string{""}}, "is empty"},
		{"twice", Plan{Pool: l.Pool(), Configs: []string{"default", "default"}}, "named twice"},
		{"unknown", Plan{Pool: l.Pool(), Configs: []string{"fastest"}}, "holds broken, default"},
		{"missing pool", Plan{Pool: "elsewhere", Configs: []string{"default"}}, "reading pool elsewhere"},
		{"refused config", Plan{Pool: l.Pool(), Configs: []string{"broken"}}, "fails to load"},
		{"slow pool", Plan{Pool: l.Pool(), Configs: []string{"default"}, Before: func(context.Context, string) error {
			l.stubs[0].cur.Load().Pause()
			return nil
		}, ReadyTimeout: 300 * time.Millisecond}, "did not settle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reps, err := RunConfigs(context.Background(), g.api, g.url, "", localTrace(t), Config{}, tc.plan)
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(reps) != 0 {
				t.Fatalf("err %v reports %d, want it to mention %q", err, len(reps), tc.want)
			}
			g.locked(func() {
				if got := g.pools[l.Pool()].GetSpec().GetSchedulerConfig(); got != "default" {
					t.Fatalf("pool left on %s", got)
				}
			})
		})
	}
}
