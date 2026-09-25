package mesh

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A manager with what hearing beacons and merging admissions need, and nothing listening
func bare(t *testing.T, id string) *Manager {
	t.Helper()
	m := &Manager{
		Config:     &v1.MeshConfig{},
		Host:       host.New(nil, nil, time.Minute),
		Events:     events.New(),
		Log:        slog.Default(),
		identity:   &db.Identity{ID: id},
		members:    map[string]*member{},
		nearby:     map[string]*heard{},
		admissions: map[string]*v1.Admission{},
	}
	return m
}

func payload(b beacon) []byte {
	body, _ := json.Marshal(b)
	return append([]byte(beaconMagic), body...)
}

// Beacons fill the nearby table: nodes in a mesh group under it, nodes in none stand alone, a
// node's own beacon is ignored, and a node unheard past the TTL leaves
func TestHeardBeacons(t *testing.T) {
	m := bare(t, "self")
	var tried sync.Map
	ctx := context.Background()
	m.heard(ctx, payload(beacon{Node: "self", Name: "me", Address: "10.0.0.1:8485"}), "10.0.0.1", &tried)
	m.heard(ctx, payload(beacon{Node: "b", Name: "desk", Address: "10.0.0.5:8485", Mesh: "abc", MeshName: "home", Members: 2, MeshTLS: true, OS: "linux", Arch: "amd64", Version: "1"}), "10.0.0.5", &tried)
	m.heard(ctx, payload(beacon{Node: "c", Name: "box", Address: "10.0.0.6:8485", Mesh: "abc", MeshName: "home", Members: 2, MeshTLS: true}), "10.0.0.6", &tried)
	m.heard(ctx, payload(beacon{Node: "d", Name: "laptop", Address: "10.0.0.7:8485"}), "10.0.0.7", &tried)
	m.heard(ctx, payload(beacon{Node: "e", Name: "other", Address: "10.0.0.8:8485", Mesh: "zzz", MeshName: "lab", Members: 1}), "10.0.0.8", &tried)
	m.heard(ctx, []byte("not a beacon"), "10.0.0.9", &tried)
	st := m.Status()
	if len(st.GetNodes()) != 4 {
		t.Fatalf("nodes heard %v", st.GetNodes())
	}
	if len(st.GetMeshes()) != 2 {
		t.Fatalf("meshes heard %v", st.GetMeshes())
	}
	var home *v1.NearbyMesh
	for _, nm := range st.GetMeshes() {
		if nm.GetName() == "home" {
			home = nm
		}
	}
	if home == nil || home.GetHash() != "abc" || home.GetMembers() != 2 || !home.GetTls() || len(home.GetNodes()) != 2 {
		t.Fatalf("home %v", home)
	}
	for _, n := range st.GetNodes() {
		switch n.GetId() {
		case "d":
			if n.GetMeshHash() != "" || n.GetFrom() != "10.0.0.7" {
				t.Fatalf("laptop %v", n)
			}
		case "b":
			if n.GetOs() != "linux" || n.GetVersion() != "1" || n.GetMeshName() != "home" {
				t.Fatalf("desk %v", n)
			}
		}
	}
	// The freshest member of a mesh is the one to ask through.
	m.mu.Lock()
	m.nearby["b"].at = time.Now().Add(-20 * time.Second)
	fresh := m.freshestMemberLocked("abc")
	m.mu.Unlock()
	if fresh.GetId() != "c" {
		t.Fatalf("freshest %v", fresh)
	}
	// Silence past the TTL drops a node.
	m.mu.Lock()
	m.nearby["d"].at = time.Now().Add(-NearbyTTL - time.Second)
	m.mu.Unlock()
	m.expireNearby()
	if len(m.Status().GetNodes()) != 3 {
		t.Fatalf("after expiry %v", m.Status().GetNodes())
	}
	// A member's beacon marks it a member and does not list its mesh as one to ask.
	m.mu.Lock()
	m.mesh = &db.MeshRow{ID: "id", Name: "home"}
	m.members["c"] = &member{rec: &v1.Node{Id: "c"}}
	m.mu.Unlock()
	m.heard(ctx, payload(beacon{Node: "c", Name: "box", Address: "10.0.0.6:8485", Mesh: meshHex("id"), MeshName: "home", Members: 2}), "10.0.0.6", &tried)
	st = m.Status()
	for _, n := range st.GetNodes() {
		if n.GetId() == "c" && !n.GetMember() {
			t.Fatalf("c is a member: %v", n)
		}
	}
	for _, nm := range st.GetMeshes() {
		if nm.GetHash() == meshHex("id") {
			t.Fatalf("this node's own mesh is not one to ask: %v", nm)
		}
	}
}

// A beacon from the address on record marks a member's admission reached
func TestBeaconMarksReached(t *testing.T) {
	m := bare(t, "self")
	m.mesh = &db.MeshRow{ID: "id", Name: "home"}
	a := newAdmission(v1.AdmissionSide_ADMISSION_SIDE_MEMBER)
	a.Node = &v1.NearbyNode{Id: "x", Name: "x", Address: "10.0.0.9:8485"}
	a.MeshHash, a.Asked = meshHex("id"), true
	m.admissions[a.GetId()] = a
	var tried sync.Map
	m.heard(context.Background(), payload(beacon{Node: "x", Name: "x", Address: "10.0.0.2:8485"}), "10.0.0.2", &tried)
	if m.admissionByNodeLocked("x").GetReached() {
		t.Fatal("another address does not prove the one on record")
	}
	m.heard(context.Background(), payload(beacon{Node: "x", Name: "x", Address: "10.0.0.9:8485"}), "10.0.0.9", &tried)
	if !m.admissionByNodeLocked("x").GetReached() {
		t.Fatal("the address on record was heard")
	}
}

// Members' copies of an admission converge: the copy changed last wins, the flags hold once set,
// two records for one node fold into the older id, and a member's arrival settles its record
func TestMergeAdmissions(t *testing.T) {
	m := bare(t, "self")
	m.mesh = &db.MeshRow{ID: "id", Name: "home"}
	own := meshHex("id")
	dir := t.TempDir()
	database, err := db.Open(dir + "/nebu.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	m.DB = database
	now := time.Now()
	older := &v1.Admission{Id: "one", Side: v1.AdmissionSide_ADMISSION_SIDE_MEMBER, State: v1.AdmissionState_ADMISSION_STATE_PENDING, Node: &v1.NearbyNode{Id: "x", Name: "x"}, MeshHash: own, Asked: true, CreatedAt: timestamppb.New(now.Add(-time.Minute)), UpdatedAt: timestamppb.New(now.Add(-time.Minute))}
	m.mergeAdmissions([]*v1.Admission{older})
	if got := m.admissionByNodeLocked("x"); got == nil || got.GetId() != "one" {
		t.Fatalf("taken %v", got)
	}
	// A newer copy from another member with a decision wins.
	decided := &v1.Admission{Id: "one", Side: v1.AdmissionSide_ADMISSION_SIDE_MEMBER, State: v1.AdmissionState_ADMISSION_STATE_DENIED, By: "desk", Node: &v1.NearbyNode{Id: "x", Name: "x"}, MeshHash: own, Asked: true, CreatedAt: older.GetCreatedAt(), UpdatedAt: timestamppb.New(now)}
	m.mergeAdmissions([]*v1.Admission{decided})
	if got := m.admissionByNodeLocked("x"); got.GetState() != v1.AdmissionState_ADMISSION_STATE_DENIED || got.GetBy() != "desk" {
		t.Fatalf("newer copy wins %v", got)
	}
	// An older copy carrying a flag sets the flag and nothing else.
	invited := &v1.Admission{Id: "one", Side: v1.AdmissionSide_ADMISSION_SIDE_MEMBER, State: v1.AdmissionState_ADMISSION_STATE_PENDING, Node: &v1.NearbyNode{Id: "x", Name: "x"}, MeshHash: own, Invited: true, CreatedAt: older.GetCreatedAt(), UpdatedAt: timestamppb.New(now.Add(-time.Hour))}
	m.mergeAdmissions([]*v1.Admission{invited})
	if got := m.admissionByNodeLocked("x"); got.GetState() != v1.AdmissionState_ADMISSION_STATE_DENIED || !got.GetInvited() || !got.GetAsked() {
		t.Fatalf("flags hold %v", got)
	}
	// Two records for one node opened before syncing fold into the older id.
	twin := &v1.Admission{Id: "zero", Side: v1.AdmissionSide_ADMISSION_SIDE_MEMBER, State: v1.AdmissionState_ADMISSION_STATE_PENDING, Node: &v1.NearbyNode{Id: "x", Name: "x"}, MeshHash: own, Asked: true, CreatedAt: timestamppb.New(now.Add(-2 * time.Minute)), UpdatedAt: timestamppb.New(now.Add(-2 * time.Minute))}
	m.mergeAdmissions([]*v1.Admission{twin})
	got := m.admissionByNodeLocked("x")
	if got.GetId() != "zero" || got.GetState() != v1.AdmissionState_ADMISSION_STATE_DENIED || !got.GetInvited() {
		t.Fatalf("folded %v", got)
	}
	if len(m.admissions) != 1 {
		t.Fatalf("%d records for one node", len(m.admissions))
	}
	// Copies for another mesh, for this node, or from the candidate side are not taken.
	m.mergeAdmissions([]*v1.Admission{
		{Id: "a", Side: v1.AdmissionSide_ADMISSION_SIDE_MEMBER, Node: &v1.NearbyNode{Id: "y"}, MeshHash: "other", UpdatedAt: timestamppb.Now()},
		{Id: "b", Side: v1.AdmissionSide_ADMISSION_SIDE_CANDIDATE, Node: &v1.NearbyNode{Id: "y"}, MeshHash: own, UpdatedAt: timestamppb.Now()},
		{Id: "c", Side: v1.AdmissionSide_ADMISSION_SIDE_MEMBER, Node: &v1.NearbyNode{Id: "self"}, MeshHash: own, UpdatedAt: timestamppb.Now()},
	})
	if len(m.admissions) != 1 {
		t.Fatalf("%d records after foreign copies", len(m.admissions))
	}
	// A node that is a member already arrives as joined.
	m.members["z"] = &member{rec: &v1.Node{Id: "z"}}
	m.mergeAdmissions([]*v1.Admission{{Id: "zz", Side: v1.AdmissionSide_ADMISSION_SIDE_MEMBER, State: v1.AdmissionState_ADMISSION_STATE_PENDING, Node: &v1.NearbyNode{Id: "z"}, MeshHash: own, Asked: true, CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()}})
	if got := m.admissionByNodeLocked("z"); got.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED {
		t.Fatalf("member arrives joined %v", got)
	}
	// Records survive a restart.
	list, err := database.ListAdmissions(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("persisted %v %v", list, err)
	}
}
