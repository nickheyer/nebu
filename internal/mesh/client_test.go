package mesh

import (
	"context"
	"testing"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Transports are held per address and trust and dropped when their member is dropped, moves, or the mesh is left
func TestTransportsDropWithTheirMembers(t *testing.T) {
	m := bare(t, "self")
	m.mesh = &db.MeshRow{ID: "id", Name: "home"}
	m.peerClient("b", "10.0.0.5:8485")
	m.peerClient("b", "10.0.0.5:8485")
	m.peerClient("c", "10.0.0.6:8485")
	m.admissionClient("10.0.0.7:8485", "d", "pin")
	if m.transports() != 3 {
		t.Fatalf("one transport per address and trust: %d", m.transports())
	}
	m.mu.Lock()
	m.members["b"] = &member{rec: &v1.Node{Id: "b", Address: "10.0.0.5:8485"}}
	m.members["c"] = &member{rec: &v1.Node{Id: "c", Address: "10.0.0.6:8485", Sequence: 1}}
	m.mu.Unlock()
	m.drop("b")
	if m.transports() != 2 {
		t.Fatalf("a dropped member's transport goes: %d", m.transports())
	}
	// A member that moved leaves its old address's transport behind
	moved := &v1.Node{Id: "c", Address: "10.0.0.8:8485", Sequence: 2, Parts: []*v1.NodePart{{Part: v1.RecordPart_RECORD_PART_IDENTITY, Sequence: 2}}}
	m.take("c", nil, moved, nil, nil)
	if m.transports() != 1 {
		t.Fatalf("a moved member's old transport goes: %d", m.transports())
	}
	if err := m.clearState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.transports() != 0 {
		t.Fatalf("leaving drops every transport: %d", m.transports())
	}
}
