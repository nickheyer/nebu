package formations

import (
	"slices"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The route spec a formation names carries its policy, profile, shape and the instance ids of launched seats in order
func TestRouteSpecCarriesShapeAndSeats(t *testing.T) {
	policy := &v1.Policy{MaxInFlight: 3}
	profile := &v1.Profile{ToolServers: []string{"search"}}
	tests := []struct {
		name      string
		f         *v1.Formation
		wantSeats []string
	}{
		{name: "no seats launched", f: &v1.Formation{Shape: v1.Shape_SHAPE_RELAY, Request: &v1.RunRequest{Policy: policy, RouteProfile: profile}, Seats: []*v1.Seat{{Role: "prefill"}, {Role: "decode"}}}},
		{name: "launched seats in order", f: &v1.Formation{Shape: v1.Shape_SHAPE_REPLICAS, Request: &v1.RunRequest{Policy: policy, RouteProfile: profile}, Seats: []*v1.Seat{{Role: "replica", Rank: 0, InstanceId: "b"}, {Role: "replica", Rank: 1}, {Role: "replica", Rank: 2, InstanceId: "a"}}}, wantSeats: []string{"b", "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := routeSpec(tt.f)
			if spec.Shape != tt.f.GetShape() || !slices.Equal(spec.Seats, tt.wantSeats) || spec.Policy != policy || spec.Profile != profile {
				t.Fatalf("spec %+v, want shape %v seats %v", spec, tt.f.GetShape(), tt.wantSeats)
			}
		})
	}
}
