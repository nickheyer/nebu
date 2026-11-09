package pools

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/datalayer/scrape"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"google.golang.org/protobuf/proto"
)

// Every metric name field, the replica label and an empty valued matcher survive the trip through capabilities
func TestMetricNamesRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		names *v1.MetricNames
	}{
		{"every field", &v1.MetricNames{
			QueuedRequests: "q", RunningRequests: "r", KvCacheUtilization: "kv", CacheConfigInfo: "info", BlockSizeLabel: "bs", NumBlocksLabel: "nb",
			LoraInfo: "lora", MaxLoraLabel: "max", RunningAdaptersLabel: "running", WaitingAdaptersLabel: "waiting", MetricsPath: "/metrics", HealthPath: "/health",
			SlotsPath: "/slots", MetricsPort: 8002, QueuedRequestsMatch: map[string]string{"priority": ""}, RunningRequestsMatch: map[string]string{"kind": "run"},
			KvCacheUtilizationMatch: map[string]string{"type": "fraction"}, BlockSize: "bsg", BlockSizeMatch: map[string]string{"a": "b"}, NumBlocks: "nbg",
			NumBlocksMatch: map[string]string{"c": "d"}, AdaptersPath: "/v1/models", AdaptersFormat: "openai", MaxLoraPath: "/server_info", MaxLoraField: "max_loras_per_batch",
			StaticCache: true, ReplicaLabel: "dp_rank",
		}},
		{"sglang declared names", runtimes.SGLang{}.Metrics()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := namesOf(capabilitiesOf(c.names, "")); !proto.Equal(got, c.names) {
				t.Fatalf("round trip\n got  %v\n want %v", got, c.names)
			}
		})
	}
}

// The names a runtime declares for its launches read as the scrape table's capabilities, a runtime without metrics in neither
func TestRuntimeNamesMatchScrapeTable(t *testing.T) {
	cases := []struct {
		rt      runtimes.Runtime
		metrics bool
	}{
		{runtimes.LlamaCpp{}, true},
		{runtimes.VLLM{}, true},
		{runtimes.SGLang{}, true},
		{runtimes.NeMo{}, true},
		{runtimes.SDCpp{}, false},
	}
	if len(cases) != len(runtimes.All()) {
		t.Fatalf("%d runtimes ship, %d checked", len(runtimes.All()), len(cases))
	}
	for _, c := range cases {
		t.Run(c.rt.ID(), func(t *testing.T) {
			want, ok := scrape.Capabilities(c.rt.ID())
			if ok != c.metrics || (c.rt.Metrics() != nil) != c.metrics {
				t.Fatalf("table entry %v, declared names %v, want metrics %v", ok, c.rt.Metrics(), c.metrics)
			}
			if got := capabilitiesOf(c.rt.Metrics(), c.rt.ID()); c.metrics && !reflect.DeepEqual(got, want) {
				t.Fatalf("declared %+v\n table    %+v", got, want)
			}
		})
	}
}

// A static endpoint's declared KV capacity becomes its kv-capacity label, one declaring none carries no label
func TestStaticEndpointKVCapacity(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	spec := &v1.PoolSpec{Model: "external-llama", RuntimeId: "vllm", TargetPorts: []uint32{8080},
		StaticEndpoints: []*v1.StaticEndpoint{{Address: "10.1.0.1:8080", KvCapacity: 65536}, {Address: "10.1.0.2:8080"}}}
	p, err := m.PutPool(ctx, "llama", spec)
	if err != nil {
		t.Fatal(err)
	}
	if p.GetSpec().GetStaticEndpoints()[0].GetKvCapacity() != 65536 {
		t.Fatalf("spec dropped the capacity %v", p.GetSpec().GetStaticEndpoints())
	}
	cases := []struct {
		id    datalayer.EndpointID
		label string
		ok    bool
	}{
		{"llama/10.1.0.1:8080", "65536", true},
		{"llama/10.1.0.2:8080", "", false},
	}
	for _, c := range cases {
		ep, found := m.Store().Endpoint(c.id)
		if !found {
			t.Fatalf("no endpoint %s", c.id)
		}
		if got, ok := ep.Labels()[datalayer.LabelKVCapacity]; got != c.label || ok != c.ok {
			t.Fatalf("%s kv-capacity %q %v, want %q %v", c.id, got, ok, c.label, c.ok)
		}
	}
}

// A pool whose metrics state no pool size refuses a static endpoint without kv_capacity, naming it and the CLI syntax
func TestSizedPoolsNeedKVCapacity(t *testing.T) {
	with := []*v1.StaticEndpoint{{Address: "10.1.0.1:8080", KvCapacity: 65536}, {Address: "10.1.0.2:8080", KvCapacity: 65536}}
	without := []*v1.StaticEndpoint{{Address: "10.1.0.1:8080", KvCapacity: 65536}, {Address: "10.1.0.2:8080"}}
	cases := []struct {
		name    string
		runtime string
		metrics *v1.MetricNames
		statics []*v1.StaticEndpoint
		refused bool
	}{
		{"llama.cpp slots with every pool size", "llamacpp", nil, with, false},
		{"llama.cpp slots missing one pool size", "llamacpp", nil, without, true},
		{"NeMo static caches missing one pool size", "nemo", nil, without, true},
		{"named slot listing missing one pool size", "", &v1.MetricNames{QueuedRequests: "q", RunningRequests: "r", SlotsPath: "/slots", MetricsPath: "/metrics"}, without, true},
		{"vLLM block gauges need no pool size", "vllm", nil, without, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _, _ := newManager(t)
			_, err := m.PutPool(context.Background(), "ext", &v1.PoolSpec{Model: "m", RuntimeId: c.runtime, Metrics: c.metrics, TargetPorts: []uint32{8080}, StaticEndpoints: c.statics})
			if !c.refused {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "static endpoint 10.1.0.2:8080 needs kv_capacity") || !strings.Contains(err.Error(), "--static 10.1.0.2:8080,kv-capacity=TOKENS") {
				t.Fatalf("err = %v", err)
			}
			if _, ok := m.Pool("ext"); ok {
				t.Fatal("a refused pool was stored")
			}
		})
	}
}
