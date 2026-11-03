package instances

import (
	"context"
	"slices"
	"testing"

	"github.com/nickheyer/nebu/internal/instances/fake"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The accelerator class follows the device pools a plan uses, named through the host profile
func TestAcceleratorOf(t *testing.T) {
	profile := &v1.HostProfile{
		Devices: []*v1.Device{{Id: "gpu0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "NVIDIA", Name: "NVIDIA H100 80GB HBM3"}, {Id: "gpu1", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "NVIDIA", Name: "NVIDIA H100 80GB HBM3"}, {Id: "gpu2", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "amd"}},
		Pools:   []*v1.MemoryPool{{Id: "pool0", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "gpu0"}, {Id: "pool1", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "gpu1"}, {Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST}},
	}
	device := func(id string, used uint64) *v1.PoolUsage {
		return &v1.PoolUsage{PoolId: id, Kind: v1.PoolKind_POOL_KIND_DEVICE, UsedBytes: used}
	}
	cases := []struct {
		name    string
		plan    *v1.MemoryPlan
		profile *v1.HostProfile
		want    string
	}{
		{"no plan", nil, profile, ""},
		{"host only", &v1.MemoryPlan{Pools: []*v1.PoolUsage{{PoolId: "host", Kind: v1.PoolKind_POOL_KIND_HOST, UsedBytes: 10}}}, profile, "cpu"},
		{"one device by pool id", &v1.MemoryPlan{Pools: []*v1.PoolUsage{device("pool0", 10)}}, profile, "1x NVIDIA H100 80GB HBM3"},
		{"two devices of one name", &v1.MemoryPlan{Pools: []*v1.PoolUsage{device("pool0", 10), device("gpu1", 10)}}, profile, "2x NVIDIA H100 80GB HBM3"},
		{"unused device pool left out", &v1.MemoryPlan{Pools: []*v1.PoolUsage{device("pool0", 10), device("gpu1", 0)}}, profile, "1x NVIDIA H100 80GB HBM3"},
		{"nameless device uses vendor and id", &v1.MemoryPlan{Pools: []*v1.PoolUsage{device("gpu2", 10)}}, profile, "1x amd gpu2"},
		{"unknown device keeps its pool id", &v1.MemoryPlan{Pools: []*v1.PoolUsage{device("cuda:7", 10)}}, profile, "1x cuda:7"},
		{"mixed classes", &v1.MemoryPlan{Pools: []*v1.PoolUsage{device("gpu2", 10), device("pool1", 10)}}, profile, "1x amd gpu2 + 1x NVIDIA H100 80GB HBM3"},
		{"no profile", &v1.MemoryPlan{Pools: []*v1.PoolUsage{device("gpu0", 10)}}, nil, "1x gpu0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AcceleratorOf(c.plan, c.profile); got != c.want {
				t.Fatalf("AcceleratorOf = %q, want %q", got, c.want)
			}
		})
	}
}

// Route names are the instance name then each distinct alias param
func TestRouteNames(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		want   []string
	}{
		{"no alias", nil, []string{"solo"}},
		{"one alias", map[string]string{"alias": "chat"}, []string{"solo", "chat"}},
		{"several aliases", map[string]string{"alias": " chat, assistant ,chat,, solo"}, []string{"solo", "chat", "assistant"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RouteNames(&v1.Instance{Name: "solo", Params: c.params}); !slices.Equal(got, c.want) {
				t.Fatalf("RouteNames = %v, want %v", got, c.want)
			}
		})
	}
}

// An instance with aliases names a route per alias in its pool, and all of them share its membership
func TestAliasRoutesNamed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	m := n.manager(t, ctx, nil)
	in, _, err := m.Run(ctx, soloRun(map[string]string{fake.ParamAlias: "chat, assistant"}))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	owner := ownerOf(in.GetId())
	names := n.registrar.RouteNames()
	slices.Sort(names)
	if !slices.Equal(names, []string{"assistant", "chat", "solo"}) {
		t.Fatalf("routes %v", names)
	}
	for _, name := range names {
		rt, ok := n.registrar.Named(name)
		if !ok || rt.Owner != owner || rt.Spec.ServedName != "chat" {
			t.Fatalf("route %s %+v", name, rt)
		}
	}
	mb, _ := n.registrar.Membership(owner)
	if mb.Accelerator != AcceleratorOf(current(t, m, in.GetId()).GetPlan(), nil) || mb.Runtime != fake.RuntimeID {
		t.Fatalf("membership %+v", mb)
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_STOPPED)
	if names := n.registrar.RouteNames(); len(names) != 0 {
		t.Fatalf("routes after the stop %v", names)
	}
}
