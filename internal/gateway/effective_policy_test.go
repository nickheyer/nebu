package gateway

import (
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// Every unset route policy field, a zero or a negative one, takes the gateway default
func TestEffectivePolicyInheritsUnsetFields(t *testing.T) {
	def := &v1.Policy{MaxInFlight: 8, RequestsPerSecond: 4, Burst: 2, RequestTimeoutMs: 30000, UpstreamTimeoutMs: 60000}
	cases := []struct {
		name  string
		route *v1.Policy
		want  *v1.Policy
	}{
		{"nil route policy", nil, def},
		{"empty route policy", &v1.Policy{}, def},
		{"negative rate inherits", &v1.Policy{RequestsPerSecond: -1}, def},
		{"every field set", &v1.Policy{MaxInFlight: 1, RequestsPerSecond: 0.5, Burst: 1, RequestTimeoutMs: 10, UpstreamTimeoutMs: 20}, &v1.Policy{MaxInFlight: 1, RequestsPerSecond: 0.5, Burst: 1, RequestTimeoutMs: 10, UpstreamTimeoutMs: 20}},
		{"some fields set", &v1.Policy{MaxInFlight: 3, UpstreamTimeoutMs: 7}, &v1.Policy{MaxInFlight: 3, RequestsPerSecond: 4, Burst: 2, RequestTimeoutMs: 30000, UpstreamTimeoutMs: 7}},
		{"negative default rate counts as unset", &v1.Policy{RequestsPerSecond: -2}, def},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effectivePolicy(c.route, def); !proto.Equal(got, c.want) {
				t.Fatalf("effectivePolicy = %v, want %v", got, c.want)
			}
		})
	}
	if got := effectivePolicy(&v1.Policy{RequestsPerSecond: -1}, &v1.Policy{RequestsPerSecond: -3}); got.GetRequestsPerSecond() != 0 {
		t.Fatalf("negative rates on both sides gave %g, want zero meaning unlimited", got.GetRequestsPerSecond())
	}
}
