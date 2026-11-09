package mesh

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/pools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A pool exported from one mesh is imported into another over the identity certificates the export token pins, drained when withdrawn and dropped when the exporter ends the export
func TestCrossMeshExportImport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	meshA, _, err := a.Init(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	meshB, _, err := b.Init(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.ExportToken(ctx, 0); err != nil {
		t.Fatal(err)
	}
	_, serving := runtimeServer(t, "alpha-runtime")
	pool := testPool(a.Self(), serving, true)
	pool.Status.Parents = []*v1.ParentStatus{{ParentRef: &v1.ParentReference{Kind: "Gateway", Name: a.Self()}, ControllerName: "nebu.dev/gateway", Conditions: []*v1.Condition{{Type: "Accepted", Status: v1.ConditionStatus_CONDITION_STATUS_TRUE, Reason: "Accepted"}}}}
	routes := []*v1.Route{{Name: "chat", Spec: &v1.RouteSpec{}, Status: &v1.RouteStatus{PoolIds: []string{pool.GetId()}}}}
	a.pools.set([]*v1.Pool{pool}, routes)
	a.Bump()
	token, expires, err := a.ExportToken(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if left := time.Until(expires); left < 59*time.Minute || left > time.Hour {
		t.Fatalf("an export token lasts an hour by default, %s left", left)
	}
	if _, err := b.ImportMesh(ctx, "nebu-join-nonsense"); err == nil || !strings.Contains(err.Error(), "not an export token") {
		t.Fatalf("a join token is no export token: %v", err)
	}
	node, err := b.ImportMesh(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	f := node.GetFederation()
	if node.GetId() != a.Self() || f.GetRole() != v1.FederationRole_FEDERATION_ROLE_EXPORTER || f.GetMeshHash() != HashOf(meshA.GetId()) || f.GetMeshName() != "alpha" || f.GetAddress() != a.Address() {
		t.Fatalf("imported %v", node)
	}
	owner := pools.OwnerRef{Kind: "node", ID: a.Self() + "/" + pool.GetId()}
	eventually(t, "b publishes a's pool", 10*time.Second, func() bool {
		pub, named := b.pools.snapshot()
		call, ok := pub[owner]
		return ok && len(call.endpoints) == 1 && call.endpoints[0].Ready && len(named[owner]) == 1
	})
	pub, _ := b.pools.snapshot()
	ep := pub[owner].endpoints[0]
	if ep.NodeID != a.Self() || ep.Address.String() == serving {
		t.Fatalf("the endpoint is a's behind its relay: %+v", ep)
	}
	list := b.Imports()
	if len(list) != 1 {
		t.Fatalf("imports %v", list)
	}
	ctl := list[0].GetStatus().GetControllers()[0]
	if src := ctl.GetExportingSources(); len(src) != 1 || src[0].GetNodeId() != a.Self() || src[0].GetMeshHash() != HashOf(meshA.GetId()) || src[0].GetMeshHash() == HashOf(meshB.GetId()) {
		t.Fatalf("the import names the exporting node and its own mesh: %v", src)
	}
	found := false
	for _, p := range ctl.GetParents() {
		if p.GetControllerName() == "nebu.dev/gateway" && p.GetParentRef().GetName() == a.Self() && p.GetParentRef().GetKind() == "Gateway" && len(p.GetConditions()) == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the exporter's parent entry is carried under its controller and gateway: %v", ctl.GetParents())
	}
	// The data path crosses the meshes by pinned certificate
	resp, err := dialClient(b.Manager).Get("http://" + ep.Address.String() + "/v1/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "alpha-runtime /v1/x" {
		t.Fatalf("b reached %q", body)
	}
	// The exporter's gateway surface takes the importer by certificate and bearer
	cl, err := b.Client(ctx, a.Self())
	if err != nil {
		t.Fatal(err)
	}
	resp, err = cl.HTTP.Get(cl.Base + "/test/peer")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != b.Self()+"|"+b.Self() {
		t.Fatalf("a sees the importer as %q", body)
	}
	resp, err = cl.HTTP.Post(cl.Base+"/nebu.v1.MeshPeerService/Sync", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("an importer reaches the node to node services: %d", resp.StatusCode)
	}
	feds := a.Status().GetFederations()
	if len(feds) != 1 || feds[0].GetId() != b.Self() || feds[0].GetFederation().GetRole() != v1.FederationRole_FEDERATION_ROLE_IMPORTER || feds[0].GetFederation().GetMeshHash() != HashOf(meshB.GetId()) {
		t.Fatalf("a's federations %v", feds)
	}
	if rows, err := a.DB.ListMembers(ctx); err != nil || len(rows) != 1 || rows[0].GetFederation().GetFingerprint() != b.identityFingerprint() {
		t.Fatalf("the importer is on record by its identity: %v %v", rows, err)
	}
	// A token imports once
	c := newNode(t, ctx, 200*time.Millisecond)
	if _, err := c.ImportMesh(ctx, token); err == nil || !strings.Contains(err.Error(), "used already") {
		t.Fatalf("a used export token: %v", err)
	}
	// A withdrawn pool drains then goes
	a.pools.set(nil, nil)
	a.Bump()
	eventually(t, "b unpublishes a's withdrawn pool", 10*time.Second, func() bool {
		pub, _ := b.pools.snapshot()
		_, ok := pub[owner]
		return !ok
	})
	calls := b.pools.log()
	if len(calls) < 3 || calls[0] != "drain "+owner.ID || calls[len(calls)-1] != "unpublish "+owner.ID {
		t.Fatalf("drain comes before unpublish: %v", calls)
	}
	// The exporter ending the export is refused on b's next pull, which drops it
	a.pools.set([]*v1.Pool{testPool(a.Self(), serving, true)}, nil)
	a.Bump()
	eventually(t, "b publishes the pool again", 10*time.Second, func() bool {
		pub, _ := b.pools.snapshot()
		_, ok := pub[owner]
		return ok
	})
	if _, err := a.DropFederation(ctx, b.Self()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "b drops the federation and the import", 10*time.Second, func() bool {
		pub, _ := b.pools.snapshot()
		_, ok := pub[owner]
		return !ok && len(b.Federations()) == 0
	})
	if _, err := b.DropFederation(ctx, a.Self()); err == nil {
		t.Fatal("a dropped federation is unknown")
	}
}
