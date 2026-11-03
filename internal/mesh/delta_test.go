package mesh

import (
	"context"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The parts a record copy lists, in order
func listed(n *v1.Node) []v1.RecordPart {
	var out []v1.RecordPart
	for _, p := range n.GetParts() {
		out = append(out, p.GetPart())
	}
	return out
}

// A sync answers the parts changed since the caller's base alone, and every part when the base is unknown
func TestDeltaSyncAnswersChangedParts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	formMesh(t, ctx, a, b)
	eventually(t, "a and b meet", 10*time.Second, func() bool { return allMet(a, b) })
	settle(t, a, b)
	base := a.Record().GetSequence()
	if out, _, _ := a.outgoing(base); len(out.GetParts()) != 0 || out.GetDeltaBase() != base {
		t.Fatalf("nothing changed since the base, so nothing is sent: %v", listed(out))
	}
	a.pools.set([]*v1.Pool{testPool(a.Self(), "127.0.0.1:9300", true)}, nil)
	a.refresh()
	out, _, _ := a.outgoing(base)
	if got := listed(out); len(got) != 1 || got[0] != v1.RecordPart_RECORD_PART_POOLS || len(out.GetPools()) != 1 || out.GetDeltaBase() != base {
		t.Fatalf("one changed part is sent: %v base %d", got, out.GetDeltaBase())
	}
	if len(out.GetPools()[0].GetStatus().GetEndpoints()) != 1 || out.GetPools()[0].GetStatus().GetEndpoints()[0].GetMetrics() != nil {
		t.Fatal("the pool travels with its endpoint and without its counters")
	}
	seq := a.Record().GetSequence()
	for _, unknown := range []uint64{0, a.epoch - 1, seq + 1000} {
		snap, _, _ := a.outgoing(unknown)
		if snap.GetDeltaBase() != 0 || len(snap.GetParts()) != len(partOrder) {
			t.Fatalf("an unknown base %d gets a snapshot: %v", unknown, listed(snap))
		}
	}
	// The sync handler answers the same way to a member's request
	bCaller := caller{kind: peerMember, id: b.Self(), cert: b.currentCreds().leaf}
	resp, err := a.sync(ctx, bCaller, &v1.SyncRequest{Node: &v1.Node{Id: b.Self(), DeltaBase: 1}, DeltaBase: base})
	if err != nil {
		t.Fatal(err)
	}
	if got := listed(resp.GetNode()); len(got) != 1 || got[0] != v1.RecordPart_RECORD_PART_POOLS {
		t.Fatalf("the sync answers the changed part: %v", got)
	}
	if resp.GetSessionToken() == "" || time.Until(resp.GetExpiresAt().AsTime()) > SessionTTL {
		t.Fatalf("the sync renews a 15 minute bearer: %v", resp.GetExpiresAt().AsTime())
	}
	snap, err := a.sync(ctx, bCaller, &v1.SyncRequest{Node: &v1.Node{Id: b.Self(), DeltaBase: 1}, DeltaBase: 0})
	if err != nil {
		t.Fatal(err)
	}
	if snap.GetNode().GetDeltaBase() != 0 || len(snap.GetNode().GetParts()) != len(partOrder) {
		t.Fatalf("an unknown base is answered with a snapshot: %v", listed(snap.GetNode()))
	}
	eventually(t, "b holds a's pool from the delta", 10*time.Second, func() bool {
		rec, _ := b.Member(a.Self())
		return rec.GetSequence() >= seq && len(rec.GetPools()) == 1
	})
}

// A copy applies newer parts, the whole sequence moving only on a snapshot or a held base
func TestApplyRecordHoldsWholeOnKnownBases(t *testing.T) {
	m := bare(t, "self")
	mb := &member{rec: &v1.Node{Id: "x"}, sketch: true}
	profile := func(name string, seq uint64) *v1.Node {
		return &v1.Node{Id: "x", Profile: &v1.HostProfile{Hostname: name}, Parts: []*v1.NodePart{{Part: v1.RecordPart_RECORD_PART_PROFILE, Sequence: seq}}}
	}
	in := profile("one", 5)
	in.Sequence, in.DeltaBase = 5, 3
	held, changed := m.applyRecordLocked(mb, in)
	if held != 0 || len(changed) != 1 || mb.rec.GetProfile().GetHostname() != "one" || !mb.sketch {
		t.Fatalf("a delta on a base not held applies its parts and holds nothing whole: %d %v", held, changed)
	}
	snap := &v1.Node{Id: "x", Sequence: 10, Name: "x", Parts: []*v1.NodePart{{Part: v1.RecordPart_RECORD_PART_IDENTITY, Sequence: 10}, {Part: v1.RecordPart_RECORD_PART_PROFILE, Sequence: 4}}}
	held, _ = m.applyRecordLocked(mb, snap)
	if held != 10 || mb.sketch || mb.rec.GetName() != "x" || mb.rec.GetProfile().GetHostname() != "one" {
		t.Fatalf("a snapshot holds the record whole and keeps a newer part: %d %v", held, mb.rec)
	}
	in = profile("two", 12)
	in.Sequence, in.DeltaBase = 12, 10
	if held, _ = m.applyRecordLocked(mb, in); held != 12 || mb.rec.GetProfile().GetHostname() != "two" {
		t.Fatalf("a delta on the base held moves it: %d", held)
	}
	in = profile("three", 20)
	in.Sequence, in.DeltaBase = 20, 15
	if held, _ = m.applyRecordLocked(mb, in); held != 12 || mb.rec.GetProfile().GetHostname() != "three" {
		t.Fatalf("a delta past the base held applies but holds 12 whole: %d", held)
	}
	stale := profile("old", 11)
	stale.Sequence, stale.DeltaBase = 11, 0
	if _, changed = m.applyRecordLocked(mb, stale); len(changed) != 0 || mb.rec.GetProfile().GetHostname() != "three" {
		t.Fatal("an older part never replaces a newer one")
	}
}

// Revocations of expired certificates are dropped from memory and the record
func TestPruneRevocations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newNode(t, ctx, time.Hour)
	if _, _, err := m.Init(ctx, "prune"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m.revoke(&v1.Revocation{Serial: "aa", NodeId: "x", NotAfter: timestamppb.New(now.Add(time.Minute)), RevokedAt: timestamppb.Now()})
	if _, err := m.revoke(&v1.Revocation{Serial: "bb", NodeId: "y", NotAfter: timestamppb.New(now.Add(time.Hour)), RevokedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	m.pruneRevocations(now.Add(2 * time.Minute))
	if m.isRevoked("aa") || !m.isRevoked("bb") {
		t.Fatal("the expired revocation goes and the live one stays")
	}
	rows, err := m.DB.ListRevocations(ctx)
	if err != nil || len(rows) != 1 || rows[0].GetSerial() != "bb" {
		t.Fatalf("revocations on record %v %v", rows, err)
	}
	if held, err := m.revoke(&v1.Revocation{Serial: "cc", NotAfter: timestamppb.New(now.Add(-time.Second))}); held || err != nil {
		t.Fatalf("a revocation of an expired certificate is not taken: %v %v", held, err)
	}
}

// A revocation that cannot be written is not held in memory either, so the next exchange carries it again
func TestRevocationNotHeldWhenUnwritten(t *testing.T) {
	m := bare(t, "self")
	m.mesh = &db.MeshRow{ID: "mesh-1"}
	m.DB.Close()
	r := &v1.Revocation{Serial: "dd", NodeId: "z", NotAfter: timestamppb.New(time.Now().Add(time.Hour)), RevokedAt: timestamppb.Now()}
	held, err := m.revoke(r)
	if err == nil || held {
		t.Fatalf("an unwritten revocation is reported: %v %v", held, err)
	}
	if m.isRevoked("dd") {
		t.Fatal("an unwritten revocation is held in memory")
	}
	m.members["z"] = &member{rec: &v1.Node{Id: "z"}, serial: "dd"}
	if err := m.revokeMember("z"); err == nil {
		t.Fatal("forgetting a member whose revocation cannot be written succeeds")
	}
	if _, ok := m.members["z"]; !ok {
		t.Fatal("the member is dropped although its revocation holds nowhere")
	}
}
