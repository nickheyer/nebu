package mesh

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A manager with members set straight into its tables, sessions held so no handshake is dialed
func holdersManager(t *testing.T) *Manager {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	m := &Manager{DB: database, Config: &v1.MeshConfig{}, Host: host.New(nil, nil, time.Minute), Log: slog.Default()}
	if err := m.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *Manager) addMember(id, name string, state v1.NodeState, stored ...*v1.StoredSummary) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.members[id] = &member{rec: &v1.Node{Id: id, Name: name, Address: "127.0.0.1:1", State: state, Stored: stored}}
	m.sessions[id] = &session{peer: id, send: "token-" + id, sendExpires: time.Now().Add(time.Hour), expires: time.Now().Add(time.Hour)}
}

func (m *Manager) measure(id string, class v1.LinkClass, rtt uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.links[id] = &v1.Link{From: m.identity.ID, To: id, Class: class, RttUs: rtt, MeasuredAt: timestamppb.Now()}
}

func names(hs []Holder) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Node.GetName())
	}
	return out
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Holders come in link order: fabric, fast, lan, slow by round trip, members whose link is not
// measured yet after them by name; a member not ready or holding another group is left out; and
// a mesh with no link measured at all still answers, by name
func TestHoldersOrderByLinkThenName(t *testing.T) {
	ctx := context.Background()
	m := holdersManager(t)
	q4 := &v1.StoredSummary{SourceId: "hf", Repo: "org/model", Group: "Q4"}
	q8 := &v1.StoredSummary{SourceId: "hf", Repo: "org/model", Group: "Q8"}
	m.addMember("n-slow", "slow", v1.NodeState_NODE_STATE_READY, q4)
	m.addMember("n-lan", "lan", v1.NodeState_NODE_STATE_READY, q4)
	m.addMember("n-fast", "fast", v1.NodeState_NODE_STATE_READY, q4)
	m.addMember("n-zed", "zed", v1.NodeState_NODE_STATE_READY, q4)
	m.addMember("n-amy", "amy", v1.NodeState_NODE_STATE_READY, q4)
	m.addMember("n-gone", "gone", v1.NodeState_NODE_STATE_GONE, q4)
	m.addMember("n-other", "other", v1.NodeState_NODE_STATE_READY, q8)
	m.addMember("n-both", "both", v1.NodeState_NODE_STATE_READY, q8, q4)
	if got := names(m.Holders(ctx, "hf", "org/model", "Q4")); !same(got, []string{"amy", "both", "fast", "lan", "slow", "zed"}) {
		t.Fatalf("nothing measured yet, by name: %v", got)
	}
	m.measure("n-slow", v1.LinkClass_LINK_CLASS_SLOW, 5000)
	m.measure("n-lan", v1.LinkClass_LINK_CLASS_LAN, 900)
	m.measure("n-fast", v1.LinkClass_LINK_CLASS_FAST, 200)
	m.measure("n-both", v1.LinkClass_LINK_CLASS_LAN, 300)
	if got := names(m.Holders(ctx, "hf", "org/model", "Q4")); !same(got, []string{"fast", "both", "lan", "slow", "amy", "zed"}) {
		t.Fatalf("measured links first by class and round trip, unmeasured after by name: %v", got)
	}
	if got := names(m.Holders(ctx, "", "org/model", "")); !same(got, []string{"fast", "both", "lan", "slow", "amy", "other", "zed"}) {
		t.Fatalf("any group of the repo: %v", got)
	}
	if got := names(m.Holders(ctx, "hf", "org/model", "Q8")); !same(got, []string{"both", "other"}) {
		t.Fatalf("the other group: %v", got)
	}
	if got := m.Holders(ctx, "modelscope", "org/model", "Q4"); len(got) != 0 {
		t.Fatalf("another source holds nothing: %v", names(got))
	}
	for _, h := range m.Holders(ctx, "hf", "org/model", "Q4") {
		if h.Client == nil || h.Client.Base == "" || h.Client.HTTP == nil || h.Stored.GetGroup() != "Q4" {
			t.Fatalf("holder %s carries its connection and the matching summary: %+v", h.Node.GetName(), h)
		}
	}
	src := Source{Mesh: m}
	hs := src.Holders(ctx, "hf", "org/model", "Q4")
	if len(hs) != 6 || hs[0].Name != "fast" || hs[0].NodeID != "n-fast" || hs[0].Base == "" || len(hs[1].Stored) != 2 {
		t.Fatalf("the puller's holders carry every model a member holds: %+v", hs)
	}
}
