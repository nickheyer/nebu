package instances

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

// A loopback server listing one loaded adapter under the base model, as vLLM and SGLang answer the model listing
func adapterListing(t *testing.T) (host string, port int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"m","object":"model","root":"m","parent":null},{"id":"sql","object":"model","root":"/adapters/sql","parent":"m"}]}`)
	}))
	t.Cleanup(srv.Close)
	addr, err := addressOf(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return addr.Host, addr.Port
}

// Published endpoints carry the KV event and replay addresses their runtime derives for each rank, none without KV events
func TestPublishedKVEventLabels(t *testing.T) {
	loHost, loPort := adapterListing(t)
	lo := func(off int) string { return loHost + ":" + strconv.Itoa(loPort+off) }
	type kv struct{ events, replay, adapter string }
	cases := []struct {
		name   string
		rt     runtimes.Runtime
		params map[string]string
		ranks  []string
		want   []kv
	}{
		{"vllm one rank", runtimes.VLLM{}, nil, []string{"http://10.0.0.5:8000"}, []kv{{"10.0.0.5:8001", "10.0.0.5:8002", ""}}},
		{"vllm two ranks", runtimes.VLLM{}, map[string]string{"data_parallel_size": "2"}, []string{"http://10.0.0.5:8000", "http://10.0.0.5:8001"},
			[]kv{{"10.0.0.5:8002", "10.0.0.5:8004", ""}, {"10.0.0.5:8003", "10.0.0.5:8005", ""}}},
		{"vllm coordinated ranks after the coordinator port", runtimes.VLLM{}, map[string]string{"data_parallel_size": "2", "data_parallel_rpc_port": "13345"}, []string{"http://10.0.0.5:8000", "http://10.0.0.5:8001"},
			[]kv{{"10.0.0.5:8003", "10.0.0.5:8005", ""}, {"10.0.0.5:8004", "10.0.0.5:8006", ""}}},
		{"vllm gRPC ranks two ports apart", runtimes.VLLM{}, map[string]string{"data_parallel_size": "2", "grpc": "true"}, []string{"http://10.0.0.5:8000", "http://10.0.0.5:8002"},
			[]kv{{"10.0.0.5:8004", "10.0.0.5:8006", ""}, {"10.0.0.5:8005", "10.0.0.5:8007", ""}}},
		{"sglang one rank", runtimes.SGLang{}, nil, []string{"http://10.0.0.6:30000"}, []kv{{"10.0.0.6:30001", "10.0.0.6:30002", ""}}},
		{"sglang two ranks", runtimes.SGLang{}, map[string]string{"dp_size": "2"}, []string{"http://10.0.0.6:30000", "http://10.0.0.6:30001"},
			[]kv{{"10.0.0.6:30002", "10.0.0.6:30004", ""}, {"10.0.0.6:30003", "10.0.0.6:30005", ""}}},
		{"sglang adapter endpoint keeps its rank's addresses", runtimes.SGLang{}, map[string]string{"enable_lora": "true"}, []string{"http://" + lo(0)},
			[]kv{{lo(1), lo(2), ""}, {lo(1), lo(2), "sql"}}},
		{"vllm adapter endpoint keeps its rank's addresses", runtimes.VLLM{}, map[string]string{"enable_lora": "true"}, []string{"http://" + lo(0)},
			[]kv{{lo(1), lo(2), ""}, {lo(1), lo(2), "sql"}}},
		{"vllm without prefix caching", runtimes.VLLM{}, map[string]string{"enable_prefix_caching": "off"}, []string{"http://10.0.0.5:8000"}, []kv{{}}},
		{"sglang without the radix cache", runtimes.SGLang{}, map[string]string{"disable_radix_cache": "true"}, []string{"http://10.0.0.6:30000"}, []kv{{}}},
		{"llamacpp publishes no events", runtimes.LlamaCpp{}, map[string]string{"n_ctx": "8192"}, []string{"http://10.0.0.7:8080"}, []kv{{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			reg := fake.NewRegistrar()
			m := &Manager{Registrar: reg}
			rec := &v1.Instance{Id: db.NewID(), Name: "kv", RuntimeId: c.rt.ID(), Params: c.params, Endpoint: c.ranks[0], Healthy: true, State: v1.InstanceState_INSTANCE_STATE_READY}
			if len(c.ranks) > 1 {
				for i, ep := range c.ranks {
					rec.Ranks = append(rec.Ranks, &v1.InstanceRank{Rank: uint32(i), Endpoint: ep, Healthy: true})
				}
			}
			if err := m.publish(ctx, m.newInstance(rec, c.rt)); err != nil {
				t.Fatal(err)
			}
			eps := reg.Endpoints(ownerOf(rec.GetId()))
			if len(eps) != len(c.want) {
				t.Fatalf("published %d endpoints, want %d: %+v", len(eps), len(c.want), eps)
			}
			for i, ep := range eps {
				want := c.want[i]
				events, hasEvents := ep.Labels[datalayer.LabelKVEvents]
				replay, hasReplay := ep.Labels[datalayer.LabelKVReplay]
				if want.events == "" {
					if hasEvents || hasReplay {
						t.Fatalf("endpoint %d carries kv labels %v", i, ep.Labels)
					}
					continue
				}
				if events != want.events || replay != want.replay || ep.Labels[datalayer.LabelAdapter] != want.adapter || ep.Adapter != want.adapter {
					t.Fatalf("endpoint %d rank %d labels %v adapter %q, want events %s replay %s adapter %q", i, ep.Rank, ep.Labels, ep.Adapter, want.events, want.replay, want.adapter)
				}
			}
		})
	}
}
