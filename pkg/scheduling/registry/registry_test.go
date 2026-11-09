package registry_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/nickheyer/nebu/pkg/scheduling/internal/schedtest"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/assumed"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/filter"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/picker"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/profile"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/scorer"
	_ "github.com/nickheyer/nebu/pkg/scheduling/plugins/throughput"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

func deps() registry.Deps {
	return registry.Deps{Store: schedtest.NewStore(), Indexer: schedtest.NewIndexer(4), Predictor: &schedtest.Predictor{}, Clock: schedtest.NewClock()}
}

// The smallest valid parameters of every registered type
var valid = map[string]map[string]any{
	"ready-filter":                nil,
	"fresh-metrics-filter":        {"threshold": "2s"},
	"subset-filter":               nil,
	"role-filter":                 {"role": "decode"},
	"kv-threshold-filter":         {"max": 0.9},
	"queue-threshold-filter":      {"max": 8},
	"lora-affinity-filter":        nil,
	"least-queue-filter":          nil,
	"capacity-filter":             nil,
	"objective-filter":            nil,
	"expert-parallel-scorer":      nil,
	"throughput":                  nil,
	"queue-scorer":                nil,
	"kv-cache-utilization-scorer": nil,
	"lora-affinity-scorer":        nil,
	"prefix-cache-scorer":         nil,
	"throughput-scorer":           nil,
	"latency-scorer":              nil,
	"assumed-load-scorer":         nil,
	"session-affinity-scorer":     {"header": "x-session-id"},
	"max-score-picker":            nil,
	"weighted-random-picker":      {"count": 2},
	"random-picker":               nil,
	"single-profile-handler":      {"profile": "default"},
	"shadow-profile-handler":      {"primary": "a", "shadows": []any{"b"}},
	"pd-profile-handler":          {"prefillProfile": "p", "decodeProfile": "d", "threshold": 256},
	"assumed-load":                nil,
}

func TestEveryTypeBuildsAndRefusesUnknownParams(t *testing.T) {
	types := registry.Types()
	if len(types) != len(valid) {
		t.Fatalf("registered %v, test knows %d types", types, len(valid))
	}
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			params, ok := valid[typ]
			if !ok {
				t.Fatalf("no valid parameters listed for %s", typ)
			}
			p, err := registry.New(typ, "", params, deps())
			if err != nil {
				t.Fatal(err)
			}
			if n := p.TypedName(); n.Type != typ || n.Name != typ {
				t.Fatalf("name %+v", n)
			}
			p, err = registry.New(typ, "mine", params, deps())
			if err != nil || p.TypedName().Name != "mine" || p.TypedName().Type != typ {
				t.Fatalf("named build %v %v", p, err)
			}
			bad := map[string]any{"bogus": 1}
			for k, v := range params {
				bad[k] = v
			}
			if _, err := registry.New(typ, "x", bad, deps()); !errors.Is(err, registry.ErrInvalidParams) {
				t.Fatalf("unknown param accepted, err %v", err)
			}
		})
	}
}

func TestParamValidation(t *testing.T) {
	full := deps()
	tests := []struct {
		name   string
		typ    string
		params map[string]any
		deps   registry.Deps
		want   error
	}{
		{"unknown type", "nope", nil, full, registry.ErrUnknownType},
		{"fresh needs a threshold", "fresh-metrics-filter", nil, full, registry.ErrInvalidParams},
		{"fresh threshold must parse", "fresh-metrics-filter", map[string]any{"threshold": "soon"}, full, registry.ErrInvalidParams},
		{"fresh threshold must be a string", "fresh-metrics-filter", map[string]any{"threshold": 5}, full, registry.ErrInvalidParams},
		{"fresh threshold must be positive", "fresh-metrics-filter", map[string]any{"threshold": "0s"}, full, registry.ErrInvalidParams},
		{"role must be known", "role-filter", map[string]any{"role": "chef"}, full, registry.ErrInvalidParams},
		{"role is required", "role-filter", nil, full, registry.ErrInvalidParams},
		{"kv max above one", "kv-threshold-filter", map[string]any{"max": 1.5}, full, registry.ErrInvalidParams},
		{"kv max required", "kv-threshold-filter", nil, full, registry.ErrInvalidParams},
		{"queue max negative", "queue-threshold-filter", map[string]any{"max": -1}, full, registry.ErrInvalidParams},
		{"queue max wrong type", "queue-threshold-filter", map[string]any{"max": "many"}, full, registry.ErrInvalidParams},
		{"margin negative", "least-queue-filter", map[string]any{"margin": -2}, full, registry.ErrInvalidParams},
		{"picker count zero", "max-score-picker", map[string]any{"count": 0}, full, registry.ErrInvalidParams},
		{"session header required", "session-affinity-scorer", nil, full, registry.ErrInvalidParams},
		{"session limit positive", "session-affinity-scorer", map[string]any{"header": "h", "maxSessions": 0}, full, registry.ErrInvalidParams},
		{"latency ttl positive", "latency-scorer", map[string]any{"requestTTL": "-1s"}, full, registry.ErrInvalidParams},
		{"single needs a profile", "single-profile-handler", nil, full, registry.ErrInvalidParams},
		{"shadow needs shadows", "shadow-profile-handler", map[string]any{"primary": "a"}, full, registry.ErrInvalidParams},
		{"shadow cannot shadow the primary", "shadow-profile-handler", map[string]any{"primary": "a", "shadows": []any{"a"}}, full, registry.ErrInvalidParams},
		{"shadow names distinct", "shadow-profile-handler", map[string]any{"primary": "a", "shadows": []any{"b", "b"}}, full, registry.ErrInvalidParams},
		{"pd profiles distinct", "pd-profile-handler", map[string]any{"prefillProfile": "x", "decodeProfile": "x", "threshold": 1}, full, registry.ErrInvalidParams},
		{"pd threshold required", "pd-profile-handler", map[string]any{"prefillProfile": "p", "decodeProfile": "d"}, full, registry.ErrInvalidParams},
		{"pd selector known", "pd-profile-handler", map[string]any{"prefillProfile": "p", "decodeProfile": "d", "threshold": 1, "decodeSelector": "vibes"}, full, registry.ErrInvalidParams},
		{"pd uncached selector needs the indexer", "pd-profile-handler", map[string]any{"prefillProfile": "p", "decodeProfile": "d", "threshold": 1, "decodeSelector": "uncached-tokens"}, registry.Deps{}, registry.ErrMissingDependency},
		{"prefix needs the indexer", "prefix-cache-scorer", nil, registry.Deps{}, registry.ErrMissingDependency},
		{"latency needs the predictor", "latency-scorer", nil, registry.Deps{Indexer: full.Indexer}, registry.ErrMissingDependency},
		{"latency needs the indexer", "latency-scorer", nil, registry.Deps{Predictor: full.Predictor}, registry.ErrMissingDependency},
		{"assumed load needs the store", "assumed-load", nil, registry.Deps{}, registry.ErrMissingDependency},
		{"pd uncached selector builds with the indexer", "pd-profile-handler", map[string]any{"prefillProfile": "p", "decodeProfile": "d", "threshold": 1, "decodeSelector": "uncached-tokens"}, full, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := registry.New(tc.typ, "x", tc.params, tc.deps)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRegisterTwicePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("second registration accepted")
		}
	}()
	registry.Register("ready-filter", nil)
}

func TestTypesSorted(t *testing.T) {
	if types := registry.Types(); !slices.IsSorted(types) {
		t.Fatalf("types %v", types)
	}
}
