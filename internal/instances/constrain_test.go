package instances

import (
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func profile() *v1.HostProfile {
	return &v1.HostProfile{
		Devices: []*v1.Device{
			{Id: "cpu", Kind: v1.DeviceKind_DEVICE_KIND_CPU},
			{Id: "g0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Facts: map[string]string{"index": "0"}},
			{Id: "g1", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Facts: map[string]string{"index": "1"}},
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
	same := Constrain(p, nil, 0, v1.Placement_PLACEMENT_UNSPECIFIED)
	if len(same.GetPools()) != 3 || len(same.GetDevices()) != 3 {
		t.Fatal("no constraint keeps everything")
	}
	same.Pools[0].TotalBytes = 1
	if p.GetPools()[0].GetTotalBytes() != 100 {
		t.Fatal("must clone")
	}
	c := Constrain(p, []string{"g1"}, 20, v1.Placement_PLACEMENT_UNSPECIFIED)
	if len(c.GetPools()) != 2 || c.GetPools()[1].GetId() != "g1" || c.GetPools()[1].GetTotalBytes() != 20 || c.GetPools()[1].GetFreeBytes() != 10 {
		t.Fatalf("pools %v", c.GetPools())
	}
	if len(c.GetDevices()) != 2 || c.GetDevices()[1].GetId() != "g1" {
		t.Fatalf("devices %v", c.GetDevices())
	}
	budget := Constrain(p, nil, 30, v1.Placement_PLACEMENT_UNSPECIFIED)
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
