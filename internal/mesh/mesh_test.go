package mesh_test

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/mesh/links"
	"github.com/nickheyer/nebu/internal/rpc/services"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"
)

// A node with its own store, listening on loopback and advertising it
func newNode(t *testing.T, ctx context.Context) *mesh.Manager {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	blobs, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	rts, err := runtimes.New(runtimes.All())
	if err != nil {
		t.Fatal(err)
	}
	table, err := perf.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	guard := auth.NewGuard("", nil, nil)
	m := &mesh.Manager{
		DB:        database,
		Config:    &v1.MeshConfig{Listen: "127.0.0.1:0", Advertise: "127.0.0.1", Announce: proto.Bool(false)},
		Host:      host.New(nil, nil, time.Minute),
		Installs:  &installs.Manager{DB: database},
		Runtimes:  rts,
		Store:     blobs,
		Perf:      table,
		Events:    events.New(),
		Guard:     guard,
		Log:       slog.Default(),
		Version:   "test",
		APIListen: "127.0.0.1:0",
	}
	if err := m.Open(ctx); err != nil {
		t.Fatal(err)
	}
	svc := services.NewMeshService(m, nil, nil, nil, blobs, table, nil, guard)
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewMeshServiceHandler(svc))
	mux.HandleFunc(links.SinkPath, links.Sink)
	m.Handler = h2c.NewHandler(mux, &http2.Server{})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within 10 s", what)
}

// Two nodes: one makes a mesh, the other joins with the token, both see each other's records,
// a change on one reaches the other, the link between them measures, and leaving forgets
func TestInitJoinSyncProbeLeave(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx)
	b := newNode(t, ctx)
	if _, err := a.Token(); err == nil {
		t.Fatal("a token before a mesh exists is refused")
	}
	meshA, token, err := a.Init(ctx, "test", true)
	if err != nil {
		t.Fatal(err)
	}
	if meshA.GetName() != "test" || !meshA.GetTls() || token == "" {
		t.Fatalf("mesh %v token %q", meshA, token)
	}
	meshB, bootstrap, _, err := b.Join(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if meshB.GetId() != meshA.GetId() || bootstrap.GetId() != a.Self() || !meshB.GetTls() {
		t.Fatalf("joined %v through %v", meshB, bootstrap)
	}
	eventually(t, "a learns b", func() bool {
		for _, n := range a.Nodes() {
			if n.GetId() == b.Self() && n.GetState() == v1.NodeState_NODE_STATE_READY && n.GetProfile() != nil {
				return true
			}
		}
		return false
	})
	eventually(t, "b learns a", func() bool {
		for _, n := range b.Nodes() {
			if n.GetId() == a.Self() && n.GetState() == v1.NodeState_NODE_STATE_READY {
				return true
			}
		}
		return false
	})
	// A change on b carries a higher sequence to a on the next sync.
	var before uint64
	for _, n := range a.Nodes() {
		if n.GetId() == b.Self() {
			before = n.GetSequence()
		}
	}
	b.Bump()
	eventually(t, "a takes b's newer record", func() bool {
		for _, n := range a.Nodes() {
			if n.GetId() == b.Self() && n.GetSequence() > before {
				return true
			}
		}
		return false
	})
	// The link measures over the members' TLS listeners, the certificates the mesh authority issued.
	linksOut, err := b.Probe(ctx, a.Self(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(linksOut) != 1 || linksOut[0].GetRttUs() == 0 || linksOut[0].GetStreamBytesPerSecond() == 0 || linksOut[0].GetClass() == v1.LinkClass_LINK_CLASS_UNSPECIFIED {
		t.Fatalf("links %v", linksOut)
	}
	if b.Link(a.Self()) == nil {
		t.Fatal("the link is kept")
	}
	eventually(t, "a sees b's link to it", func() bool {
		for _, l := range a.Links() {
			if l.GetFrom() == b.Self() && l.GetTo() == a.Self() {
				return true
			}
		}
		return false
	})
	// A second join with the same token is refused while a member.
	if _, _, _, err := b.Join(ctx, token); err == nil {
		t.Fatal("a member cannot join twice")
	}
	if _, err := b.Leave(ctx); err != nil {
		t.Fatal(err)
	}
	if b.Joined() {
		t.Fatal("b left")
	}
	eventually(t, "a forgets b", func() bool {
		for _, n := range a.Nodes() {
			if n.GetId() == b.Self() {
				return false
			}
		}
		return true
	})
}

// A node holding no mesh secret cannot be admitted
func TestHandshakeNeedsTheSecret(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx)
	b := newNode(t, ctx)
	if _, _, err := a.Init(ctx, "one", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Init(ctx, "two", false); err != nil {
		t.Fatal(err)
	}
	tokenB, err := b.Token()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := a.Join(ctx, tokenB); err == nil {
		t.Fatal("a member of one mesh cannot join another without leaving")
	}
	if _, err := a.Leave(ctx); err != nil {
		t.Fatal(err)
	}
	// A token for another mesh, with a wrong secret, is refused by the handshake.
	forged := tokenB[:len(tokenB)-4] + "AAAA"
	if _, _, _, err := a.Join(ctx, forged); err == nil {
		t.Fatal("a forged token must not admit a node")
	}
}

// Forget spreads, gossip does not undo it, reset clears all
func TestForgetAndReset(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx)
	b := newNode(t, ctx)
	c := newNode(t, ctx)
	if _, _, err := a.Init(ctx, "three", false); err != nil {
		t.Fatal(err)
	}
	token, err := a.Token()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []*mesh.Manager{b, c} {
		if _, _, _, err := n.Join(ctx, token); err != nil {
			t.Fatal(err)
		}
	}
	knows := func(m *mesh.Manager, id string) bool {
		for _, n := range m.Nodes() {
			if n.GetId() == id {
				return true
			}
		}
		return false
	}
	// A record with a profile came from the member itself, so every handshake is over.
	met := func(m *mesh.Manager, id string) bool {
		for _, n := range m.Nodes() {
			if n.GetId() == id && n.GetProfile() != nil {
				return true
			}
		}
		return false
	}
	eventually(t, "every member has met every other", func() bool {
		for _, pair := range [][2]*mesh.Manager{{a, b}, {a, c}, {b, a}, {b, c}, {c, a}, {c, b}} {
			if !met(pair[0], pair[1].Self()) {
				return false
			}
		}
		return true
	})
	if _, err := a.Forget(ctx, a.Self(), true); err == nil {
		t.Fatal("a node cannot forget itself")
	}
	b.Close()
	rec, err := a.Forget(ctx, b.Self(), true)
	if err != nil || rec.GetId() != b.Self() {
		t.Fatalf("forget: %v %v", rec, err)
	}
	if knows(a, b.Self()) {
		t.Fatal("a still lists b")
	}
	eventually(t, "c forgets b", func() bool { return !knows(c, b.Self()) })
	// Gossip from a member that still names b does not bring it back.
	if _, err := a.Sync(ctx, c.Self(), &v1.SyncRequest{Members: []*v1.Member{{Id: b.Self(), Name: "b", Address: "127.0.0.1:1"}}}); err != nil {
		t.Fatal(err)
	}
	if knows(a, b.Self()) {
		t.Fatal("gossip brought b back")
	}
	// The member c stays on record, and b's row stays as the refusal
	members, err := a.DB.ListMembers(ctx)
	if err != nil || len(members) != 2 {
		t.Fatalf("members on record after the forget: %v %v", members, err)
	}
	for _, rec := range members {
		if (rec.GetId() == b.Self()) != (rec.GetState() == v1.NodeState_NODE_STATE_FORGOTTEN) {
			t.Fatalf("b's row is the forgotten one: %v", members)
		}
	}
	// Late attempt to join after a forget event occurs, should be ignored
	late := a.Record()
	left, err := a.Reset(ctx)
	if err != nil || left == nil || a.Joined() {
		t.Fatalf("reset: %v %v joined %v", left, err, a.Joined())
	}
	if n := a.Nodes(); len(n) != 1 || !n[0].GetSelf() {
		t.Fatalf("nodes after the reset: %v", n)
	}
	if members, err := a.DB.ListMembers(ctx); err != nil || len(members) != 0 {
		t.Fatalf("members on record after the reset: %v %v", members, err)
	}
	eventually(t, "c forgets a", func() bool { return !knows(c, a.Self()) })
	// Refuse late sync
	if _, err := c.Sync(ctx, a.Self(), &v1.SyncRequest{Node: late}); err == nil {
		t.Fatal("a sync from a departed member must be refused")
	}
	if knows(c, a.Self()) {
		t.Fatal("a late sync brought a back")
	}
	if m, err := a.Reset(ctx); err != nil || m != nil {
		t.Fatalf("a reset outside any mesh: %v %v", m, err)
	}
	if _, _, err := a.Init(ctx, "again", false); err != nil {
		t.Fatal(err)
	}
	if n := a.Nodes(); len(n) != 1 || !n[0].GetSelf() {
		t.Fatalf("a new mesh starts with this node alone: %v", n)
	}
}

// An instance event bumps the node record only when it brings a state this node has not seen for
// that instance: a seat's measurements refresh every second while its conductor waits, and the
// mesh hears nothing of it; a new instance, a state change, and a deletion each bump once
func TestInstanceEventsBumpOnStateChangesOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newNode(t, ctx)
	if _, _, err := m.Init(ctx, "solo", false); err != nil {
		t.Fatal(err)
	}
	seq := func() uint64 { return m.Record().GetSequence() }
	settle := func() uint64 {
		last := seq()
		quiet := time.Now()
		for time.Since(quiet) < 500*time.Millisecond {
			time.Sleep(50 * time.Millisecond)
			if now := seq(); now != last {
				last, quiet = now, time.Now()
			}
		}
		return last
	}
	publish := func(action v1.EventAction, state v1.InstanceState, bytes uint64) {
		m.Events.Publish(v1.EventKind_EVENT_KIND_INSTANCE, action, "in1", &v1.Instance{Id: "in1", State: state, Measurements: []*v1.Measurement{{Key: "guard.received", Bytes: bytes}}})
	}
	before := settle()
	publish(v1.EventAction_EVENT_ACTION_UPDATED, v1.InstanceState_INSTANCE_STATE_STARTING, 0)
	eventually(t, "a new instance bumps the record", func() bool { return seq() > before })
	settled := settle()
	publish(v1.EventAction_EVENT_ACTION_UPDATED, v1.InstanceState_INSTANCE_STATE_STARTING, 100)
	publish(v1.EventAction_EVENT_ACTION_UPDATED, v1.InstanceState_INSTANCE_STATE_STARTING, 200)
	if after := settle(); after != settled {
		t.Fatalf("measurement refreshes bumped the record from %d to %d", settled, after)
	}
	publish(v1.EventAction_EVENT_ACTION_UPDATED, v1.InstanceState_INSTANCE_STATE_READY, 200)
	eventually(t, "a state change bumps the record", func() bool { return seq() > settled })
	ready := settle()
	publish(v1.EventAction_EVENT_ACTION_DELETED, v1.InstanceState_INSTANCE_STATE_READY, 200)
	eventually(t, "a deletion bumps the record", func() bool { return seq() > ready })
	gone := settle()
	publish(v1.EventAction_EVENT_ACTION_UPDATED, v1.InstanceState_INSTANCE_STATE_READY, 200)
	eventually(t, "an instance seen again after deletion bumps the record", func() bool { return seq() > gone })
}
