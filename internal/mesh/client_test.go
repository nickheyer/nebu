package mesh

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/installs"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/store"
)

// The transport cache holds one transport per address and trust, and drops an address's
// transports when a member there is forgotten or moves, and every transport when the mesh is left
func TestTransportsDropWithTheirMembers(t *testing.T) {
	m := bare(t, "self")
	database, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	blobs, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	m.DB, m.Installs, m.Store = database, &installs.Manager{DB: database}, blobs
	m.roundTripper("10.0.0.5:8485", "", nil)
	m.roundTripper("10.0.0.5:8485", "", nil)
	m.roundTripper("10.0.0.6:8485", "", nil)
	m.roundTripper("10.0.0.7:8485", "", nil)
	if m.transports() != 3 {
		t.Fatalf("one transport per address: %d", m.transports())
	}
	m.mu.Lock()
	m.members["b"] = &member{rec: &v1.Node{Id: "b", Name: "desk", Address: "10.0.0.5:8485"}}
	m.members["c"] = &member{rec: &v1.Node{Id: "c", Name: "box", Address: "10.0.0.6:8485", Sequence: 1}}
	m.mu.Unlock()
	m.forget("b")
	if m.transports() != 2 {
		t.Fatalf("a forgotten member's transport is dropped: %d", m.transports())
	}
	// A member that moved leaves its old address's transport behind
	m.mesh = &db.MeshRow{ID: "id", Name: "home", Secret: make([]byte, 32)}
	m.mergeRecord(&v1.Node{Id: "c", Name: "box", Address: "10.0.0.8:8485", Sequence: 2}, true)
	if m.transports() != 1 {
		t.Fatalf("a moved member's old transport is dropped: %d", m.transports())
	}
	m.roundTripper("10.0.0.8:8485", "", nil)
	if err := m.clearState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.transports() != 0 {
		t.Fatalf("leaving drops every transport: %d", m.transports())
	}
}
