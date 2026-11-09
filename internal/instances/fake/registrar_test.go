package fake

import (
	"context"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func owner(id string) pools.OwnerRef { return pools.OwnerRef{Kind: pools.OwnerInstance, ID: id} }

func endpoint(instance string, rank uint32, ready bool) pools.Published {
	return pools.Published{InstanceID: instance, Address: datalayer.Address{Host: "127.0.0.1", Port: 9000 + int(rank)}, Rank: rank, Ready: ready}
}

// Published endpoints are held per owner with their readiness, flipped per instance and per rank, and dropped on unpublish
func TestRegistrarPublishAndReadiness(t *testing.T) {
	ctx := context.Background()
	r := NewRegistrar()
	a := owner("a")
	mem := pools.Membership{Runtime: "fake", Concurrency: 2}
	if err := r.Publish(ctx, a, mem, []pools.Published{endpoint("i1", 0, true), endpoint("i1", 1, false)}); err != nil {
		t.Fatal(err)
	}
	if got := r.Endpoints(a); len(got) != 2 || got[1].Rank != 1 {
		t.Fatalf("endpoints %v", got)
	}
	if m, ok := r.Membership(a); !ok || m.Runtime != "fake" {
		t.Fatalf("membership %v %v", m, ok)
	}
	if r.Ready("i1") || r.RankReady("i1", 1) || !r.RankReady("i1", 0) {
		t.Fatal("the last published rank state wins")
	}
	r.SetRankReady(ctx, "i1", 1, true)
	if !r.Ready("i1") || !r.RankReady("i1", 1) {
		t.Fatal("every rank ready makes the instance ready")
	}
	r.SetRankReady(ctx, "i1", 0, false)
	if r.Ready("i1") {
		t.Fatal("one rank down takes the instance down")
	}
	r.SetReady(ctx, "i1", true)
	if !r.Ready("i1") || !r.RankReady("i1", 0) || !r.RankReady("i1", 1) {
		t.Fatal("instance readiness sets every rank")
	}
	r.Drain(ctx, a)
	if r.Ready("i1") || r.RankReady("i1", 0) || r.Drains(a) != 1 || r.Drains(owner("b")) != 0 {
		t.Fatalf("drain left ready %v %v drains %d", r.Ready("i1"), r.RankReady("i1", 0), r.Drains(a))
	}
	r.Unpublish(ctx, a)
	if got := r.Endpoints(a); len(got) != 0 {
		t.Fatalf("endpoints after unpublish %v", got)
	}
	if _, ok := r.Membership(a); ok {
		t.Fatal("an owner with nothing left is forgotten")
	}
	want := []string{"publish a", "rankready i1 1 true", "rankready i1 0 false", "ready i1", "drain a", "unpublish a"}
	if got := r.History(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history %v", got)
	}
}

// Routes belong to the owner that named them, splits follow, and dropping them frees an owner holding no endpoints
func TestRegistrarRoutes(t *testing.T) {
	ctx := context.Background()
	r := NewRegistrar()
	a, b := owner("a"), owner("b")
	if _, err := r.NameRoute(ctx, a, "chat", pools.RouteSpec{}); err == nil {
		t.Fatal("an owner without a pool cannot name a route")
	}
	if err := r.Publish(ctx, a, pools.Membership{Runtime: "fake"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Publish(ctx, b, pools.Membership{Runtime: "fake"}, []pools.Published{endpoint("i2", 0, true)}); err != nil {
		t.Fatal(err)
	}
	spec := pools.RouteSpec{ServedName: "model-a", Policy: &v1.Policy{MaxInFlight: 2}}
	route, err := r.NameRoute(ctx, a, "chat", spec)
	if err != nil || route.GetName() != "chat" || route.GetOwnerKind() != pools.OwnerInstance || route.GetOwnerId() != "a" {
		t.Fatalf("route %v %v", route, err)
	}
	if _, err := r.NameRoute(ctx, b, "chat", pools.RouteSpec{}); err == nil {
		t.Fatal("another owner must not take the route")
	}
	if named, ok := r.Named("chat"); !ok || named.Owner != a || named.Spec.ServedName != "model-a" || named.Split {
		t.Fatalf("named %+v %v", named, ok)
	}
	if err := r.SplitRoute(ctx, b, "chat", nil); err == nil {
		t.Fatal("another owner must not split the route")
	}
	if err := r.SplitRoute(ctx, a, "other", nil); err == nil {
		t.Fatal("an unnamed route cannot be split")
	}
	targets := []pools.WeightedTarget{{Pool: "p1", Model: "m", Weight: 1}, {Pool: "p2", Model: "m", Weight: 3}}
	if err := r.SplitRoute(ctx, a, "chat", targets); err != nil {
		t.Fatal(err)
	}
	if named, _ := r.Named("chat"); !named.Split || len(named.Targets) != 2 || named.Targets[1].Weight != 3 {
		t.Fatalf("split %+v", named)
	}
	if _, err := r.NameRoute(ctx, a, "chat", spec); err != nil {
		t.Fatal(err)
	}
	if named, _ := r.Named("chat"); named.Split || named.Targets != nil {
		t.Fatalf("naming again clears the split %+v", named)
	}
	if _, err := r.NameRoute(ctx, a, "alt", spec); err != nil {
		t.Fatal(err)
	}
	if view, ok := r.Route("alt"); !ok || view.GetOwnerId() != "a" {
		t.Fatalf("route view %v %v", view, ok)
	}
	if _, ok := r.Route("none"); ok {
		t.Fatal("an unnamed route has no view")
	}
	if _, ok := r.Named("none"); ok {
		t.Fatal("an unnamed route is not named")
	}
	if got := r.RouteNames(); strings.Join(got, ",") != "alt,chat" {
		t.Fatalf("route names %v", got)
	}
	r.DropRoute(ctx, b, "chat")
	if _, ok := r.Named("chat"); !ok {
		t.Fatal("another owner must not drop the route")
	}
	r.DropRoute(ctx, a, "chat")
	if _, ok := r.Named("chat"); ok {
		t.Fatal("the owner drops its route")
	}
	if _, ok := r.Membership(a); !ok {
		t.Fatal("an owner still naming a route keeps its pool")
	}
	r.DropRoutes(ctx, a)
	if len(r.RouteNames()) != 0 {
		t.Fatalf("routes left %v", r.RouteNames())
	}
	if _, ok := r.Membership(a); ok {
		t.Fatal("an owner with no endpoints and no routes is forgotten")
	}
	if _, ok := r.Membership(b); !ok {
		t.Fatal("the other owner keeps its pool")
	}
}

// Objectives are held per owner by name and copied on the way in and out
func TestRegistrarObjectives(t *testing.T) {
	ctx := context.Background()
	r := NewRegistrar()
	a := owner("a")
	in := &v1.Objective{Name: "fast", TtftMs: 100}
	out, err := r.PutObjectiveFor(ctx, a, in)
	if err != nil || out == in || out.GetTtftMs() != 100 {
		t.Fatalf("put %v %v", out, err)
	}
	in.TtftMs = 999
	if _, err := r.PutObjectiveFor(ctx, a, &v1.Objective{Name: "slow", TtftMs: 500}); err != nil {
		t.Fatal(err)
	}
	if len(r.objectives[a]) != 2 || r.objectives[a]["fast"].GetTtftMs() != 100 {
		t.Fatalf("objectives %v", r.objectives[a])
	}
	r.DropObjective(ctx, a, "fast")
	if len(r.objectives[a]) != 1 {
		t.Fatalf("objectives after drop %v", r.objectives[a])
	}
	r.DropObjectives(ctx, a)
	if _, ok := r.objectives[a]; ok {
		t.Fatal("objectives after dropping all")
	}
	if got := r.History(); strings.Join(got, "|") != "objective fast|objective slow|dropobjective fast|dropobjectives a" {
		t.Fatalf("history %v", got)
	}
}

// Traffic counts in flight and dispatched requests per instance and the load its scrape last reported
func TestTraffic(t *testing.T) {
	tr := NewTraffic()
	tr.SetInFlight("i1", 3)
	tr.Dispatch("i1", 2)
	tr.Dispatch("i1", 5)
	tr.SetLoad("i1", 1, 2)
	tr.SetLoad("i1", 4, 0)
	running, queued, scraped := tr.Load("i1")
	if tr.InFlight("i1") != 3 || tr.Dispatched("i1") != 7 || running != 4 || queued != 0 || !scraped {
		t.Fatalf("in flight %d dispatched %d load %d/%d scraped %v", tr.InFlight("i1"), tr.Dispatched("i1"), running, queued, scraped)
	}
	if running, queued, scraped := tr.Load("i2"); tr.InFlight("i2") != 0 || tr.Dispatched("i2") != 0 || running != 0 || queued != 0 || scraped {
		t.Fatal("an unknown instance counts nothing and was never scraped")
	}
}
