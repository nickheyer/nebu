package mesh

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// Two nodes join by certificate, sync, probe under mutual TLS, bind bearers to certificates and part
func TestInitJoinSyncProbeLeave(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	if _, _, err := a.Token(ctx, 0); err == nil {
		t.Fatal("a token before a mesh exists is refused")
	}
	meshA, token, err := a.Init(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if meshA.GetName() != "test" || meshA.GetIssuer() != a.Self() || meshA.GetCaFingerprint() == "" || token == "" {
		t.Fatalf("mesh %v token %q", meshA, token)
	}
	meshB, bootstrap, err := b.Join(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if meshB.GetId() != meshA.GetId() || bootstrap.GetId() != a.Self() || meshB.GetIssuer() != a.Self() || meshB.GetCaFingerprint() != meshA.GetCaFingerprint() {
		t.Fatalf("joined %v through %v", meshB, bootstrap)
	}
	creds := b.currentCreds()
	if creds.caKey != nil {
		t.Fatal("the authority key never leaves the issuer")
	}
	row, err := b.DB.GetMesh(ctx)
	if err != nil || len(row.CAKey) != 0 || len(row.Key) == 0 {
		t.Fatalf("b's row holds its own key and no authority key: %v", err)
	}
	if id, ok := nodeIDOf(creds.leaf); !ok || id != b.Self() || creds.leaf.URIs[0].String() != "nebu://node/"+b.Self() {
		t.Fatalf("b's certificate names it by URI: %v", creds.leaf.URIs)
	}
	if life := time.Until(creds.leaf.NotAfter); life < CertificateLifetime-time.Hour || life > CertificateLifetime {
		t.Fatalf("b's certificate lasts 90 days, %s left", life)
	}
	eventually(t, "a and b meet", 10*time.Second, func() bool { return allMet(a, b) })
	// A change on b carries a higher sequence to a
	before, _ := a.Member(b.Self())
	b.pools.set([]*v1.Pool{testPool(b.Self(), "127.0.0.1:9100", true)}, nil)
	b.Bump()
	eventually(t, "a takes b's newer record", 10*time.Second, func() bool {
		rec, _ := a.Member(b.Self())
		return rec.GetSequence() > before.GetSequence() && len(rec.GetPools()) == 1
	})
	// The link measures over the members' mutual TLS listeners
	linksOut, err := b.Probe(ctx, a.Self(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(linksOut) != 1 || linksOut[0].GetRttUs() == 0 || linksOut[0].GetStreamBytesPerSecond() == 0 || linksOut[0].GetClass() == v1.LinkClass_LINK_CLASS_UNSPECIFIED {
		t.Fatalf("links %v", linksOut)
	}
	eventually(t, "a sees b's link to it", 10*time.Second, func() bool {
		for _, l := range a.Links() {
			if l.GetFrom() == b.Self() && l.GetTo() == a.Self() {
				return true
			}
		}
		return false
	})
	// The API handler on the mesh listener sees the member its certificate proves and the bearer naming the same member
	cl, err := b.Client(ctx, a.Self())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cl.HTTP.Get(cl.Base + "/test/peer")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != b.Self()+"|"+b.Self() {
		t.Fatalf("the API sees %q", body)
	}
	// A bearer a minted for b is refused on any other member's certificate
	c := newNode(t, ctx, 200*time.Millisecond)
	tokenC, _, err := a.Token(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Join(ctx, tokenC); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a meets c", 10*time.Second, func() bool { return met(a.Manager, c.Self()) })
	stolen := cl.Header.Get("Authorization")
	req, _ := http.NewRequest(http.MethodGet, "https://"+a.Address()+"/test/peer", nil)
	req.Header.Set("Authorization", stolen)
	resp, err = (&http.Client{Transport: transport(c.memberDialConfig(a.Self()))}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a bearer on another member's certificate answered %d", resp.StatusCode)
	}
	if _, _, err := b.Join(ctx, token); err == nil {
		t.Fatal("a member cannot join twice")
	}
	serial := serialOf(creds.leaf)
	if _, err := b.Leave(ctx); err != nil {
		t.Fatal(err)
	}
	if b.Joined() {
		t.Fatal("b left")
	}
	eventually(t, "a drops b and revokes its certificate", 10*time.Second, func() bool {
		_, still := a.Member(b.Self())
		return !still && a.isRevoked(serial)
	})
	eventually(t, "c learns the revocation and drops b", 10*time.Second, func() bool {
		_, still := c.Member(b.Self())
		return !still && c.isRevoked(serial)
	})
}

// A pool of one endpoint on a node, exported to the mesh
func testPool(nodeID, address string, ready bool) *v1.Pool {
	return &v1.Pool{
		Id:   "vllm/hf:org/model:Q4",
		Spec: &v1.PoolSpec{Model: "hf:org/model:Q4", RuntimeId: "vllm", AppProtocol: v1.AppProtocol_APP_PROTOCOL_HTTP, Concurrency: 4, Export: proto.Bool(true), Metrics: &v1.MetricNames{QueuedRequests: "q", SlotsPath: "/slots"}},
		Status: &v1.PoolStatus{Endpoints: []*v1.Endpoint{{
			Id: "ep1", Address: address, NodeId: nodeID, InstanceId: "in-" + nodeID, Ready: ready, Healthy: ready,
			Labels: map[string]string{"adapter": "sql", "kv-capacity": "8192"}, Metrics: &v1.EndpointMetrics{QueuedRequests: 3},
		}}},
	}
}

// Forgetting revokes a member everywhere, gossip naming it brings nothing back, and a reset leaves its certificate refused
func TestForgetAndReset(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	c := newNode(t, ctx, 200*time.Millisecond)
	formMesh(t, ctx, a, b, c)
	eventually(t, "every member meets every other", 15*time.Second, func() bool { return allMet(a, b, c) })
	if _, err := a.Forget(ctx, a.Self()); err == nil {
		t.Fatal("a node cannot forget itself")
	}
	if _, err := c.Forget(ctx, a.Self()); err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("the issuer is not forgotten: %v", err)
	}
	bSerial := serialOf(b.currentCreds().leaf)
	b.Close()
	rec, err := a.Forget(ctx, b.Self())
	if err != nil || rec.GetId() != b.Self() {
		t.Fatalf("forget: %v %v", rec, err)
	}
	if _, still := a.Member(b.Self()); still || !a.isRevoked(bSerial) {
		t.Fatal("a still lists b")
	}
	eventually(t, "c drops b on the revocation", 10*time.Second, func() bool {
		_, still := c.Member(b.Self())
		return !still && c.isRevoked(bSerial)
	})
	// Gossip that still names b does not bring it back
	a.mergeMembers([]*v1.Member{{Id: b.Self(), Name: "b", Address: "127.0.0.1:1", Serial: bSerial, Heartbeat: 1 << 62}})
	if _, still := a.Member(b.Self()); still {
		t.Fatal("gossip brought b back")
	}
	rows, err := a.DB.ListMembers(ctx)
	if err != nil || len(rows) != 1 || rows[0].GetId() != c.Self() {
		t.Fatalf("members on record after the forget: %v %v", rows, err)
	}
	revs, err := a.DB.ListRevocations(ctx)
	if err != nil || len(revs) != 1 || revs[0].GetSerial() != bSerial {
		t.Fatalf("revocations on record: %v %v", revs, err)
	}
	old := a.currentCreds()
	left, err := a.Reset(ctx)
	if err != nil || left == nil || a.Joined() {
		t.Fatalf("reset: %v %v joined %v", left, err, a.Joined())
	}
	if n := a.Nodes(); len(n) != 1 || !n[0].GetSelf() {
		t.Fatalf("nodes after the reset: %v", n)
	}
	if rows, err := a.DB.ListMembers(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("members on record after the reset: %v %v", rows, err)
	}
	eventually(t, "c drops a", 10*time.Second, func() bool {
		_, still := c.Member(a.Self())
		return !still && c.isRevoked(serialOf(old.leaf))
	})
	// The certificate a left with is refused at c's listener
	stale := &tls.Config{
		MinVersion: tls.VersionTLS13, ServerName: meshServerName, NextProtos: []string{"h2"}, InsecureSkipVerify: true,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &old.tlsCert, nil },
	}
	if resp, err := (&http.Client{Transport: transport(stale)}).Get("https://" + c.Address() + "/test/peer"); err == nil {
		resp.Body.Close()
		t.Fatalf("a revoked certificate reached c's API, %d", resp.StatusCode)
	}
	if m, err := a.Reset(ctx); err != nil || m != nil {
		t.Fatalf("a reset outside any mesh: %v %v", m, err)
	}
	if _, _, err := a.Init(ctx, "again"); err != nil {
		t.Fatal(err)
	}
	if n := a.Nodes(); len(n) != 1 || !n[0].GetSelf() {
		t.Fatalf("a new mesh starts with this node alone: %v", n)
	}
}

// Traffic counters on exported endpoints and routes never move the record's sequence, a readiness change does
func TestTrafficNeverMovesTheRecord(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newNode(t, ctx, time.Hour)
	if _, _, err := m.Init(ctx, "solo"); err != nil {
		t.Fatal(err)
	}
	pool := testPool(m.Self(), "127.0.0.1:9100", true)
	route := &v1.Route{Name: "model", Spec: &v1.RouteSpec{Objective: "fast"}, Status: &v1.RouteStatus{PoolIds: []string{pool.GetId()}, Requests: 1}}
	m.pools.set([]*v1.Pool{pool}, []*v1.Route{route})
	m.refresh()
	settled := m.Record().GetSequence()
	for i := range 5 {
		pool = proto.Clone(pool).(*v1.Pool)
		pool.Status.Endpoints[0].Metrics = &v1.EndpointMetrics{QueuedRequests: uint32(i), RunningRequests: uint32(i * 2), KvCacheUtilization: float64(i) / 10}
		pool.Status.Endpoints[0].AssumedRequests = uint32(i)
		route = proto.Clone(route).(*v1.Route)
		route.Status.Requests, route.Status.InFlight = uint64(100+i), uint32(i)
		m.pools.set([]*v1.Pool{pool}, []*v1.Route{route})
		m.refresh()
	}
	if after := m.Record().GetSequence(); after != settled {
		t.Fatalf("traffic moved the record from %d to %d", settled, after)
	}
	pool = proto.Clone(pool).(*v1.Pool)
	pool.Status.Endpoints[0].Ready = false
	m.pools.set([]*v1.Pool{pool}, []*v1.Route{route})
	m.refresh()
	if after := m.Record().GetSequence(); after <= settled {
		t.Fatal("a readiness change moves the record")
	}
}

// A certificate chain that verifies against a pool for client use, for asserting what a member holds
func chainsTo(cert *x509.Certificate, pool *x509.CertPool) bool {
	_, err := verifyChain([]*x509.Certificate{cert}, pool, x509.ExtKeyUsageClientAuth, time.Now())
	return err == nil
}

// The mesh listener stays closed until the node makes, joins or asks to join a mesh, and binds when mesh.listen names an address
func TestListenerClosedUntilMesh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lazy := func(m *Manager) {
		m.Config = &v1.MeshConfig{Advertise: "127.0.0.1", Announce: proto.Bool(true)}
		m.defaultListen = "127.0.0.1:0"
	}
	a := newNode(t, ctx, 200*time.Millisecond, lazy)
	b := newNode(t, ctx, 200*time.Millisecond, lazy)
	c := newNode(t, ctx, 200*time.Millisecond, lazy)
	explicit := newNode(t, ctx, 200*time.Millisecond, func(m *Manager) {
		m.Config = &v1.MeshConfig{Listen: "127.0.0.1:0", Advertise: "127.0.0.1", Announce: proto.Bool(true)}
	})
	for _, n := range []*testNode{a, b, c} {
		if st := n.Status(); st.GetListen() != "" || st.GetAddress() != "" || !st.GetAnnounce() {
			t.Fatalf("a node outside a mesh listens: %v", st)
		}
	}
	if st := explicit.Status(); st.GetListen() == "" {
		t.Fatalf("mesh.listen binds at start: %v", st)
	}
	_, token, err := a.Init(ctx, "lazy")
	if err != nil {
		t.Fatal(err)
	}
	if st := a.Status(); st.GetListen() == "" || st.GetAddress() == "" {
		t.Fatalf("making a mesh binds the listener: %v", st)
	}
	if _, _, err := b.Join(ctx, token); err != nil {
		t.Fatal(err)
	}
	if st := b.Status(); st.GetListen() == "" {
		t.Fatalf("joining binds the listener: %v", st)
	}
	if _, err := c.Ask(ctx, "", "", a.Address()); err != nil {
		t.Fatal(err)
	}
	if st := c.Status(); st.GetListen() == "" {
		t.Fatalf("asking to join binds the listener for the grant to arrive on: %v", st)
	}
	// A node joined before a restart binds at start
	b.Close()
	again := &Manager{
		DB: b.DB, Config: b.Config, Host: b.Host, Installs: b.Installs, Runtimes: b.Runtimes, Store: b.Store, Perf: b.Perf,
		Pools: b.pools, Registrar: b.pools, Events: b.Events, Guard: b.Guard, Log: quietLog(), Version: "test", InFlight: b.pools.InFlight, interval: b.interval,
	}
	lazy(again)
	if err := again.Open(ctx); err != nil {
		t.Fatal(err)
	}
	again.Handler = http.NewServeMux()
	if err := again.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(again.Close)
	if st := again.Status(); st.GetListen() == "" || st.GetMesh() == nil {
		t.Fatalf("a member binds at start: %v", st)
	}
}
