package mesh

import (
	"context"
	"math/bits"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/config"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Members one round syncs with, ceil(log2 N) plus one, never more than the other members
func fanout(n int) int {
	if n <= 1 {
		return 0
	}
	f := bits.Len(uint(n-1)) + 1
	return min(f, n-1)
}

// Rounds in which a node reaches every other member once, the bound every pair meets within
func roundsPerEpoch(n int) int {
	f := fanout(n)
	if f == 0 {
		return 0
	}
	return (n - 1 + f - 1) / f
}

// The members a node syncs with in a round, a block of a per epoch shuffle of ring offsets
func targets(self string, others []string, round uint64) []string {
	ring := append([]string{self}, others...)
	sort.Strings(ring)
	n := len(ring)
	f := fanout(n)
	if f == 0 {
		return nil
	}
	at := sort.SearchStrings(ring, self)
	per := uint64(roundsPerEpoch(n))
	epoch, slot := round/per, int(round%per)
	offsets := make([]int, n-1)
	for i := range offsets {
		offsets[i] = i + 1
	}
	shuffle := rand.New(rand.NewPCG(epoch, uint64(n)))
	shuffle.Shuffle(len(offsets), func(i, j int) { offsets[i], offsets[j] = offsets[j], offsets[i] })
	out := make([]string, 0, f)
	for k := range f {
		out = append(out, ring[(at+offsets[(slot*f+k)%(n-1)])%n])
	}
	return out
}

// The members this round syncs with, the hubs alone for a spoke and a block of the ring shuffle otherwise
func (m *Manager) roundTargets(others, hubs []string, round uint64) []string {
	if m.topology() != config.MeshTopologySpoke {
		return targets(m.identity.ID, others, round)
	}
	var out []string
	for _, id := range others {
		if slices.Contains(hubs, id) {
			out = append(out, id)
		}
	}
	return out
}

// The configured topology, full when the setting names none
func (m *Manager) topology() string {
	switch strings.ToLower(strings.TrimSpace(m.Config.GetTopology())) {
	case config.MeshTopologyHub:
		return config.MeshTopologyHub
	case config.MeshTopologySpoke:
		return config.MeshTopologySpoke
	}
	return config.MeshTopologyFull
}

// The configured sync exchange, push and pull when the setting names none
func (m *Manager) syncMode() string {
	switch strings.ToLower(strings.TrimSpace(m.Config.GetSync())) {
	case config.MeshSyncPush:
		return config.MeshSyncPush
	case config.MeshSyncPull:
		return config.MeshSyncPull
	}
	return config.MeshSyncPushPull
}

// The member ids mesh.hubs names, by id or by name
func (m *Manager) hubsLocked() []string {
	var out []string
	for _, ref := range m.Config.GetHubs() {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := m.members[ref]; ok {
			out = append(out, ref)
			continue
		}
		for id, mb := range m.members {
			if mb.rec.GetName() == ref {
				out = append(out, id)
			}
		}
	}
	return out
}

// Runs a gossip round every interval and an immediate push whenever the record changes
func (m *Manager) gossipLoop(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	m.round(ctx, true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.round(ctx, true)
		case <-m.wake:
			m.round(ctx, false)
		}
	}
}

// Syncs with this round's fan-out, counting the heartbeat, liveness, conditions and renewal on a tick
func (m *Manager) round(ctx context.Context, tick bool) {
	if !m.Joined() {
		return
	}
	now := time.Now()
	m.mu.Lock()
	if tick {
		m.heartbeat++
	}
	others := make([]string, 0, len(m.members))
	for id := range m.members {
		others = append(others, id)
	}
	hubs := m.hubsLocked()
	m.mu.Unlock()
	var due []string
	for _, id := range m.roundTargets(others, hubs, uint64(now.UnixNano()/int64(m.interval))) {
		m.mu.Lock()
		mb := m.members[id]
		skip := mb == nil || mb.rec.GetState() == v1.NodeState_NODE_STATE_GONE && now.Sub(mb.lastTried) < goneRetry
		m.mu.Unlock()
		if !skip {
			due = append(due, id)
		}
	}
	var wg sync.WaitGroup
	for _, id := range due {
		wg.Go(func() { m.syncWith(ctx, id) })
	}
	wg.Wait()
	if tick {
		m.evaluate(time.Now())
		m.pruneRevocations(time.Now())
		m.renewIfDue(ctx)
	}
}

// Drops revocations of certificates that expired, which no handshake would take anyway
func (m *Manager) pruneRevocations(now time.Time) {
	m.rows.Lock()
	defer m.rows.Unlock()
	m.mu.Lock()
	pruned := 0
	for serial, r := range m.revoked {
		if now.After(r.GetNotAfter().AsTime()) {
			delete(m.revoked, serial)
			pruned++
		}
	}
	m.mu.Unlock()
	if pruned == 0 {
		return
	}
	if err := m.DB.DeleteRevocations(context.Background(), now); err != nil {
		m.Log.Warn("expired revocations not dropped", "err", err)
	}
}

// Moves every member through ready, unreachable and gone by the last time any view saw it, and sets its conditions
func (m *Manager) evaluate(now time.Time) {
	type change struct {
		rec   *v1.Node
		state bool
	}
	var changes []change
	m.mu.Lock()
	for _, mb := range m.members {
		before := mb.rec.GetState()
		since := now.Sub(mb.seenAt)
		switch {
		case since > GoneAfter:
			mb.rec.State = v1.NodeState_NODE_STATE_GONE
		case since > UnreachableAfter*m.interval:
			mb.rec.State = v1.NodeState_NODE_STATE_UNREACHABLE
		default:
			mb.rec.State = v1.NodeState_NODE_STATE_READY
		}
		mb.rec.SeenAt = timestamppb.New(mb.seenAt)
		conds := m.memberConditionsLocked(mb, now)
		if moved := before != mb.rec.GetState(); moved || conds {
			changes = append(changes, change{rec: proto.Clone(mb.rec).(*v1.Node), state: moved})
		}
	}
	m.mu.Unlock()
	for _, c := range changes {
		id := c.rec.GetId()
		m.persist(id)
		m.Events.Publish(v1.EventKind_EVENT_KIND_NODE, v1.EventAction_EVENT_ACTION_UPDATED, id, c.rec)
		if !c.state {
			continue
		}
		m.Log.Warn("mesh member "+stateWord(c.rec.GetState()), "node", id, "name", c.rec.GetName())
		if m.Formations != nil {
			m.Formations.MemberState(id, c.rec.GetState())
		}
		m.importFrom(context.Background(), id)
	}
}

// A state as a log line names it
func stateWord(s v1.NodeState) string {
	switch s {
	case v1.NodeState_NODE_STATE_READY:
		return "ready"
	case v1.NodeState_NODE_STATE_UNREACHABLE:
		return "unreachable"
	case v1.NodeState_NODE_STATE_GONE:
		return "gone"
	}
	return "unknown"
}
