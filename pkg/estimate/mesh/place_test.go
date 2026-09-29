package mesh

import (
	"slices"
	"testing"

	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

const testMiB = 1 << 20

// A node with two accelerators and a host pool for placement tests
func twoDeviceNode(second uint64) *Node {
	pool := func(id string, total uint64) *v1.MemoryPool {
		return &v1.MemoryPool{Id: id, Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: id, TotalBytes: total, FreeBytes: total}
	}
	d0 := &Device{Device: &v1.Device{Id: "g0", Name: "big"}, Pool: pool("g0", 10*1024*testMiB), Node: "n"}
	d1 := &Device{Device: &v1.Device{Id: "g1", Name: "small"}, Pool: pool("g1", second), Node: "n"}
	cpu := &Device{Device: &v1.Device{Id: "cpu", Kind: v1.DeviceKind_DEVICE_KIND_CPU}, Pool: &v1.MemoryPool{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 64 * 1024 * testMiB, FreeBytes: 64 * 1024 * testMiB}, Node: "n"}
	return &Node{ID: "n", Name: "node", Devices: []*Device{d0, d1}, CPU: cpu}
}

// Six layers of a GiB each, the first two bearing experts of 512 MiB, an embedding on the host and
// an output on the devices; the plan pools two thirds of the device bytes on the first device and
// keeps one expert group on the host
func placementFixture() ([][]*v1.TensorGroup, []*v1.TensorGroup, *v1.MemoryPlan) {
	var layers [][]*v1.TensorGroup
	for i := range 6 {
		layer := []*v1.TensorGroup{{Id: "l", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Layer: int32(i), Bytes: 1024 * testMiB}}
		if i < 2 {
			layer = append(layer, &v1.TensorGroup{Id: "e", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, Layer: int32(i), Bytes: 512 * testMiB})
		}
		layers = append(layers, layer)
	}
	extras := []*v1.TensorGroup{
		{Id: "emb", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, Layer: -1, Bytes: 256 * testMiB},
		{Id: "out", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, Layer: -1, Bytes: 300 * testMiB},
	}
	host, device := text.Enum(v1.PoolKind_POOL_KIND_HOST), text.Enum(v1.PoolKind_POOL_KIND_DEVICE)
	plan := &v1.MemoryPlan{
		Verdict:       v1.FitVerdict_FIT_VERDICT_FITS,
		CacheBytes:    6 * 64 * testMiB,
		OverheadBytes: 900 * testMiB,
		Pools: []*v1.PoolUsage{
			{PoolId: "g0", Kind: v1.PoolKind_POOL_KIND_DEVICE, UsedBytes: 6 * 1024 * testMiB},
			{PoolId: "g1", Kind: v1.PoolKind_POOL_KIND_DEVICE, UsedBytes: 3 * 1024 * testMiB},
			{PoolId: "host", Kind: v1.PoolKind_POOL_KIND_HOST, UsedBytes: 768 * testMiB},
		},
		Placements: []*v1.GroupPlacement{
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, PoolId: host, Bytes: 256 * testMiB, Count: 1},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, PoolId: device, Bytes: 6 * 1024 * testMiB, Count: 6},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, PoolId: device, Bytes: 300 * testMiB, Count: 1},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, PoolId: device, Bytes: 512 * testMiB, Count: 1},
			{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, PoolId: host, Bytes: 512 * testMiB, Count: 1},
		},
	}
	return layers, extras, plan
}

// The layers split into whole counts by the pooled bytes, each device holding its layers' weights
// kept on devices, their cache, its capacity share of the overhead, and the last the output
func TestPlaceLaysWholeLayersOnEachDevice(t *testing.T) {
	pl := &planner{req: Request{Free: true}, policy: &estimate.Policy{Margin: 0.05}}
	layers, extras, plan := placementFixture()
	pc := pl.place(twoDeviceNode(5*1024*testMiB), plan, layers, extras, nil)
	if !slices.Equal(pc.layers, []uint32{4, 2}) {
		t.Fatalf("layers %v", pc.layers)
	}
	// The first device takes layers 0-3: four GiB of layers, the experts of layer 1 (layer 0's sit
	// on the host), four layers of cache, and two thirds of the overhead by capacity
	want0 := uint64(4*1024+512+4*64)*testMiB + uint64(float64(900*testMiB)*10/15)
	// The second takes layers 4-5, the output, and a third of the overhead
	want1 := uint64(2*1024+2*64+300)*testMiB + uint64(float64(900*testMiB)*5/15)
	if pc.bytes[0] != want0 || pc.bytes[1] != want1 {
		t.Fatalf("bytes %v, want %d %d", pc.bytes, want0, want1)
	}
	if pc.over != nil {
		t.Fatalf("both fit: %s holds %d over %d", pc.over.label(), pc.held, pc.cap)
	}
	// With the family's shape, layers 3 and 5 hold the cache alone: half each
	pc = pl.place(twoDeviceNode(5*1024*testMiB), plan, layers, extras, []float64{0, 0, 0, 1, 0, 1})
	if pc.bytes[0] != want0-4*64*testMiB+192*testMiB || pc.bytes[1] != want1-2*64*testMiB+192*testMiB {
		t.Fatalf("shaped cache %v", pc.bytes)
	}
	// A smaller second device overflows with the same whole layers
	pc = pl.place(twoDeviceNode(2560*testMiB), plan, layers, extras, nil)
	if pc.over == nil || pc.over.ID() != "g1" || pc.overLayers != 2 || pc.overExtras != " and the output" {
		t.Fatalf("overflow %+v", pc)
	}
	if pc.cap != uint64(float64(2560*testMiB)*0.95) || pc.held <= pc.cap {
		t.Fatalf("held %d cap %d", pc.held, pc.cap)
	}
}

// A device holding no plan bytes takes no layers, and a plan with no device pools places nothing
func TestPlaceSkipsEmptyDevices(t *testing.T) {
	pl := &planner{req: Request{Free: true}, policy: &estimate.Policy{Margin: 0.05}}
	layers, extras, plan := placementFixture()
	plan.Pools[1].UsedBytes = 0
	pc := pl.place(twoDeviceNode(5*1024*testMiB), plan, layers, extras, nil)
	if len(pc.devices) != 1 || pc.devices[0].ID() != "g0" || !slices.Equal(pc.layers, []uint32{6}) {
		t.Fatalf("one device: %v %v", pc.devices, pc.layers)
	}
	plan.Pools[0].UsedBytes = 0
	pc = pl.place(twoDeviceNode(5*1024*testMiB), plan, layers, extras, nil)
	if len(pc.devices) != 0 || len(pc.bytes) != 0 || len(pc.layers) != 0 || pc.over != nil {
		t.Fatalf("no devices: %+v", pc)
	}
}
