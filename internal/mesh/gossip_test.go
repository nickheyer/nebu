package mesh

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Each round reaches ceil(log2 N) plus one members and every pair meets within one epoch
func TestTargetsMeetEveryPairWithinBound(t *testing.T) {
	for n := 2; n <= 70; n++ {
		ids := make([]string, n)
		for i := range ids {
			ids[i] = fmt.Sprintf("node-%03d", n-i)
		}
		want := min(int(math.Ceil(math.Log2(float64(n))))+1, n-1)
		if fanout(n) != want {
			t.Fatalf("fan-out of %d is %d, want %d", n, fanout(n), want)
		}
		per := uint64(roundsPerEpoch(n))
		for _, start := range []uint64{0, per * 7, per * 1234567} {
			met := map[[2]string]bool{}
			for r := start; r < start+per; r++ {
				for i, self := range ids {
					others := append(append([]string{}, ids[:i]...), ids[i+1:]...)
					got := targets(self, others, r)
					if len(got) != want {
						t.Fatalf("n %d round %d sends to %d members, want %d", n, r, len(got), want)
					}
					for _, other := range got {
						if other == self {
							t.Fatalf("n %d: a node never syncs with itself", n)
						}
						met[[2]string{min(self, other), max(self, other)}] = true
					}
				}
			}
			if pairs := n * (n - 1) / 2; len(met) != pairs {
				t.Fatalf("n %d from round %d: %d of %d pairs met within %d rounds", n, start, len(met), pairs, per)
			}
		}
	}
}

// Over real listeners every pair syncs within the bound, liveness follows the rounds, and the conditions say so
func TestGossipRoundsOverListeners(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interval := 250 * time.Millisecond
	var nodes []*testNode
	for range 6 {
		nodes = append(nodes, newNode(t, ctx, interval))
	}
	formMesh(t, ctx, nodes...)
	eventually(t, "gossip introduces every member to every other", 30*time.Second, func() bool { return allMet(nodes...) })
	settle(t, nodes...)
	start := time.Now()
	for _, n := range nodes {
		n.mu.Lock()
		for _, mb := range n.members {
			mb.direct = time.Time{}
		}
		n.mu.Unlock()
	}
	bound := time.Duration(2*roundsPerEpoch(len(nodes))+1) * interval
	time.Sleep(bound)
	for _, n := range nodes {
		n.mu.Lock()
		for id, mb := range n.members {
			if mb.direct.Before(start) {
				n.mu.Unlock()
				t.Fatalf("%s and %s did not sync within %s", n.Self()[:6], id[:6], bound)
			}
		}
		n.mu.Unlock()
	}
	for _, n := range nodes {
		for _, rec := range n.Nodes()[1:] {
			for _, typ := range []string{ConditionReachable, ConditionSynced, ConditionCertificateValid} {
				if !conditionIs(rec, typ, v1.ConditionStatus_CONDITION_STATUS_TRUE) {
					t.Fatalf("%s sees %s with %s not true: %v", n.Self()[:6], rec.GetId()[:6], typ, rec.GetConditions())
				}
			}
		}
		self := n.Record()
		if !conditionIs(self, ConditionReachable, v1.ConditionStatus_CONDITION_STATUS_TRUE) || !conditionIs(self, ConditionCertificateValid, v1.ConditionStatus_CONDITION_STATUS_TRUE) {
			t.Fatalf("a node's own conditions: %v", self.GetConditions())
		}
	}
	// A closed member goes unseen in every view and turns unreachable after three rounds
	gone := nodes[len(nodes)-1]
	gone.Close()
	for _, n := range nodes[:len(nodes)-1] {
		eventually(t, "the closed member is unreachable", 10*time.Second, func() bool {
			rec, ok := n.Member(gone.Self())
			return ok && rec.GetState() == v1.NodeState_NODE_STATE_UNREACHABLE && conditionIs(rec, ConditionReachable, v1.ConditionStatus_CONDITION_STATUS_FALSE)
		})
	}
	// A member still running is never taken for unreachable while the others hear it
	for _, n := range nodes[:len(nodes)-1] {
		for _, other := range nodes[:len(nodes)-1] {
			if rec, ok := n.Member(other.Self()); n != other && (!ok || rec.GetState() != v1.NodeState_NODE_STATE_READY) {
				t.Fatalf("%s sees running %s as %v", n.Self()[:6], other.Self()[:6], rec.GetState())
			}
		}
	}
}

// A push only node never takes records from answers and a pull only node never sends its own, so each learns what the exchange allows
func TestSyncModes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pusher := newNode(t, ctx, 200*time.Millisecond, func(m *Manager) { m.Config.Sync = "push" })
	puller := newNode(t, ctx, 200*time.Millisecond, func(m *Manager) { m.Config.Sync = "pull" })
	formMesh(t, ctx, pusher, puller)
	eventually(t, "the pusher's record reaches the puller", 10*time.Second, func() bool { return met(puller.Manager, pusher.Self()) })
	pusher.pools.set([]*v1.Pool{testPool(pusher.Self(), "127.0.0.1:9300", true)}, nil)
	pusher.Bump()
	eventually(t, "the puller takes the pusher's new pool", 10*time.Second, func() bool {
		rec, ok := puller.Member(pusher.Self())
		return ok && len(rec.GetPools()) == 1
	})
	puller.pools.set([]*v1.Pool{testPool(puller.Self(), "127.0.0.1:9301", true)}, nil)
	puller.Bump()
	time.Sleep(6 * 200 * time.Millisecond)
	if rec, ok := pusher.Member(puller.Self()); !ok || len(rec.GetPools()) != 0 || rec.GetState() != v1.NodeState_NODE_STATE_READY {
		t.Fatalf("a push only node takes no record from a pull only one, which sends none, yet counts it live: %v %v", rec, ok)
	}
	if pub, _ := pusher.pools.snapshot(); len(pub) != 0 {
		t.Fatalf("the pusher imported from the puller: %v", pub)
	}
	if pub, _ := puller.pools.snapshot(); len(pub) != 1 {
		t.Fatalf("the puller imports from the pusher: %v", pub)
	}
}

// Spokes sync with the hubs alone, so exports flow spoke to hub and hub to spoke and never spoke to spoke
func TestHubAndSpoke(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interval := 200 * time.Millisecond
	hub := newNode(t, ctx, interval, func(m *Manager) { m.Config.Topology = "hub" })
	spoke := func() *testNode {
		return newNode(t, ctx, interval, func(m *Manager) {
			m.Config.Topology = "spoke"
			m.Config.Hubs = []string{hub.Self()}
		})
	}
	s1, s2 := spoke(), spoke()
	formMesh(t, ctx, hub, s1, s2)
	eventually(t, "the hub meets every spoke", 10*time.Second, func() bool {
		return met(hub.Manager, s1.Self()) && met(hub.Manager, s2.Self()) && met(s1.Manager, hub.Self()) && met(s2.Manager, hub.Self())
	})
	s1.pools.set([]*v1.Pool{testPool(s1.Self(), "127.0.0.1:9310", true)}, nil)
	s1.Bump()
	hub.pools.set([]*v1.Pool{testPool(hub.Self(), "127.0.0.1:9311", true)}, nil)
	hub.Bump()
	eventually(t, "the hub imports the spoke's pool and the spokes the hub's", 10*time.Second, func() bool {
		pub, _ := hub.pools.snapshot()
		one, _ := s1.pools.snapshot()
		two, _ := s2.pools.snapshot()
		return len(pub) == 1 && len(one) == 1 && len(two) == 1
	})
	time.Sleep(8 * interval)
	if rec, ok := s2.Member(s1.Self()); !ok || len(rec.GetPools()) != 0 {
		t.Fatalf("a spoke never holds another spoke's record: %v %v", rec, ok)
	}
	if two, _ := s2.pools.snapshot(); len(two) != 1 {
		t.Fatalf("a spoke imports from the hub alone: %v", two)
	}
}
