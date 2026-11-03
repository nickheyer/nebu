package formations

import (
	"context"
	"slices"
	"testing"

	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Replica seats of two ranks publish one endpoint per rank numbered across replicas, a dead rank leaving alone
func TestReplicaRanksPublishEachRank(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newReplicasNode(t, ctx)
	plan := n.m.planner
	n.m.planner = func(ctx context.Context, run *v1.RunRequest) (*planned, error) {
		p, err := plan(ctx, run)
		if err == nil {
			p.params = map[string]string{fake.ParamRanks: "2"}
		}
		return p, err
	}
	f := n.run(t, ctx, "dp")
	owner := pools.OwnerRef{Kind: pools.OwnerFormation, ID: f.GetId()}
	eventually(t, "every rank of both replicas published", func() bool {
		return slices.Equal(ranks(n.reg.Endpoints(owner)), []uint32{0, 1, 2, 3})
	})
	ports := map[int]bool{}
	byRank := map[uint32]pools.Published{}
	for _, ep := range n.reg.Endpoints(owner) {
		if ports[ep.Address.Port] || ep.HealthPort != uint32(ep.Address.Port) || ep.MetricsPort != uint32(ep.Address.Port) || !ep.Ready || ep.Labels[datalayer.LabelFormation] != f.GetId() {
			t.Fatalf("endpoint %+v", ep)
		}
		ports[ep.Address.Port] = true
		byRank[ep.Rank] = ep
	}
	eventually(t, "the seat records list their ranks", func() bool {
		cur, err := n.m.Get(f.GetId())
		if err != nil {
			return false
		}
		for _, s := range cur.GetSeats() {
			if len(s.GetRanks()) != 2 {
				return false
			}
		}
		return true
	})
	second := byRank[3].InstanceID
	if byRank[2].InstanceID != second || byRank[0].InstanceID == second {
		t.Fatalf("ranks 2 and 3 belong to replica 1: %+v", byRank)
	}
	n.launcher.SetHealthy(fake.RankName("dp/replica1", 1), false)
	eventually(t, "rank 3 out of traffic alone", func() bool {
		return !n.reg.RankReady(second, 3) && n.reg.RankReady(second, 2) && n.reg.RankReady(byRank[0].InstanceID, 0) && n.reg.RankReady(byRank[1].InstanceID, 1)
	})
	eventually(t, "the seat record shows the dead rank", func() bool {
		cur, err := n.m.Get(f.GetId())
		if err != nil {
			return false
		}
		for _, s := range cur.GetSeats() {
			if s.GetInstanceId() == second {
				return len(s.GetRanks()) == 2 && !s.GetRanks()[1].GetHealthy() && s.GetRanks()[0].GetHealthy()
			}
		}
		return false
	})
	n.launcher.SetHealthy(fake.RankName("dp/replica1", 1), true)
	eventually(t, "rank 3 back in traffic", func() bool { return n.reg.RankReady(second, 3) })
	cur, _ := n.m.Get(f.GetId())
	if cur.GetState() != v1.FormationState_FORMATION_STATE_READY {
		t.Fatalf("an unhealthy rank does not degrade the formation: %s", cur.GetState())
	}
}
