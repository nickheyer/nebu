package filter_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/internal/schedtest"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/filter"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins/picker"
)

func set(names ...string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

func TestFilters(t *testing.T) {
	clock := schedtest.NewClock()
	now := clock.Now()
	ep := schedtest.EP
	m := func(q int, kv float64) datalayer.ServerMetrics {
		return datalayer.ServerMetrics{QueuedRequests: q, KVCacheUtilization: kv, UpdatedAt: now}
	}
	lora := func(max int, running, waiting []string) datalayer.ServerMetrics {
		return datalayer.ServerMetrics{MaxLoRA: max, RunningAdapters: set(running...), WaitingAdapters: set(waiting...), UpdatedAt: now}
	}
	addr := func(s string) datalayer.Address { a, _ := datalayer.ParseAddress(s); return a }

	tests := []struct {
		name   string
		filter framework.Filter
		req    framework.Request
		eps    []*schedtest.Endpoint
		want   []string
	}{
		{
			name:   "ready keeps ready endpoints healthy or never probed",
			filter: filter.NewReadyFilter(),
			eps: []*schedtest.Endpoint{
				ep("healthy").With(datalayer.CollectorHealth, &datalayer.HealthState{Healthy: true}),
				ep("unprobed"),
				ep("notready").NotReady(),
				ep("sick").With(datalayer.CollectorHealth, &datalayer.HealthState{Healthy: false}),
			},
			want: []string{"healthy", "unprobed"},
		},
		{
			name:   "fresh keeps metrics no older than the threshold",
			filter: filter.NewFreshMetricsFilter(2*time.Second, clock),
			eps: []*schedtest.Endpoint{
				ep("new").Metrics(datalayer.ServerMetrics{UpdatedAt: now.Add(-time.Second)}),
				ep("edge").Metrics(datalayer.ServerMetrics{UpdatedAt: now.Add(-2 * time.Second)}),
				ep("old").Metrics(datalayer.ServerMetrics{UpdatedAt: now.Add(-3 * time.Second)}),
				ep("none"),
			},
			want: []string{"new", "edge"},
		},
		{
			name:   "subset unset keeps every endpoint",
			filter: filter.NewSubsetFilter(),
			eps:    []*schedtest.Endpoint{ep("a"), ep("b")},
			want:   []string{"a", "b"},
		},
		{
			name:   "subset set and empty keeps none",
			filter: filter.NewSubsetFilter(),
			req:    framework.Request{SubsetSet: true},
			eps:    []*schedtest.Endpoint{ep("a"), ep("b")},
			want:   []string{},
		},
		{
			name:   "subset matches exact addresses and whole hosts for port zero",
			filter: filter.NewSubsetFilter(),
			req:    framework.Request{SubsetSet: true, Subset: []datalayer.Address{addr("10.0.0.1:8001"), {Host: "10.0.0.2"}}},
			eps: []*schedtest.Endpoint{
				ep("a").At("10.0.0.1", 8000),
				ep("b").At("10.0.0.1", 8001),
				ep("c").At("10.0.0.2", 8000),
				ep("d").At("10.0.0.2", 9000),
				ep("e").At("10.0.0.3", 8001),
			},
			want: []string{"b", "c", "d"},
		},
		{
			name:   "subset listed without the set flag still restricts",
			filter: filter.NewSubsetFilter(),
			req:    framework.Request{Subset: []datalayer.Address{addr("10.0.0.1:8000")}},
			eps:    []*schedtest.Endpoint{ep("a").At("10.0.0.1", 8000), ep("b").At("10.0.0.2", 8000)},
			want:   []string{"a"},
		},
		{
			name:   "role keeps the role and both",
			filter: filter.NewRoleFilter(datalayer.RolePrefill),
			eps: []*schedtest.Endpoint{
				ep("p").Label(datalayer.LabelRole, datalayer.RolePrefill),
				ep("d").Label(datalayer.LabelRole, datalayer.RoleDecode),
				ep("b").Label(datalayer.LabelRole, datalayer.RoleBoth),
				ep("n"),
			},
			want: []string{"p", "b"},
		},
		{
			name:   "kv threshold drops above max and unknown",
			filter: filter.NewKVThresholdFilter(0.8),
			eps:    []*schedtest.Endpoint{ep("low").Metrics(m(0, 0.5)), ep("at").Metrics(m(0, 0.8)), ep("high").Metrics(m(0, 0.81)), ep("none")},
			want:   []string{"low", "at"},
		},
		{
			name:   "queue threshold drops above max and unknown",
			filter: filter.NewQueueThresholdFilter(5),
			eps:    []*schedtest.Endpoint{ep("q0").Metrics(m(0, 0)), ep("q5").Metrics(m(5, 0)), ep("q6").Metrics(m(6, 0)), ep("none")},
			want:   []string{"q0", "q5"},
		},
		{
			name:   "lora keeps all without an adapter",
			filter: filter.NewLoRAAffinityFilter(),
			eps:    []*schedtest.Endpoint{ep("a").Metrics(lora(1, []string{"x"}, nil)), ep("none")},
			want:   []string{"a", "none"},
		},
		{
			name:   "lora prefers active adapters",
			filter: filter.NewLoRAAffinityFilter(),
			req:    framework.Request{Adapter: "x"},
			eps: []*schedtest.Endpoint{
				ep("active").Metrics(lora(2, []string{"x", "y"}, nil)),
				ep("free").Metrics(lora(2, []string{"y"}, nil)),
				ep("waiting").Metrics(lora(1, []string{"y"}, []string{"x"})),
			},
			want: []string{"active"},
		},
		{
			name:   "lora falls to free capacity",
			filter: filter.NewLoRAAffinityFilter(),
			req:    framework.Request{Adapter: "x"},
			eps: []*schedtest.Endpoint{
				ep("full").Metrics(lora(1, []string{"y"}, nil)),
				ep("free").Metrics(lora(2, []string{"y"}, nil)),
				ep("waiting").Metrics(lora(1, []string{"y"}, []string{"x"})),
			},
			want: []string{"free"},
		},
		{
			name:   "lora falls to waiting",
			filter: filter.NewLoRAAffinityFilter(),
			req:    framework.Request{Adapter: "x"},
			eps: []*schedtest.Endpoint{
				ep("full").Metrics(lora(1, []string{"y"}, nil)),
				ep("waiting").Metrics(lora(1, []string{"y"}, []string{"x"})),
				ep("none"),
			},
			want: []string{"waiting"},
		},
		{
			name:   "lora keeps none when no endpoint can take the adapter",
			filter: filter.NewLoRAAffinityFilter(),
			req:    framework.Request{Adapter: "x"},
			eps:    []*schedtest.Endpoint{ep("full").Metrics(lora(1, []string{"y"}, nil)), ep("none")},
			want:   []string{},
		},
		{
			name:   "lora serves by label or catalog without gauges and by running gauge with them",
			filter: filter.NewLoRAAffinityFilter(),
			req:    framework.Request{Adapter: "x"},
			eps: []*schedtest.Endpoint{
				ep("labeled").Metrics(datalayer.ServerMetrics{UpdatedAt: now}).Label(datalayer.LabelAdapter, "x"),
				ep("cataloged").With(datalayer.CollectorCatalog, &datalayer.Catalog{Adapters: []string{"x"}}),
				ep("running").Metrics(lora(2, []string{"x"}, nil)),
				ep("other").With(datalayer.CollectorCatalog, &datalayer.Catalog{Adapters: []string{"y"}}),
				ep("free").Metrics(lora(2, []string{"y"}, nil)),
				ep("none"),
			},
			want: []string{"labeled", "cataloged", "running"},
		},
		{
			name:   "capacity drops full seats and token loads over the kv cache",
			filter: filter.NewCapacityFilter(),
			req:    framework.Request{PromptTokens: 100, ExpectedOutputTokens: 50},
			eps: []*schedtest.Endpoint{
				ep("room").With(datalayer.CollectorCapacity, 2).With(datalayer.CollectorAssumed, 1),
				ep("full").With(datalayer.CollectorCapacity, 2).With(datalayer.CollectorAssumed, 2),
				ep("uncapped").With(datalayer.CollectorAssumed, 50),
				ep("kv-room").Metrics(datalayer.ServerMetrics{NumGPUBlocks: 20, BlockSize: 16}).With(datalayer.CollectorAssumedTokens, 170),
				ep("kv-full").Metrics(datalayer.ServerMetrics{NumGPUBlocks: 20, BlockSize: 16}).With(datalayer.CollectorAssumedTokens, 171),
				ep("kv-empty").Label(datalayer.LabelKVCapacity, "10"),
				ep("kv-label-full").Label(datalayer.LabelKVCapacity, "200").With(datalayer.CollectorAssumedTokens, 100),
			},
			want: []string{"room", "uncapped", "kv-room", "kv-empty"},
		},
		{
			name:   "objective keeps every endpoint without objectives",
			filter: filter.NewObjectiveFilter(&schedtest.Predictor{TTFT: map[datalayer.EndpointID]time.Duration{"slow": time.Hour}}, schedtest.NewIndexer(4)),
			eps:    []*schedtest.Endpoint{ep("slow"), ep("none")},
			want:   []string{"slow", "none"},
		},
		{
			name: "objective drops confident predictions missing by mean plus one deviation",
			filter: filter.NewObjectiveFilter(&schedtest.Predictor{
				TTFT:      map[datalayer.EndpointID]time.Duration{"in": 80 * time.Millisecond, "edge": 90 * time.Millisecond, "out": 95 * time.Millisecond, "tpot-out": 10 * time.Millisecond},
				TPOT:      map[datalayer.EndpointID]time.Duration{"in": 5 * time.Millisecond, "edge": 5 * time.Millisecond, "out": 5 * time.Millisecond, "tpot-out": 12 * time.Millisecond},
				StdDev:    10 * time.Millisecond,
				Confident: true,
			}, schedtest.NewIndexer(4)),
			req:  framework.Request{Objective: framework.Objective{TTFT: 100 * time.Millisecond, TPOT: 20 * time.Millisecond}},
			eps:  []*schedtest.Endpoint{ep("in"), ep("edge"), ep("out"), ep("tpot-out")},
			want: []string{"in", "edge"},
		},
		{
			name: "objective judges unmeasured endpoints by the prior mean alone",
			filter: filter.NewObjectiveFilter(&schedtest.Predictor{
				TTFT:   map[datalayer.EndpointID]time.Duration{"prior-in": 90 * time.Millisecond, "prior-out": 101 * time.Millisecond},
				StdDev: time.Hour,
			}, schedtest.NewIndexer(4)),
			req:  framework.Request{Objective: framework.Objective{TTFT: 100 * time.Millisecond}},
			eps:  []*schedtest.Endpoint{ep("prior-in"), ep("prior-out")},
			want: []string{"prior-in"},
		},
		{
			name:   "least queue keeps within the margin of the lowest",
			filter: filter.NewLeastQueueFilter(2),
			eps:    []*schedtest.Endpoint{ep("q3").Metrics(m(3, 0)), ep("q5").Metrics(m(5, 0)), ep("q6").Metrics(m(6, 0)), ep("none")},
			want:   []string{"q3", "q5"},
		},
		{
			name:   "least queue with zero margin keeps ties at the lowest",
			filter: filter.NewLeastQueueFilter(0),
			eps:    []*schedtest.Endpoint{ep("a").Metrics(m(1, 0)), ep("b").Metrics(m(1, 0)), ep("c").Metrics(m(2, 0))},
			want:   []string{"a", "b"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := schedtest.IDs(tc.filter.Filter(context.Background(), framework.NewCycleState(), &tc.req, schedtest.Eps(tc.eps...)))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFilterNames(t *testing.T) {
	type named interface {
		framework.Filter
		SetName(string)
	}
	tests := []struct {
		f   named
		typ string
	}{
		{filter.NewReadyFilter(), filter.ReadyFilterType},
		{filter.NewFreshMetricsFilter(time.Second, schedtest.NewClock()), filter.FreshMetricsFilterType},
		{filter.NewSubsetFilter(), filter.SubsetFilterType},
		{filter.NewRoleFilter(datalayer.RoleDecode), filter.RoleFilterType},
		{filter.NewKVThresholdFilter(0.9), filter.KVThresholdFilterType},
		{filter.NewQueueThresholdFilter(3), filter.QueueThresholdFilterType},
		{filter.NewLoRAAffinityFilter(), filter.LoRAAffinityFilterType},
		{filter.NewLeastQueueFilter(1), filter.LeastQueueFilterType},
		{filter.NewCapacityFilter(), filter.CapacityFilterType},
		{filter.NewObjectiveFilter(&schedtest.Predictor{}, schedtest.NewIndexer(4)), filter.ObjectiveFilterType},
	}
	for _, tc := range tests {
		t.Run(tc.typ, func(t *testing.T) {
			if got := tc.f.TypedName(); got.Type != tc.typ || got.Name != tc.typ {
				t.Fatalf("default name %+v", got)
			}
			tc.f.SetName("custom")
			if got := tc.f.TypedName(); got.Type != tc.typ || got.Name != "custom" {
				t.Fatalf("renamed %+v", got)
			}
		})
	}
}

// Every filter that drops to nothing on a request it could serve later refuses the profile run as saturated
func TestSaturatedRefusals(t *testing.T) {
	clock := schedtest.NewClock()
	full := datalayer.ServerMetrics{UpdatedAt: clock.Now(), MaxLoRA: 1, RunningAdapters: set("other")}
	slow := &schedtest.Predictor{TTFT: map[datalayer.EndpointID]time.Duration{"a": time.Second, "b": time.Second}, Confident: true}
	tests := []struct {
		name   string
		filter framework.Filter
		req    framework.Request
		eps    []*schedtest.Endpoint
		want   error
	}{
		{"lora every slot taken", filter.NewLoRAAffinityFilter(), framework.Request{Adapter: "x"}, []*schedtest.Endpoint{schedtest.EP("a").Metrics(full), schedtest.EP("b").Metrics(full)}, framework.ErrSaturated},
		{"lora nothing can ever serve", filter.NewLoRAAffinityFilter(), framework.Request{Adapter: "x"}, []*schedtest.Endpoint{schedtest.EP("a"), schedtest.EP("b").With(datalayer.CollectorCatalog, &datalayer.Catalog{Adapters: []string{"y"}})}, framework.ErrNoEndpoints},
		{"capacity every seat full", filter.NewCapacityFilter(), framework.Request{}, []*schedtest.Endpoint{schedtest.EP("a").With(datalayer.CollectorCapacity, 1).With(datalayer.CollectorAssumed, 1)}, framework.ErrSaturated},
		{"objective every endpoint misses", filter.NewObjectiveFilter(slow, schedtest.NewIndexer(4)), framework.Request{Objective: framework.Objective{TTFT: time.Millisecond}}, []*schedtest.Endpoint{schedtest.EP("a"), schedtest.EP("b")}, framework.ErrSaturated},
		{"metrics every sample stale", filter.NewFreshMetricsFilter(time.Second, clock), framework.Request{}, []*schedtest.Endpoint{schedtest.EP("a").Metrics(datalayer.ServerMetrics{UpdatedAt: clock.Now().Add(-time.Minute)}), schedtest.EP("b")}, framework.ErrSaturated},
		{"kv every endpoint over the threshold", filter.NewKVThresholdFilter(0.9), framework.Request{}, []*schedtest.Endpoint{schedtest.EP("a").Metrics(datalayer.ServerMetrics{UpdatedAt: clock.Now(), KVCacheUtilization: 0.95})}, framework.ErrSaturated},
		{"queue every endpoint over the threshold", filter.NewQueueThresholdFilter(2), framework.Request{}, []*schedtest.Endpoint{schedtest.EP("a").Metrics(datalayer.ServerMetrics{UpdatedAt: clock.Now(), QueuedRequests: 3})}, framework.ErrSaturated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &framework.Profile{Name: "p", Filters: []framework.Filter{tc.filter}, Picker: picker.NewMaxScorePicker(1)}
			_, err := framework.RunProfile(context.Background(), framework.NewCycleState(), &tc.req, p, schedtest.Eps(tc.eps...))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
			if errors.Is(err, framework.ErrSaturated) && errors.Is(err, framework.ErrNoEndpoints) {
				t.Fatalf("err %v names both", err)
			}
		})
	}
}
