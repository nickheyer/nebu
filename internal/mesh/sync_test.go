package mesh

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/installs"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/store"
)

// A sync from a node that is not a member is refused before anything in it is taken: neither the
// caller's record nor the members it names join the table
func TestSyncFromANonMemberTakesNothing(t *testing.T) {
	m := bare(t, "self")
	m.mesh = &db.MeshRow{ID: "id", Name: "home", Secret: make([]byte, 32)}
	req := &v1.SyncRequest{
		Node:    &v1.Node{Id: "stranger", Name: "stranger", Address: "10.0.0.9:8485", Sequence: 5, Profile: &v1.HostProfile{Hostname: "stranger"}},
		Members: []*v1.Member{{Id: "friend", Name: "friend", Address: "10.0.0.10:8485"}},
	}
	_, err := m.Sync(context.Background(), "stranger", req)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a non member is refused: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.members) != 0 {
		t.Fatalf("nothing of the stranger's sync is kept: %v", m.members)
	}
}

// A forgotten node stays forgotten: its row keeps the refusal across a restart, its handshake and
// the gossip naming it are refused, an invite or admission lets it back, and rotating the secret
// clears the whole set
func TestForgottenNodesStayOut(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	blobs, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	m := bare(t, "self")
	m.DB, m.Installs, m.Store = database, &installs.Manager{DB: database}, blobs
	m.mesh = &db.MeshRow{ID: "id", Name: "home", Secret: make([]byte, 32)}
	ctx := context.Background()
	m.mu.Lock()
	m.members["b"] = &member{rec: &v1.Node{Id: "b", Name: "desk", Address: "10.0.0.5:8485", State: v1.NodeState_NODE_STATE_READY}}
	m.mu.Unlock()
	if err := m.DB.PutMember(ctx, m.members["b"].rec); err != nil {
		t.Fatal(err)
	}
	m.forget("b")
	rows, err := m.DB.ListMembers(ctx)
	if err != nil || len(rows) != 1 || rows[0].GetId() != "b" || rows[0].GetState() != v1.NodeState_NODE_STATE_FORGOTTEN || rows[0].GetName() != "desk" {
		t.Fatalf("the row keeps the refusal: %v %v", rows, err)
	}
	hello := &v1.HelloRequest{NodeId: "b", MeshHash: meshHash("id"), Nonce: []byte("nonce")}
	if _, err := m.Hello(ctx, hello); connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "was forgotten here") {
		t.Fatalf("the handshake is refused: %v", err)
	}
	m.mergeMembers([]*v1.Member{{Id: "b", Name: "desk", Address: "10.0.0.5:8485"}})
	if _, ok := m.Member("b"); ok {
		t.Fatal("gossip does not bring a forgotten node back")
	}
	// A restart reads the refusal back
	again := bare(t, "self")
	again.DB = database
	if err := again.loadMembers(ctx); err != nil {
		t.Fatal(err)
	}
	again.mu.Lock()
	_, forgotten := again.forgotten["b"]
	_, member := again.members["b"]
	again.mu.Unlock()
	if !forgotten || member {
		t.Fatalf("after a restart the node is forgotten, not a member: %v %v", forgotten, member)
	}
	// An invite or admission lets it back: the handshake proceeds past the refusal
	m.readmit("b")
	if rows, _ := m.DB.ListMembers(ctx); len(rows) != 0 {
		t.Fatalf("the row is dropped: %v", rows)
	}
	if _, err := m.Hello(ctx, hello); err != nil {
		t.Fatalf("the handshake opens again: %v", err)
	}
	// Rotation clears the whole set
	m.forget("b")
	m.mu.Lock()
	m.forgotten["c"] = time.Now()
	m.mu.Unlock()
	m.readmitAll()
	m.mu.Lock()
	left := len(m.forgotten)
	m.mu.Unlock()
	if rows, _ := m.DB.ListMembers(ctx); left != 0 || len(rows) != 0 {
		t.Fatalf("rotation forgets the refusals: %d %v", left, rows)
	}
}
