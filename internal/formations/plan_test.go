package formations

import (
	"maps"
	"testing"

	"github.com/nickheyer/nebu/internal/instances"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Each node takes the caps keyed for it by pool id, this node its bare keys too
func TestPoolCapsForEachNode(t *testing.T) {
	caps := map[string]uint64{"gpu0": 8, "self/host": 16, "far/gpu0": 4, "far/host": 2}
	cases := []struct {
		name   string
		nodeID string
		self   bool
		want   map[string]uint64
	}{
		{"this node", "self", true, map[string]uint64{"gpu0": 8, "host": 16}},
		{"another node", "far", false, map[string]uint64{"gpu0": 4, "host": 2}},
		{"a node with no caps", "near", false, map[string]uint64{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := poolCapsFor(caps, c.nodeID, c.self); !maps.Equal(got, c.want) {
				t.Fatalf("caps %v, want %v", got, c.want)
			}
		})
	}
	profile := &v1.HostProfile{Pools: []*v1.MemoryPool{{Id: "gpu0", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "gpu0", TotalBytes: 24, FreeBytes: 20}, {Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 64, FreeBytes: 1}}}
	far := instances.Constrain(profile, nil, 0, v1.Placement_PLACEMENT_UNSPECIFIED, poolCapsFor(caps, "far", false))
	if got := far.GetPools(); got[0].GetTotalBytes() != 4 || got[0].GetFreeBytes() != 4 || got[1].GetTotalBytes() != 2 || got[1].GetFreeBytes() != 1 {
		t.Fatalf("the far node's pools under its caps %v", got)
	}
}
