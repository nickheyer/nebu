package instances

import (
	"context"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

// A published llama.cpp launch states its whole KV pool from its recorded or planned n_ctx, and one stating neither is refused
func TestLlamaCppPublishNeedsPoolSize(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		plan   *v1.MemoryPlan
		want   uint32
	}{
		{"recorded context", map[string]string{"n_ctx": "8192"}, nil, 8192},
		{"recorded context over the planned one", map[string]string{"n_ctx": "8192", "kv_unified": "off"}, &v1.MemoryPlan{Params: map[string]string{"n_ctx": "4096"}}, 8192},
		{"planned context", nil, &v1.MemoryPlan{Params: map[string]string{"n_ctx": "4096"}}, 4096},
		{"unified slots of a set size", map[string]string{"kv_unified": "on", "kv_unified_per_slot": "2048", "n_parallel": "4"}, nil, 8192},
		{"no context recorded or planned", nil, &v1.MemoryPlan{}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg := fake.NewRegistrar()
			m := &Manager{Registrar: reg}
			rec := &v1.Instance{Id: db.NewID(), Name: "llama", RuntimeId: "llamacpp", Params: c.params, Plan: c.plan, Endpoint: "http://10.0.0.7:8080", Healthy: true, State: v1.InstanceState_INSTANCE_STATE_READY}
			err := m.publish(context.Background(), m.newInstance(rec, runtimes.LlamaCpp{}))
			if c.want == 0 {
				if err == nil || !strings.Contains(err.Error(), "llama records no n_ctx") {
					t.Fatalf("err = %v", err)
				}
				if eps := reg.Endpoints(ownerOf(rec.GetId())); len(eps) != 0 {
					t.Fatalf("published %v", eps)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mb, ok := reg.Membership(ownerOf(rec.GetId())); !ok || mb.KVCapacity != c.want {
				t.Fatalf("membership %+v, want KV capacity %d", mb, c.want)
			}
		})
	}
}

// The fake runtime reporting slot use like llama.cpp with no pool size it can state
type unsizedRuntime struct{ fake.Runtime }

func (unsizedRuntime) Metrics() *v1.MetricNames { return runtimes.LlamaCpp{}.Metrics() }

func (unsizedRuntime) Params() []*v1.Param {
	return append(fake.Runtime{}.Params(), &v1.Param{Name: "n_ctx", Type: v1.ParamType_PARAM_TYPE_INT, Label: "Context length"})
}

func (unsizedRuntime) Policy() *estimate.Policy {
	p := fake.Runtime{}.Policy()
	p.ContextParam = "n_ctx"
	return p
}

// A run whose pool size is unknown is refused before it launches, failed with a Ready condition naming n_ctx
func TestUnsizedLaunchNotPublished(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	rts, err := runtimes.New([]runtimes.Runtime{unsizedRuntime{}})
	if err != nil {
		t.Fatal(err)
	}
	m.Runtimes = rts
	if _, _, err := m.Run(ctx, soloRun(nil)); err == nil || !strings.Contains(err.Error(), "solo records no n_ctx") {
		t.Fatalf("run err = %v", err)
	}
	var rec *v1.Instance
	for _, r := range m.List(false) {
		if r.GetName() == "solo" {
			rec = r
		}
	}
	c := ConditionOf(rec.GetConditions(), CondReady)
	if rec.GetState() != v1.InstanceState_INSTANCE_STATE_FAILED || c.GetReason() != ReasonLaunchFailed || !strings.Contains(c.GetMessage(), "records no n_ctx") {
		t.Fatalf("refused run %v", rec)
	}
	if eps := n.registrar.Endpoints(ownerOf(rec.GetId())); len(eps) != 0 || n.launcher.Launches("solo") != 0 {
		t.Fatalf("an unsized run published %v after %d launches", eps, n.launcher.Launches("solo"))
	}
}
