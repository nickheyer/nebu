package mesh

import (
	"context"
	"net"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The admission a node holds about another, by the other's id
func admissionFor(m *testNode, nodeID string) *v1.Admission {
	for _, a := range m.Admissions() {
		if a.GetNode().GetId() == nodeID {
			return a
		}
	}
	return nil
}

// A node asks by address, a member admits it, and its certificate confirms the join
func TestAskAdmitJoin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	if _, _, err := a.Init(ctx, "home"); err != nil {
		t.Fatal(err)
	}
	asked, err := b.Ask(ctx, "", "", a.Address())
	if err != nil {
		t.Fatal(err)
	}
	if asked.GetSide() != v1.AdmissionSide_ADMISSION_SIDE_CANDIDATE || !asked.GetAsked() || asked.GetState() != v1.AdmissionState_ADMISSION_STATE_PENDING || asked.GetMeshName() != "home" || asked.GetNode().GetId() != a.Self() {
		t.Fatalf("candidate record %v", asked)
	}
	if asked.GetNode().GetTlsFingerprint() != a.identityFingerprint() {
		t.Fatalf("the member's identity was learned on first contact: %v", asked.GetNode())
	}
	knock := admissionFor(a, b.Self())
	if knock == nil || knock.GetSide() != v1.AdmissionSide_ADMISSION_SIDE_MEMBER || !knock.GetAsked() || knock.GetInvited() || knock.GetState() != v1.AdmissionState_ADMISSION_STATE_PENDING {
		t.Fatalf("member record %v", knock)
	}
	if knock.GetNode().GetTlsFingerprint() != b.identityFingerprint() {
		t.Fatal("the member pins the identity the candidate knocked with")
	}
	// A second ask, by hash or by address, is the same record
	if _, err := b.Ask(ctx, asked.GetMeshHash(), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Ask(ctx, "", "", a.Address()); err != nil {
		t.Fatal(err)
	}
	if n := len(a.Admissions()); n != 1 {
		t.Fatalf("%d admissions on the member after asking three times", n)
	}
	if n := len(b.Admissions()); n != 1 {
		t.Fatalf("%d admissions on the candidate after asking three times", n)
	}
	admitted, err := a.Admit(ctx, knock.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if admitted.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED {
		t.Fatalf("after admitting: %v", admitted)
	}
	if !b.Joined() || b.currentCreds().caKey != nil {
		t.Fatal("b joined with a certificate and no authority key")
	}
	if got := admissionFor(b, a.Self()); got.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED || got.GetBy() != admitted.GetBy() {
		t.Fatalf("candidate record after joining: %v", got)
	}
	eventually(t, "a and b meet", 10*time.Second, func() bool { return allMet(a, b) })
	if again, err := a.Admit(ctx, knock.GetId()); err != nil || again.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED {
		t.Fatalf("admit again: %v %v", again, err)
	}
}

// A member invites a node by address with a grant, and the node's acceptance joins with it at once
func TestInviteAcceptJoin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	if _, _, err := a.Init(ctx, "home"); err != nil {
		t.Fatal(err)
	}
	invited, err := a.Invite(ctx, "", b.Address())
	if err != nil {
		t.Fatal(err)
	}
	if invited.GetSide() != v1.AdmissionSide_ADMISSION_SIDE_MEMBER || !invited.GetInvited() || invited.GetAsked() || invited.GetNode().GetId() != b.Self() || invited.GetState() != v1.AdmissionState_ADMISSION_STATE_PENDING {
		t.Fatalf("member record %v", invited)
	}
	offer := admissionFor(b, a.Self())
	if offer == nil || offer.GetSide() != v1.AdmissionSide_ADMISSION_SIDE_CANDIDATE || !offer.GetInvited() || offer.GetAsked() || offer.GetMeshName() != "home" {
		t.Fatalf("candidate record %v", offer)
	}
	if b.heldGrant(offer.GetId()) == "" {
		t.Fatal("the invitation's grant is held with the admission")
	}
	grants, err := b.DB.AdmissionGrants(ctx)
	if err != nil || grants[offer.GetId()] == "" {
		t.Fatalf("the grant is kept on record: %v %v", grants, err)
	}
	if _, err := a.Admit(ctx, invited.GetId()); err == nil {
		t.Fatal("a node that has not asked cannot be admitted, its own page decides")
	}
	accepted, err := b.Ask(ctx, offer.GetMeshHash(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED || !b.Joined() {
		t.Fatalf("after accepting: %v", accepted)
	}
	eventually(t, "the member's record says joined", 10*time.Second, func() bool {
		return admissionFor(a, b.Self()).GetState() == v1.AdmissionState_ADMISSION_STATE_JOINED
	})
	eventually(t, "a and b meet", 10*time.Second, func() bool { return allMet(a, b) })
	if _, err := a.Invite(ctx, "", b.Address()); err == nil {
		t.Fatal("a member is not invited")
	}
}

// Closing a node mid admission waits for it, and a closed node binds and starts nothing
func TestCloseDuringAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	if _, _, err := a.Init(ctx, "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invite(ctx, "", b.Address()); err != nil {
		t.Fatal(err)
	}
	address := b.Address()
	b.Close()
	if conn, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		conn.Close()
		t.Fatal("a closed node still listens")
	}
	if err := b.Start(ctx); err == nil {
		t.Fatal("a closed node starts nothing")
	}
}

// A member denies a node that asked, and a node declines an invitation, both sides learning it
func TestRefuse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	c := newNode(t, ctx, 200*time.Millisecond)
	if _, _, err := a.Init(ctx, "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Ask(ctx, "", "", a.Address()); err != nil {
		t.Fatal(err)
	}
	denied, err := a.Refuse(ctx, b.Self())
	if err != nil {
		t.Fatal(err)
	}
	if denied.GetState() != v1.AdmissionState_ADMISSION_STATE_DENIED {
		t.Fatalf("member record %v", denied)
	}
	if got := admissionFor(b, a.Self()); got.GetState() != v1.AdmissionState_ADMISSION_STATE_DENIED || got.GetBy() == "" {
		t.Fatalf("candidate record %v", got)
	}
	if b.Joined() {
		t.Fatal("a denied node does not join")
	}
	if err := a.Dismiss(ctx, denied.GetId()); err != nil {
		t.Fatal(err)
	}
	if got := admissionFor(a, b.Self()); got != nil {
		t.Fatalf("dismissed records leave the page: %v", got)
	}
	if _, err := a.Invite(ctx, "", c.Address()); err != nil {
		t.Fatal(err)
	}
	if err := a.Dismiss(ctx, c.Self()); err == nil {
		t.Fatal("a pending invitation is refused, not dismissed")
	}
	declined, err := c.Refuse(ctx, a.Self())
	if err != nil {
		t.Fatal(err)
	}
	if declined.GetState() != v1.AdmissionState_ADMISSION_STATE_DECLINED {
		t.Fatalf("candidate record %v", declined)
	}
	if got := admissionFor(a, c.Self()); got.GetState() != v1.AdmissionState_ADMISSION_STATE_DECLINED {
		t.Fatalf("member record %v", got)
	}
	reopened, err := b.Ask(ctx, "", "", a.Address())
	if err != nil {
		t.Fatal(err)
	}
	if reopened.GetState() != v1.AdmissionState_ADMISSION_STATE_PENDING {
		t.Fatalf("reopened %v", reopened)
	}
}

// A grant arriving unasked, for another mesh, or from another sender than named is refused
func TestWelcomeNeedsTheAsk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	c := newNode(t, ctx, 200*time.Millisecond)
	if _, _, err := a.Init(ctx, "home"); err != nil {
		t.Fatal(err)
	}
	grant, _, err := a.Token(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	self := &v1.NearbyNode{Id: a.Self(), Name: "a", Address: a.Address()}
	hash := HashOf(a.Status().GetMesh().GetId())
	fromA := caller{kind: peerStranger, id: a.Self(), cert: a.ident.Leaf}
	if _, err := b.welcome(ctx, fromA, &v1.WelcomeRequest{Member: self, MeshHash: hash, Grant: grant}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an unasked welcome is refused: %v", err)
	}
	if _, err := b.Ask(ctx, "", "", a.Address()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.welcome(ctx, caller{kind: peerStranger, id: c.Self(), cert: c.ident.Leaf}, &v1.WelcomeRequest{Member: self, MeshHash: hash, Grant: grant}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a welcome naming another sender is refused: %v", err)
	}
	if _, _, err := c.Init(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	other, _, err := c.Token(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.welcome(ctx, fromA, &v1.WelcomeRequest{Member: self, MeshHash: hash, Grant: other}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a grant for another mesh is refused: %v", err)
	}
	if b.Joined() {
		t.Fatal("b did not join")
	}
	resp, err := b.welcome(ctx, fromA, &v1.WelcomeRequest{Member: self, MeshHash: hash, Grant: grant})
	if err != nil || !resp.GetJoined() {
		t.Fatalf("welcome %v %v", resp, err)
	}
	if err := a.confirmJoined(resp.GetCertificate(), b.Self()); err != nil {
		t.Fatalf("the welcome answers with the certificate the issuer signed: %v", err)
	}
}

// A request reaches every member's page and a member other than the issuer admits through the issuer
func TestAdmissionsReachEveryMember(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	c := newNode(t, ctx, 200*time.Millisecond)
	formMesh(t, ctx, a, c)
	eventually(t, "a and c meet", 10*time.Second, func() bool { return allMet(a, c) })
	if _, err := b.Ask(ctx, "", "", a.Address()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "c's page shows b's request", 10*time.Second, func() bool {
		got := admissionFor(c, b.Self())
		return got != nil && got.GetAsked() && got.GetState() == v1.AdmissionState_ADMISSION_STATE_PENDING
	})
	shared := admissionFor(c, b.Self())
	if shared.GetId() != admissionFor(a, b.Self()).GetId() {
		t.Fatal("members share one record per node")
	}
	admitted, err := c.Admit(ctx, shared.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if admitted.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED || !b.Joined() {
		t.Fatalf("after c admits: %v", admitted)
	}
	eventually(t, "a's record says joined", 10*time.Second, func() bool {
		return admissionFor(a, b.Self()).GetState() == v1.AdmissionState_ADMISSION_STATE_JOINED
	})
	eventually(t, "everyone meets everyone", 15*time.Second, func() bool { return allMet(a, b, c) })
}

// A node in a mesh is neither invited into another nor asks to join one
func TestOneMeshAtATime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	if _, _, err := a.Init(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Init(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := a.Invite(ctx, "", b.Address()); err == nil {
			t.Fatal("a member of another mesh is not invited")
		}
	}
	var failed []*v1.Admission
	for _, got := range a.Admissions() {
		if got.GetNode().GetAddress() == b.Address() {
			failed = append(failed, got)
		}
	}
	if len(failed) != 1 || failed[0].GetState() != v1.AdmissionState_ADMISSION_STATE_FAILED || failed[0].GetDetail() == "" {
		t.Fatalf("one failed invitation stays on the page with its reason: %v", failed)
	}
	if _, err := b.Ask(ctx, "", "", a.Address()); err == nil {
		t.Fatal("a member does not ask to join another mesh")
	}
	if _, err := a.Refuse(ctx, "nobody"); err == nil {
		t.Fatal("refusing an unknown admission fails")
	}
}
