package instances

import (
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func profile() *v1.HostProfile {
	return &v1.HostProfile{
		Devices: []*v1.Device{
			{Id: "cpu", Kind: v1.DeviceKind_DEVICE_KIND_CPU, PoolId: "host"},
			{Id: "g0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Facts: map[string]string{"index": "0"}, PoolId: "g0"},
			{Id: "g1", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Facts: map[string]string{"index": "1"}, PoolId: "g1"},
		},
		Pools: []*v1.MemoryPool{
			{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 100, FreeBytes: 90},
			{Id: "g0", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "g0", TotalBytes: 50, FreeBytes: 40},
			{Id: "g1", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "g1", TotalBytes: 60, FreeBytes: 10},
		},
	}
}

func TestConstrain(t *testing.T) {
	p := profile()
	same := Constrain(p, nil, 0, v1.Placement_PLACEMENT_UNSPECIFIED, nil)
	if len(same.GetPools()) != 3 || len(same.GetDevices()) != 3 {
		t.Fatal("no constraint keeps everything")
	}
	same.Pools[0].TotalBytes = 1
	if p.GetPools()[0].GetTotalBytes() != 100 {
		t.Fatal("must clone")
	}
	c := Constrain(p, []string{"g1"}, 20, v1.Placement_PLACEMENT_UNSPECIFIED, nil)
	if len(c.GetPools()) != 2 || c.GetPools()[1].GetId() != "g1" || c.GetPools()[1].GetTotalBytes() != 20 || c.GetPools()[1].GetFreeBytes() != 10 {
		t.Fatalf("pools %v", c.GetPools())
	}
	if len(c.GetDevices()) != 2 || c.GetDevices()[1].GetId() != "g1" {
		t.Fatalf("devices %v", c.GetDevices())
	}
	budget := Constrain(p, nil, 30, v1.Placement_PLACEMENT_UNSPECIFIED, nil)
	if budget.GetPools()[1].GetTotalBytes() != 30 || budget.GetPools()[1].GetFreeBytes() != 30 || budget.GetPools()[0].GetTotalBytes() != 100 {
		t.Fatalf("budget only %v", budget.GetPools())
	}
	views := slotDevices(c, &Reservation{DeviceIDs: []string{"g1"}})
	if len(views) != 1 || views[0].GetId() != "g1" || views[0].GetFacts()["index"] != "1" {
		t.Fatalf("views %v", views)
	}
	if slotDevices(p, nil) != nil || slotDevices(p, &Reservation{}) != nil {
		t.Fatal("no slot devices means no views")
	}
}

// Pinning a GPU caps its own pool and leaves the host pool whole, pinning the CPU alone caps nothing
func TestConstrainCapsPinnedDevicePoolsAlone(t *testing.T) {
	p := profile()
	c := Constrain(p, []string{"g0"}, 40, v1.Placement_PLACEMENT_UNSPECIFIED, nil)
	if len(c.GetPools()) != 2 || c.GetPools()[0].GetId() != "host" || c.GetPools()[0].GetTotalBytes() != 100 || c.GetPools()[1].GetId() != "g0" || c.GetPools()[1].GetTotalBytes() != 40 || c.GetPools()[1].GetFreeBytes() != 40 {
		t.Fatalf("pools %v", c.GetPools())
	}
	if len(c.GetDevices()) != 2 || c.GetDevices()[0].GetId() != "cpu" || c.GetDevices()[1].GetId() != "g0" {
		t.Fatalf("devices %v", c.GetDevices())
	}
	cpu := Constrain(p, []string{"cpu"}, 40, v1.Placement_PLACEMENT_UNSPECIFIED, nil)
	if cpu.GetPools()[0].GetTotalBytes() != 100 {
		t.Fatalf("the CPU pin alone leaves the host pool whole %v", cpu.GetPools())
	}
}

// A pool cap lowers its pool alone after the pins and the budget, the least of every bound winning
func TestConstrainCapsEachPool(t *testing.T) {
	p := profile()
	alone := Constrain(p, nil, 0, v1.Placement_PLACEMENT_UNSPECIFIED, map[string]uint64{"host": 30, "g1": 5})
	if got := alone.GetPools(); len(got) != 3 || got[0].GetTotalBytes() != 30 || got[0].GetFreeBytes() != 30 || got[1].GetTotalBytes() != 50 || got[1].GetFreeBytes() != 40 || got[2].GetTotalBytes() != 5 || got[2].GetFreeBytes() != 5 {
		t.Fatalf("caps alone %v", got)
	}
	if p.GetPools()[0].GetTotalBytes() != 100 || p.GetPools()[2].GetTotalBytes() != 60 {
		t.Fatal("must clone")
	}
	both := Constrain(p, nil, 20, v1.Placement_PLACEMENT_UNSPECIFIED, map[string]uint64{"g0": 30, "g1": 8})
	if got := both.GetPools(); got[0].GetTotalBytes() != 100 || got[1].GetTotalBytes() != 20 || got[1].GetFreeBytes() != 20 || got[2].GetTotalBytes() != 8 || got[2].GetFreeBytes() != 8 {
		t.Fatalf("caps under a budget %v", got)
	}
	pinned := Constrain(p, []string{"g1"}, 0, v1.Placement_PLACEMENT_UNSPECIFIED, map[string]uint64{"g0": 1, "host": 50, "far": 1})
	if got := pinned.GetPools(); len(got) != 2 || got[0].GetId() != "host" || got[0].GetTotalBytes() != 50 || got[0].GetFreeBytes() != 50 || got[1].GetId() != "g1" || got[1].GetTotalBytes() != 60 || got[1].GetFreeBytes() != 10 {
		t.Fatalf("caps beside a pin %v", got)
	}
	host := Constrain(p, nil, 0, v1.Placement_PLACEMENT_HOST, map[string]uint64{"g0": 45})
	if got := host.GetPools(); got[0].GetTotalBytes() != 100 || got[1].GetTotalBytes() != 45 || got[1].GetFreeBytes() != 40 {
		t.Fatalf("a cap on a device pool a host placement leaves whole %v", got)
	}
}
