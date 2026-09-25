package mesh_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/mesh"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The admission a node holds about another, by the other's id
func admissionFor(m *mesh.Manager, nodeID string) *v1.Admission {
	for _, a := range m.Admissions() {
		if a.GetNode().GetId() == nodeID {
			return a
		}
	}
	return nil
}

func member(m *mesh.Manager, id string) bool {
	for _, n := range m.Nodes() {
		if n.GetId() == id && !n.GetSelf() {
			return true
		}
	}
	return false
}

// A node outside asks a member's mesh by address, the member's page admits it, and the token
// goes to it on its own: it joins with nobody copying anything
func TestAskAdmitJoin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx)
	b := newNode(t, ctx)
	if _, _, err := a.Init(ctx, "home", true); err != nil {
		t.Fatal(err)
	}
	asked, err := b.Ask(ctx, "", "", a.Address())
	if err != nil {
		t.Fatal(err)
	}
	if asked.GetSide() != v1.AdmissionSide_ADMISSION_SIDE_CANDIDATE || !asked.GetAsked() || asked.GetState() != v1.AdmissionState_ADMISSION_STATE_PENDING || asked.GetMeshName() != "home" || asked.GetNode().GetId() != a.Self() {
		t.Fatalf("candidate record %v", asked)
	}
	if !asked.GetNode().GetTls() || asked.GetNode().GetTlsFingerprint() == "" {
		t.Fatalf("the member's TLS was found on first contact: %v", asked.GetNode())
	}
	knock := admissionFor(a, b.Self())
	if knock == nil || knock.GetSide() != v1.AdmissionSide_ADMISSION_SIDE_MEMBER || !knock.GetAsked() || knock.GetInvited() || knock.GetState() != v1.AdmissionState_ADMISSION_STATE_PENDING {
		t.Fatalf("member record %v", knock)
	}
	status := a.Status()
	if status.GetMesh().GetName() != "home" || len(status.GetAdmissions()) != 1 || status.GetAddress() == "" {
		t.Fatalf("status %v", status)
	}
	// A second ask, by hash or by address, is the same record, not another.
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
	if !b.Joined() {
		t.Fatal("b joined")
	}
	if got := admissionFor(b, a.Self()); got.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED || got.GetBy() != admitted.GetBy() {
		t.Fatalf("candidate record after joining: %v", got)
	}
	eventually(t, "a sees b as a member", func() bool { return member(a, b.Self()) })
	eventually(t, "b sees a as a member", func() bool { return member(b, a.Self()) })
	// Admitting again changes nothing.
	if again, err := a.Admit(ctx, knock.GetId()); err != nil || again.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED {
		t.Fatalf("admit again: %v %v", again, err)
	}
}

// A member invites a node outside by address, the node's page accepts, and the invitation stands
// as the decision: no member decides again
func TestInviteAcceptJoin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx)
	b := newNode(t, ctx)
	if _, _, err := a.Init(ctx, "home", false); err != nil {
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
	if _, err := a.Admit(ctx, invited.GetId()); err == nil {
		t.Fatal("a node that has not asked cannot be admitted; its own page decides")
	}
	accepted, err := b.Ask(ctx, offer.GetMeshHash(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.GetState() != v1.AdmissionState_ADMISSION_STATE_ADMITTED && accepted.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED {
		t.Fatalf("after accepting: %v", accepted)
	}
	eventually(t, "b joins on the invitation", b.Joined)
	eventually(t, "the member's record says joined", func() bool {
		return admissionFor(a, b.Self()).GetState() == v1.AdmissionState_ADMISSION_STATE_JOINED
	})
	eventually(t, "the candidate's record says joined", func() bool {
		return admissionFor(b, a.Self()).GetState() == v1.AdmissionState_ADMISSION_STATE_JOINED
	})
	eventually(t, "a sees b as a member", func() bool { return member(a, b.Self()) })
	// A member cannot be invited again.
	if _, err := a.Invite(ctx, "", b.Address()); err == nil {
		t.Fatal("a member is not invited")
	}
}

// A member denies a node that asked, and a node declines an invitation; both sides learn it
func TestRefuse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx)
	b := newNode(t, ctx)
	c := newNode(t, ctx)
	if _, _, err := a.Init(ctx, "home", false); err != nil {
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
	// A denied record is settled, so the page may clear it; a pending one may not.
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
	// Asking again after a denial reopens the request.
	reopened, err := b.Ask(ctx, "", "", a.Address())
	if err != nil {
		t.Fatal(err)
	}
	if reopened.GetState() != v1.AdmissionState_ADMISSION_STATE_PENDING {
		t.Fatalf("reopened %v", reopened)
	}
	if got := admissionFor(a, b.Self()); got == nil || got.GetState() != v1.AdmissionState_ADMISSION_STATE_PENDING {
		t.Fatalf("member record after asking again %v", got)
	}
}

// A token that arrives unasked is refused, as is one for another mesh
func TestWelcomeNeedsTheAsk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx)
	b := newNode(t, ctx)
	if _, _, err := a.Init(ctx, "home", false); err != nil {
		t.Fatal(err)
	}
	token, err := a.Token()
	if err != nil {
		t.Fatal(err)
	}
	self := &v1.NearbyNode{Id: a.Self(), Name: "a", Address: a.Address()}
	meshHash := mesh.HashOf(a.Status().GetMesh().GetId())
	if _, err := b.Welcome(ctx, &v1.WelcomeRequest{Member: self, MeshHash: meshHash, Token: token}); err == nil || connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an unasked welcome is refused: %v", err)
	}
	if b.Joined() {
		t.Fatal("b did not join")
	}
	// Asked, but a token for another mesh does not admit.
	if _, err := b.Ask(ctx, "", "", a.Address()); err != nil {
		t.Fatal(err)
	}
	c := newNode(t, ctx)
	if _, _, err := c.Init(ctx, "other", false); err != nil {
		t.Fatal(err)
	}
	other, err := c.Token()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Welcome(ctx, &v1.WelcomeRequest{Member: self, MeshHash: meshHash, Token: other}); err == nil || connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a token for another mesh is refused: %v", err)
	}
	if b.Joined() {
		t.Fatal("b did not join")
	}
	// The right token, asked for, joins.
	if resp, err := b.Welcome(ctx, &v1.WelcomeRequest{Member: self, MeshHash: meshHash, Token: token}); err != nil || !resp.GetJoined() {
		t.Fatalf("welcome %v %v", resp, err)
	}
	if !b.Joined() {
		t.Fatal("b joined")
	}
}

// A request made to one member reaches every member's page over the sync, and any member's
// admission delivers the token
func TestAdmissionsReachEveryMember(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx)
	b := newNode(t, ctx)
	c := newNode(t, ctx)
	if _, _, err := a.Init(ctx, "home", false); err != nil {
		t.Fatal(err)
	}
	token, err := a.Token()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := c.Join(ctx, token); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a and c are members", func() bool { return member(a, c.Self()) && member(c, a.Self()) })
	if _, err := b.Ask(ctx, "", "", a.Address()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "c's page shows b's request", func() bool {
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
	if admitted.GetState() != v1.AdmissionState_ADMISSION_STATE_JOINED {
		t.Fatalf("after c admits: %v", admitted)
	}
	if !b.Joined() {
		t.Fatal("b joined through c")
	}
	eventually(t, "a's record says joined", func() bool {
		return admissionFor(a, b.Self()).GetState() == v1.AdmissionState_ADMISSION_STATE_JOINED
	})
	eventually(t, "everyone sees everyone", func() bool {
		return member(a, b.Self()) && member(b, a.Self()) && member(b, c.Self()) && member(c, b.Self())
	})
}

// A node in a mesh is neither invited into another nor asks to join one
func TestOneMeshAtATime(t *testing.T) {
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
	if _, err := a.Invite(ctx, "", b.Address()); err == nil {
		t.Fatal("a member of another mesh is not invited")
	}
	if _, err := a.Invite(ctx, "", b.Address()); err == nil {
		t.Fatal("a member of another mesh is not invited")
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
