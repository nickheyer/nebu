package mesh_test

import (
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/estimate/mesh"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A node takes the pinned install when it has it, its newest install when none is pinned, and refuses a pin it lacks
func TestNodeOfHoldsThePinnedInstall(t *testing.T) {
	older := timestamppb.New(time.Now().Add(-time.Hour))
	newer := timestamppb.Now()
	rec := &v1.Node{Id: "a", Name: "alpha", Profile: &v1.HostProfile{}, Installs: []*v1.Install{
		{Id: "old", RuntimeId: "llamacpp", CreatedAt: older},
		{Id: "new", RuntimeId: "llamacpp", CreatedAt: newer},
		{Id: "other", RuntimeId: "vllm", CreatedAt: newer},
	}}
	numbers := numbersOf(box{})
	cases := []struct {
		name, pin, install string
		refused            bool
	}{
		{"no pin takes the newest", "", "new", false},
		{"a pin the node holds", "old", "old", false},
		{"a pin the node lacks", "gone", "", true},
		{"a pin of another runtime", "other", "", true},
	}
	for _, c := range cases {
		n := mesh.NodeOf(rec, "llamacpp", c.pin, nil, numbers)
		if n.Install.GetId() != c.install || (n.Refused != "") != c.refused {
			t.Errorf("%s: install %q refused %q", c.name, n.Install.GetId(), n.Refused)
		}
		if c.refused && !strings.Contains(n.Refused, "alpha has no install "+c.pin) {
			t.Errorf("%s: refusal %q", c.name, n.Refused)
		}
	}
	// A plan over a refused node names the missing install instead of planning on another install
	refused := mesh.NodeOf(rec, "llamacpp", "gone", nil, numbers)
	req := request(model(shape{layers: 8, layerBytes: 64 * mib, embedding: 1024, density: q4}))
	req.Span = []string{"a"}
	if _, err := mesh.Plan(req, mesh.New([]*mesh.Node{refused}, nil, "a")); err == nil || !strings.Contains(err.Error(), "alpha has no install gone of llamacpp, which the run pins") {
		t.Fatalf("plan over a refused node: %v", err)
	}
}

// A device without memory of its own never becomes a candidate, the node planning on the devices with pools
func TestNodeOfLeavesOutDevicesWithoutMemory(t *testing.T) {
	rec := &v1.Node{Id: "a", Name: "alpha", Profile: &v1.HostProfile{
		Devices: []*v1.Device{
			{Id: "gpu0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia", Name: "big"},
			{Id: "intel-0000:00:02.0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "intel", Name: "AlderLake-S GT1"},
			{Id: "cpu0", Kind: v1.DeviceKind_DEVICE_KIND_CPU, Name: "cpu"},
		},
		Pools: []*v1.MemoryPool{
			{Id: "gpu0", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "gpu0", TotalBytes: 24 << 30, FreeBytes: 24 << 30},
			{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 64 << 30, FreeBytes: 64 << 30},
		},
	}, Installs: []*v1.Install{{Id: "i", RuntimeId: "llamacpp", CreatedAt: timestamppb.Now()}}}
	n := mesh.NodeOf(rec, "llamacpp", "", nil, numbersOf(box{}))
	if len(n.Devices) != 1 || n.Devices[0].ID() != "gpu0" || n.CPU == nil {
		t.Fatalf("devices %v cpu %v", n.Devices, n.CPU)
	}
}
