package config_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/config"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/internal/schedtest"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

func deps(clock *schedtest.Clock) registry.Deps {
	return registry.Deps{Store: schedtest.NewStore(), Indexer: schedtest.NewIndexer(4), Predictor: &schedtest.Predictor{}, Clock: clock}
}

func names[T framework.Plugin](ps []T) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.TypedName().String()
	}
	return out
}

func scorers(ws []framework.WeightedScorer) map[string]float64 {
	out := map[string]float64{}
	for _, w := range ws {
		out[w.TypedName().Name] = w.Weight
	}
	return out
}

func equalWeights(a, b map[string]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestDefaults(t *testing.T) {
	clock := schedtest.NewClock()
	fresh := datalayer.ServerMetrics{UpdatedAt: clock.Now()}
	withRole := func(id, role string) *schedtest.Endpoint {
		return schedtest.EP(id).Label(datalayer.LabelRole, role).Metrics(fresh)
	}
	tests := []struct {
		name        string
		yaml        string
		profiles    map[string][]string
		weights     map[string]map[string]float64
		lifecycle   []string
		cands       []*schedtest.Endpoint
		req         framework.Request
		wantPrimary string
		wantPrefill bool
	}{
		{
			name: "metrics pool",
			yaml: config.DefaultConfig,
			profiles: map[string][]string{
				"default": {"ready-filter/ready", "fresh-metrics-filter/fresh", "subset-filter/subset", "kv-threshold-filter/kv-limit", "capacity-filter/capacity", "lora-affinity-filter/lora-filter", "objective-filter/objective"},
			},
			weights:     map[string]map[string]float64{"default": {"queue": 1, "kv": 1, "prefix": 3, "lora": 1, "assumed": 1, "latency": 2, "tps": 1}},
			lifecycle:   []string{"prefix-cache-scorer/prefix", "latency-scorer/latency", "assumed-load", "throughput"},
			cands:       []*schedtest.Endpoint{schedtest.EP("a").Metrics(fresh), schedtest.EP("b").Metrics(fresh), schedtest.EP("stale")},
			req:         framework.Request{ID: "r", PromptTokens: 10},
			wantPrimary: "default",
		},
		{
			name:        "pool without metrics",
			yaml:        config.DefaultNoMetricsConfig,
			profiles:    map[string][]string{"default": {"ready-filter/ready", "subset-filter/subset", "capacity-filter/capacity", "objective-filter/objective"}},
			weights:     map[string]map[string]float64{"default": {"assumed": 2, "prefix": 3, "latency": 2, "tps": 1}},
			lifecycle:   []string{"prefix-cache-scorer/prefix", "latency-scorer/latency", "assumed-load", "throughput"},
			cands:       []*schedtest.Endpoint{schedtest.EP("a"), schedtest.EP("b")},
			req:         framework.Request{ID: "r", PromptTokens: 10},
			wantPrimary: "default",
		},
		{
			name: "disaggregated pool",
			yaml: config.DefaultPDConfig,
			profiles: map[string][]string{
				"prefill": {"ready-filter/ready", "fresh-metrics-filter/fresh", "subset-filter/subset", "role-filter/prefill-role", "kv-threshold-filter/kv-limit", "capacity-filter/capacity", "lora-affinity-filter/lora-filter"},
				"decode":  {"ready-filter/ready", "fresh-metrics-filter/fresh", "subset-filter/subset", "role-filter/decode-role", "kv-threshold-filter/kv-limit", "capacity-filter/capacity", "lora-affinity-filter/lora-filter"},
			},
			weights: map[string]map[string]float64{
				"prefill": {"queue": 1, "kv": 1, "prefix": 3, "lora": 1, "assumed": 1, "ep": 1, "tps": 1},
				"decode":  {"queue": 1, "kv": 1, "prefix": 3, "lora": 1, "assumed": 1, "latency": 2, "ep": 1, "tps": 1},
			},
			lifecycle:   []string{"prefix-cache-scorer/prefix", "latency-scorer/latency", "assumed-load", "throughput"},
			cands:       []*schedtest.Endpoint{withRole("p", datalayer.RolePrefill), withRole("d", datalayer.RoleDecode)},
			req:         framework.Request{ID: "r", PromptTokens: 256},
			wantPrimary: "decode",
			wantPrefill: true,
		},
		{
			name: "replicas pool",
			yaml: config.DefaultReplicasConfig,
			profiles: map[string][]string{
				"replicas": {"ready-filter/ready", "fresh-metrics-filter/fresh", "subset-filter/subset", "kv-threshold-filter/kv-limit", "capacity-filter/capacity", "lora-affinity-filter/lora-filter"},
			},
			weights:     map[string]map[string]float64{"replicas": {"queue": 1, "kv": 1, "prefix": 4, "assumed": 1, "latency": 1, "ep": 1, "tps": 1}},
			lifecycle:   []string{"prefix-cache-scorer/prefix", "latency-scorer/latency", "assumed-load", "throughput"},
			cands:       []*schedtest.Endpoint{schedtest.EP("a").Metrics(fresh), schedtest.EP("b").Metrics(fresh), schedtest.EP("stale")},
			req:         framework.Request{ID: "r", PromptTokens: 10},
			wantPrimary: "replicas",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, lifecycle, err := config.Load([]byte(tc.yaml), deps(clock))
			if err != nil {
				t.Fatal(err)
			}
			got := s.Profiles()
			if len(got) != len(tc.profiles) {
				t.Fatalf("profiles %v", got)
			}
			for name, filters := range tc.profiles {
				p := got[name]
				if p == nil {
					t.Fatalf("profile %s missing", name)
				}
				if f := names(p.Filters); !slices.Equal(f, filters) {
					t.Fatalf("%s filters %v, want %v", name, f, filters)
				}
				if w := scorers(p.Scorers); !equalWeights(w, tc.weights[name]) {
					t.Fatalf("%s weights %v, want %v", name, w, tc.weights[name])
				}
				if p.Picker.TypedName().String() != "max-score-picker/picker" {
					t.Fatalf("%s picker %s", name, p.Picker.TypedName())
				}
			}
			if l := names(lifecycle); !slices.Equal(l, tc.lifecycle) {
				t.Fatalf("lifecycle %v, want %v", l, tc.lifecycle)
			}
			res, err := s.Schedule(context.Background(), &tc.req, schedtest.Eps(tc.cands...))
			if err != nil {
				t.Fatal(err)
			}
			if res.PrimaryProfile != tc.wantPrimary || len(res.Endpoints()) == 0 {
				t.Fatalf("primary %s endpoints %v", res.PrimaryProfile, schedtest.IDs(res.Endpoints()))
			}
			if slices.Contains(schedtest.IDs(res.Endpoints()), "stale") {
				t.Fatal("stale endpoint picked")
			}
			if tc.wantPrefill != (res.Profile("prefill") != nil) {
				t.Fatalf("prefill %v, want %v", schedtest.IDs(res.Profile("prefill")), tc.wantPrefill)
			}
			for name, p := range got {
				if len(p.Reservers) != 1 || p.Reservers[0].TypedName().Type != "assumed-load" {
					t.Fatalf("%s reservers %v", name, p.Reservers)
				}
			}
		})
	}
}

func TestPDThresholdFromDefault(t *testing.T) {
	clock := schedtest.NewClock()
	s, _, err := config.Load([]byte(config.DefaultPDConfig), deps(clock))
	if err != nil {
		t.Fatal(err)
	}
	fresh := datalayer.ServerMetrics{UpdatedAt: clock.Now().Add(-time.Second)}
	cands := schedtest.Eps(
		schedtest.EP("p").Label(datalayer.LabelRole, datalayer.RolePrefill).Metrics(fresh),
		schedtest.EP("d").Label(datalayer.LabelRole, datalayer.RoleDecode).Metrics(fresh),
	)
	for tokens, want := range map[int]bool{255: false, 256: true} {
		res, err := s.Schedule(context.Background(), &framework.Request{ID: "r", PromptTokens: tokens}, cands)
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Profile("prefill") != nil; got != want || schedtest.IDs(res.Endpoints())[0] != "d" {
			t.Fatalf("%d tokens prefill %v decode %v", tokens, got, schedtest.IDs(res.Endpoints()))
		}
	}
}

func TestRejects(t *testing.T) {
	const handler = `
- type: single-profile-handler
  name: handler
  parameters:
    profile: default
`
	const tail = `
profileHandler:
  pluginRef: handler
`
	tests := []struct {
		name string
		yaml string
		want error
	}{
		{
			name: "profile without a picker",
			yaml: "plugins:\n- type: ready-filter\n  name: ready" + handler + "profiles:\n- name: default\n  plugins:\n  - pluginRef: ready" + tail,
			want: framework.ErrNoPicker,
		},
		{
			name: "profile with two pickers",
			yaml: "plugins:\n- type: max-score-picker\n  name: a\n- type: random-picker\n  name: b" + handler + "profiles:\n- name: default\n  plugins:\n  - pluginRef: a\n  - pluginRef: b" + tail,
			want: framework.ErrNoPicker,
		},
		{
			name: "unknown plugin ref",
			yaml: "plugins:\n- type: max-score-picker\n  name: a" + handler + "profiles:\n- name: default\n  plugins:\n  - pluginRef: a\n  - pluginRef: ghost" + tail,
			want: config.ErrUnknownRef,
		},
		{
			name: "unknown handler ref",
			yaml: "plugins:\n- type: max-score-picker\n  name: a\nprofiles:\n- name: default\n  plugins:\n  - pluginRef: a\nprofileHandler:\n  pluginRef: ghost\n",
			want: config.ErrUnknownRef,
		},
		{
			name: "handler ref that is not a handler",
			yaml: "plugins:\n- type: max-score-picker\n  name: a\nprofiles:\n- name: default\n  plugins:\n  - pluginRef: a\nprofileHandler:\n  pluginRef: a\n",
			want: config.ErrNotProfileHandler,
		},
		{
			name: "handler runs an undeclared profile",
			yaml: "plugins:\n- type: max-score-picker\n  name: a" + handler + "profiles:\n- name: other\n  plugins:\n  - pluginRef: a" + tail,
			want: config.ErrUnknownRef,
		},
		{
			name: "profile the handler never runs",
			yaml: "plugins:\n- type: max-score-picker\n  name: a" + handler + "profiles:\n- name: default\n  plugins:\n  - pluginRef: a\n- name: extra\n  plugins:\n  - pluginRef: a" + tail,
			want: config.ErrInvalid,
		},
		{
			name: "weight on a filter",
			yaml: "plugins:\n- type: ready-filter\n  name: ready\n- type: max-score-picker\n  name: a" + handler + "profiles:\n- name: default\n  plugins:\n  - pluginRef: ready\n    weight: 2\n  - pluginRef: a" + tail,
			want: config.ErrInvalid,
		},
		{
			name: "negative weight",
			yaml: "plugins:\n- type: queue-scorer\n  name: q\n- type: max-score-picker\n  name: a" + handler + "profiles:\n- name: default\n  plugins:\n  - pluginRef: q\n    weight: -1\n  - pluginRef: a" + tail,
			want: config.ErrInvalid,
		},
		{
			name: "plugin used by nothing",
			yaml: "plugins:\n- type: queue-scorer\n  name: q\n- type: max-score-picker\n  name: a" + handler + "profiles:\n- name: default\n  plugins:\n  - pluginRef: a" + tail,
			want: config.ErrInvalid,
		},
		{
			name: "profile using a lifecycle only plugin",
			yaml: "plugins:\n- type: assumed-load\n  name: al\n- type: max-score-picker\n  name: a" + handler + "profiles:\n- name: default\n  plugins:\n  - pluginRef: al\n  - pluginRef: a" + tail,
			want: config.ErrInvalid,
		},
		{
			name: "duplicate plugin names",
			yaml: "plugins:\n- type: max-score-picker\n  name: a\n- type: random-picker\n  name: a" + handler + "profiles:\n- name: default\n  plugins:\n  - pluginRef: a" + tail,
			want: config.ErrInvalid,
		},
		{
			name: "unknown parameter",
			yaml: "plugins:\n- type: max-score-picker\n  name: a\n  parameters:\n    cuont: 2" + handler + "profiles:\n- name: default\n  plugins:\n  - pluginRef: a" + tail,
			want: registry.ErrInvalidParams,
		},
		{
			name: "unknown plugin type",
			yaml: "plugins:\n- type: psychic-picker\n  name: a" + handler + "profiles:\n- name: default\n  plugins:\n  - pluginRef: a" + tail,
			want: registry.ErrUnknownType,
		},
		{
			name: "unknown top level field",
			yaml: "plugin:\n- type: max-score-picker\n",
			want: config.ErrInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := config.Load([]byte(tc.yaml), deps(schedtest.NewClock()))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPluginJoinsEveryExtensionPoint(t *testing.T) {
	cfg := &config.Config{
		Plugins: []config.PluginSpec{
			{Type: "max-score-picker", Name: "picker"},
			{Type: "prefix-cache-scorer"},
			{Type: "single-profile-handler", Name: "h", Parameters: map[string]any{"profile": "p"}},
		},
		Profiles:       []config.ProfileSpec{{Name: "p", Plugins: []config.PluginRef{{PluginRef: "prefix-cache-scorer"}, {PluginRef: "picker"}}}},
		ProfileHandler: config.HandlerRef{PluginRef: "h"},
	}
	s, lifecycle, err := cfg.Build(deps(schedtest.NewClock()))
	if err != nil {
		t.Fatal(err)
	}
	p := s.Profiles()["p"]
	if len(p.Scorers) != 1 || p.Scorers[0].Weight != 1 {
		t.Fatalf("default weight not one: %+v", p.Scorers)
	}
	if len(lifecycle) != 1 || !strings.HasPrefix(lifecycle[0].TypedName().String(), "prefix-cache-scorer") {
		t.Fatalf("lifecycle %v", names(lifecycle))
	}
	if _, ok := lifecycle[0].(framework.PreRequestPlugin); !ok {
		t.Fatal("lifecycle plugin lost its hook")
	}
}

func TestRemoteSpec(t *testing.T) {
	base := config.DefaultNoMetricsConfig
	cases := []struct {
		name, remote string
		ok           bool
	}{
		{"absent", "", true},
		{"http", "remote:\n  url: http://sched:9000\n  timeout: 50ms\n", true},
		{"https without a timeout", "remote:\n  url: https://sched.example\n", true},
		{"relative", "remote:\n  url: /schedule\n", false},
		{"scheme", "remote:\n  url: ftp://sched\n", false},
		{"negative", "remote:\n  url: http://sched\n  timeout: -1s\n", false},
		{"unknown field", "remote:\n  url: http://sched\n  retries: 2\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := config.Parse([]byte(base + tc.remote))
			if err == nil {
				err = config.Validate([]byte(base + tc.remote))
			}
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok %v", err, tc.ok)
			}
			if tc.ok && tc.remote != "" && c.Remote.URL == "" {
				t.Fatal("remote url was not read")
			}
		})
	}
	t.Run("a zero timeout builds with the default", func(t *testing.T) {
		c, err := config.Parse([]byte(base + "remote:\n  url: https://sched.example\n"))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := c.Build(deps(schedtest.NewClock())); err != nil {
			t.Fatal(err)
		}
		if time.Duration(c.Remote.Timeout) != config.DefaultRemoteTimeout {
			t.Fatalf("timeout %v after build, want %v", time.Duration(c.Remote.Timeout), config.DefaultRemoteTimeout)
		}
	})
}
