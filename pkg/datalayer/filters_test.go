package datalayer

import (
	"testing"
	"time"
)

func TestFilters(t *testing.T) {
	now := time.Unix(1000, 0)
	s := NewStore()
	s.UpsertEndpoint(spec("fresh", "p", map[string]string{LabelRole: RolePrefill, "zone": "a"}))
	s.UpsertEndpoint(spec("stale", "p", map[string]string{LabelRole: RoleDecode}))
	s.UpsertEndpoint(spec("bare", "p", nil))
	s.UpsertEndpoint(spec("both", "p", map[string]string{LabelRole: RoleBoth, LabelAdapter: "pinned"}))
	s.SetAttribute("fresh", CollectorMetrics, &ServerMetrics{UpdatedAt: now.Add(-50 * time.Millisecond), RunningAdapters: map[string]struct{}{"running": {}}})
	s.SetAttribute("stale", CollectorMetrics, &ServerMetrics{UpdatedAt: now.Add(-time.Second)})
	s.SetAttribute("stale", CollectorCatalog, &Catalog{Adapters: []string{"listed"}})

	tests := []struct {
		name string
		keep func(Endpoint) bool
		want []EndpointID
	}{
		{"fresh metrics", FreshMetrics(now, 200*time.Millisecond), []EndpointID{"fresh"}},
		{"wide threshold", FreshMetrics(now, 2*time.Second), []EndpointID{"fresh", "stale"}},
		{"adapter running", HasAdapter("running"), []EndpointID{"fresh"}},
		{"adapter in catalog", HasAdapter("listed"), []EndpointID{"stale"}},
		{"adapter by label", HasAdapter("pinned"), []EndpointID{"both"}},
		{"empty adapter", HasAdapter(""), []EndpointID{}},
		{"prefill role", InRole(RolePrefill), []EndpointID{"bare", "both", "fresh"}},
		{"decode role", InRole(RoleDecode), []EndpointID{"bare", "both", "stale"}},
		{"both role", InRole(RoleBoth), []EndpointID{"bare", "both"}},
		{"label", WithLabel("zone", "a"), []EndpointID{"fresh"}},
		{"label value differs", WithLabel("zone", "b"), []EndpointID{}},
		{"all", All(InRole(RolePrefill), FreshMetrics(now, time.Second)), []EndpointID{"fresh"}},
		{"all of none", All(), []EndpointID{"bare", "both", "fresh", "stale"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(s.Endpoints("p", tc.keep))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestReadyAndHealthy(t *testing.T) {
	tests := []struct {
		name   string
		ready  bool
		health *HealthState
		want   bool
	}{
		{"ready unprobed", true, nil, true},
		{"ready healthy", true, &HealthState{Healthy: true}, true},
		{"ready unhealthy", true, &HealthState{Healthy: false}, false},
		{"not ready", false, &HealthState{Healthy: true}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore()
			sp := spec("e", "p", nil)
			sp.Ready = tc.ready
			s.UpsertEndpoint(sp)
			if tc.health != nil {
				s.SetAttribute("e", CollectorHealth, tc.health)
			}
			ep, _ := s.Endpoint("e")
			if got := ReadyAndHealthy(ep); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
