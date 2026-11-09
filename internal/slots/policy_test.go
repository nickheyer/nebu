package slots

import (
	"context"
	"testing"

	"github.com/nickheyer/nebu/internal/pools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// Every unset alias policy field, a zero or a negative rate included, inherits the slot's
func TestMergePolicyInheritsUnsetFields(t *testing.T) {
	slot := &v1.Policy{MaxInFlight: 8, RequestsPerSecond: 5, Burst: 2, RequestTimeoutMs: 300, UpstreamTimeoutMs: 600}
	cases := []struct {
		name  string
		alias *v1.Policy
		want  *v1.Policy
	}{
		{"nil alias", nil, slot},
		{"empty alias", &v1.Policy{}, slot},
		{"negative alias rate inherits", &v1.Policy{RequestsPerSecond: -1}, slot},
		{"alias rate wins", &v1.Policy{RequestsPerSecond: 2}, &v1.Policy{MaxInFlight: 8, RequestsPerSecond: 2, Burst: 2, RequestTimeoutMs: 300, UpstreamTimeoutMs: 600}},
		{"every alias field wins", &v1.Policy{MaxInFlight: 1, RequestsPerSecond: 1, Burst: 1, RequestTimeoutMs: 1, UpstreamTimeoutMs: 1}, &v1.Policy{MaxInFlight: 1, RequestsPerSecond: 1, Burst: 1, RequestTimeoutMs: 1, UpstreamTimeoutMs: 1}},
		{"some alias fields win", &v1.Policy{Burst: 9, UpstreamTimeoutMs: 7}, &v1.Policy{MaxInFlight: 8, RequestsPerSecond: 5, Burst: 9, RequestTimeoutMs: 300, UpstreamTimeoutMs: 7}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mergePolicy(c.alias, slot); !proto.Equal(got, c.want) {
				t.Fatalf("mergePolicy = %v, want %v", got, c.want)
			}
		})
	}
	if got := mergePolicy(&v1.Policy{RequestsPerSecond: -1}, &v1.Policy{RequestsPerSecond: -4}); got.GetRequestsPerSecond() != -4 {
		t.Fatalf("a negative alias rate over a negative slot rate gave %g, want the slot's for the gateway to treat as unset", got.GetRequestsPerSecond())
	}
}

// A user policy over a slot alias route outlives a restart through the real pool manager, adopted or relaunched
func TestRecoverKeepsAliasRoutePolicy(t *testing.T) {
	cases := []struct {
		name string
		// Whether the occupant's record is gone at the restart, so the slot launches another
		lost bool
	}{
		{"occupant adopted", false},
		{"occupant relaunched", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n := newSlotNode(t, ctx)
			pm := n.realPools(t, ctx)
			s := n.filled(t, ctx)
			r, ok := pm.Route("chat")
			if !ok || r.GetOwnerKind() != pools.OwnerSlot || r.GetOwnerId() != s.GetId() {
				t.Fatalf("alias route %v %v", r, ok)
			}
			if _, err := pm.PutRoute(ctx, "chat", &v1.RouteSpec{Rules: r.GetSpec().GetRules(), Policy: &v1.Policy{MaxInFlight: 9}}); err != nil {
				t.Fatal(err)
			}
			first := n.occupant(t, s.GetId())
			n.instances.Close()
			if c.lost {
				if err := n.db.DeleteInstance(ctx, first.GetId()); err != nil {
					t.Fatal(err)
				}
			}
			if err := pm.Close(); err != nil {
				t.Fatal(err)
			}
			n.start(t, ctx)
			pm = n.realPools(t, ctx)
			if err := n.instances.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			if err := n.slots.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the slot serving again", func() bool {
				cur := n.slot(t, s.GetId())
				return cur.GetInstanceId() != "" && (cur.GetInstanceId() != first.GetId()) == c.lost && cur.GetState() == v1.SlotState_SLOT_STATE_READY
			})
			r, ok = pm.Route("chat")
			if !ok || r.GetOwnerKind() != pools.OwnerSlot || r.GetSpec().GetPolicy().GetMaxInFlight() != 9 {
				t.Fatalf("alias route after the restart %v %v", r, ok)
			}
			if rows, err := n.db.ListRouteOverrides(ctx); err != nil || len(rows) != 1 || rows[0].GetName() != "chat" {
				t.Fatalf("override rows after the restart %v %v", rows, err)
			}
			if r, _ := pm.Route("main"); r.GetSpec().GetPolicy().GetMaxInFlight() != 0 {
				t.Fatalf("the slot's own route took the alias override %v", r.GetSpec())
			}
		})
	}
}
