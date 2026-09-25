package runtimes

import (
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Lines the runtimes log when a link is negotiated, as upstream prints them
func TestTransportLines(t *testing.T) {
	cases := []struct {
		line, want string
	}{
		{"RDMA activated: qpn=1234->5678 mtu=5 rx_depth=24", "rdma"},
		{"RDMA(Apple/UC) activated: qpn=3->7 mtu=5 rx_depth=16", "rdma"},
		{"RDMA activate failed, staying on TCP", "sockets"},
		{"  transport      : TCP", "sockets"},
		{"  transport      : TCP (RDMA auto-negotiate enabled)", ""},
		{"RDMA probed: dev=mlx5_0 gid=3 (RoCEv2) qpn=1234 inline=220", ""},
		{"node0:12:12 [0] NCCL INFO NET/IB : Using [0]mlx5_0:1/RoCE [RO]; OOB eth0:10.0.0.1<0>", "rdma"},
		{"node0:12:12 [0] NCCL INFO NET/Socket : Using [0]eth0:10.0.0.1<0>", "sockets"},
		{"Starting RPC server v7.0.0", ""},
		{"Accepted client connection", ""},
	}
	for _, c := range cases {
		if got := transportOf([]string{c.line}); got != c.want {
			t.Errorf("%q: transport %q, want %q", c.line, got, c.want)
		}
	}
	for _, rt := range []Runtime{VLLM{}, SGLang{}, LlamaCpp{}, SDCpp{}} {
		lines := []string{"Starting RPC server v7.0.0", "  transport      : TCP (RDMA auto-negotiate enabled)", "Accepted client connection", "RDMA activated: qpn=1->2 mtu=5 rx_depth=24"}
		if rt.Transport(lines) != "rdma" {
			t.Errorf("%s: activation line not read", rt.ID())
		}
		found := false
		for _, m := range rt.Measure(lines) {
			if m.GetKey() == "transport.rdma" && strings.HasPrefix(m.GetLine(), "RDMA activated") && m.GetBytes() == 0 {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: the transport is not a measurement: %v", rt.ID(), rt.Measure(lines))
		}
		if len(rt.Measure([]string{"Accepted client connection"})) != 0 {
			t.Errorf("%s: a measurement before any line names the transport", rt.ID())
		}
	}
}

// A connection that stayed on TCP without a line of its own is known from the activity that follows
// the hello: the head's buffers on rpc devices, the stage's saved tensors
func TestLlamaCppTransportFromActivity(t *testing.T) {
	llama, sd := LlamaCpp{}, SDCpp{}
	head := []string{"load_tensors: offloaded 33/33 layers to GPU", "load_tensors: RPC0[10.0.0.2:50052] model buffer size =  4096.00 MiB", "load_tensors:        CUDA0 model buffer size =  2048.00 MiB"}
	if llama.Transport(head) != "sockets" {
		t.Fatalf("head on tcp: %q", llama.Transport(head))
	}
	if llama.Transport(append([]string{"RDMA activated: qpn=1->2 mtu=5 rx_depth=24"}, head...)) != "rdma" {
		t.Fatal("head on rdma")
	}
	solo := []string{"load_tensors:        CUDA0 model buffer size =  2048.00 MiB"}
	if llama.Transport(solo) != "" {
		t.Fatal("buffers on the node's own devices name no link")
	}
	stage := []string{"Starting RPC server v7.0.0", "  transport      : TCP (RDMA auto-negotiate enabled)", "Accepted client connection", "[rpc_server::set_tensor] saved to '/cache/rpc/0123456789abcdef'"}
	if llama.Transport(stage) != "sockets" || sd.Transport(stage) != "sockets" {
		t.Fatal("stage on tcp")
	}
	if llama.Transport(stage[:3]) != "" || sd.Transport(stage[:3]) != "" {
		t.Fatal("an accepted connection alone names no transport")
	}
}

func TestRPCDeviceNames(t *testing.T) {
	stages := []*v1.SeatPeer{
		{NodeId: "a", Role: RoleStage, Rank: 1, Address: "10.0.0.2:50052", Devices: 2},
		{NodeId: "b", Role: RoleStage, Rank: 2, Address: "10.0.0.3:50052", Devices: 1},
	}
	names, err := rpcDeviceNames(stages)
	if err != nil || len(names) != 2 || strings.Join(names[0], ",") != "RPC0,RPC1" || strings.Join(names[1], ",") != "RPC2" {
		t.Fatalf("names %v %v", names, err)
	}
	if _, err := rpcDeviceNames([]*v1.SeatPeer{{NodeId: "a", Rank: 1, Address: "10.0.0.2:50052"}}); err == nil || !strings.Contains(err.Error(), "exposes no device") {
		t.Fatalf("a stage without devices: %v", err)
	}
	if _, err := rpcDeviceNames([]*v1.SeatPeer{{NodeId: "a", Rank: 1, Devices: 1}}); err == nil || !strings.Contains(err.Error(), "no address") {
		t.Fatalf("a stage without an address: %v", err)
	}
}

func TestRankHelpers(t *testing.T) {
	seat := &v1.SeatSpec{Shape: v1.Shape_SHAPE_LOCKSTEP, Role: RoleRank, Rank: 1, Count: 3, LayerFrom: 20, LayerTo: 40, Peers: []*v1.SeatPeer{
		{NodeId: "h", Role: RoleHead, Rank: 0, Devices: 2, LayerFrom: 0, LayerTo: 20},
		{NodeId: "c", Role: RoleRank, Rank: 2, Devices: 2, LayerFrom: 40, LayerTo: 48},
	}}
	if n := rankDevices(seat, 2); n != 6 {
		t.Fatalf("devices across three ranks %d", n)
	}
	partition, err := rankPartition(seat, 48)
	if err != nil || partition != "20,20,8" {
		t.Fatalf("partition %q %v", partition, err)
	}
	if _, err := rankPartition(seat, 50); err == nil || !strings.Contains(err.Error(), "cover 48 layers of a 50 layer model") {
		t.Fatalf("sum: %v", err)
	}
	seat.Peers[1].LayerTo = 40
	if _, err := rankPartition(seat, 40); err == nil || !strings.Contains(err.Error(), "rank 2 no layer range") {
		t.Fatalf("empty range: %v", err)
	}
	seat.Peers[1].Rank = 1
	if _, err := rankPartition(seat, 48); err == nil || !strings.Contains(err.Error(), "rank 1 twice") {
		t.Fatalf("duplicate rank: %v", err)
	}
	seat.Peers[1].Rank = 5
	if _, err := rankPartition(seat, 48); err == nil || !strings.Contains(err.Error(), "rank 5 among 3") {
		t.Fatalf("rank beyond count: %v", err)
	}
}

// Every role a runtime declares: phases in launch order, one head that answers the route, stages
// admitting only the head, relay pairs admitting the conductor and each other, ranks rendezvousing
func TestRoles(t *testing.T) {
	for _, rt := range []Runtime{LlamaCpp{}, VLLM{}, SGLang{}, SDCpp{}, NeMo{}} {
		for _, shape := range []v1.Shape{v1.Shape_SHAPE_SOLO, v1.Shape_SHAPE_CHAIN, v1.Shape_SHAPE_LOCKSTEP, v1.Shape_SHAPE_RELAY, v1.Shape_SHAPE_REPLICAS, v1.Shape_SHAPE_DRAFT, v1.Shape_SHAPE_STAGES} {
			roles := rt.Roles(shape)
			if len(roles) == 0 {
				continue
			}
			heads, phase := 0, 0
			for _, r := range roles {
				if r.Head {
					heads++
				}
				if r.Phase < phase {
					t.Errorf("%s %s: role %s launches in phase %d after phase %d", rt.ID(), shape, r.Name, r.Phase, phase)
				}
				phase = r.Phase
				if r.Listens && len(r.ConnectsFrom) == 0 {
					t.Errorf("%s %s: role %s listens but names nobody who connects", rt.ID(), shape, r.Name)
				}
				if r.Rendezvous && (r.Listens || len(r.ConnectsFrom) > 0 || r.Phase != 1) {
					t.Errorf("%s %s: rank role %s must launch in one phase without a guard listener", rt.ID(), shape, r.Name)
				}
			}
			if heads != 1 {
				t.Errorf("%s %s: %d heads", rt.ID(), shape, heads)
			}
		}
	}
	for _, rt := range []Runtime{VLLM{}, SGLang{}} {
		for _, shape := range []v1.Shape{v1.Shape_SHAPE_CHAIN, v1.Shape_SHAPE_LOCKSTEP} {
			roles := rt.Roles(shape)
			if len(roles) != 2 || roles[0].Name != RoleHead || !roles[0].Head || !roles[0].Rendezvous || roles[0].Health.Kind != HealthHTTP || roles[1].Name != RoleRank || roles[1].Head || !roles[1].Rendezvous || roles[1].Health.Kind != HealthProcess {
				t.Errorf("%s %s: rank roles %+v", rt.ID(), shape, roles)
			}
		}
		relay := rt.Roles(v1.Shape_SHAPE_RELAY)
		if relay[0].Name != RolePrefill || relay[0].Health.Kind != HealthHTTP || strings.Join(relay[0].ConnectsFrom, ",") != "conductor,decode" {
			t.Errorf("%s: prefill %+v", rt.ID(), relay[0])
		}
		if relay[1].Name != RoleDecode || relay[1].Health.Kind != HealthCanary || !relay[1].Head || strings.Join(relay[1].ConnectsFrom, ",") != "conductor,prefill" {
			t.Errorf("%s: decode %+v", rt.ID(), relay[1])
		}
		// The decode seat is ready on its node by its HTTP path; the canary is the conductor's step.
		h, err := SeatHealth(rt, &v1.SeatSpec{Shape: v1.Shape_SHAPE_RELAY, Role: RoleDecode})
		if err != nil || h.Kind != HealthHTTP || h.Path != "/health" || h.Interval <= 0 || h.Timeout <= 0 {
			t.Errorf("%s: decode seat health %+v %v", rt.ID(), h, err)
		}
	}
	for _, rt := range []Runtime{LlamaCpp{}} {
		relay := rt.Roles(v1.Shape_SHAPE_RELAY)
		if relay[1].Health.Kind != HealthHTTP || strings.Join(relay[0].ConnectsFrom, ",") != "conductor" || strings.Join(relay[1].ConnectsFrom, ",") != "conductor" {
			t.Errorf("llama.cpp relay carries the cache by file, not seat to seat: %+v", relay)
		}
	}
	for _, c := range []struct {
		rt    Runtime
		shape v1.Shape
		stage string
	}{{LlamaCpp{}, v1.Shape_SHAPE_CHAIN, RoleStage}, {LlamaCpp{}, v1.Shape_SHAPE_DRAFT, RoleStage}, {SDCpp{}, v1.Shape_SHAPE_STAGES, RoleDenoiser}} {
		roles := c.rt.Roles(c.shape)
		if roles[0].Name != c.stage || roles[0].Phase != 1 || !roles[0].Listens || !roles[0].Single || roles[0].Files != FilesNone || roles[0].Health.Kind != HealthHello || strings.Join(roles[0].ConnectsFrom, ",") != RoleHead {
			t.Errorf("%s %s: stage %+v", c.rt.ID(), c.shape, roles[0])
		}
		if roles[1].Name != RoleHead || roles[1].Phase != 2 || !roles[1].Head {
			t.Errorf("%s %s: head %+v", c.rt.ID(), c.shape, roles[1])
		}
	}
	if _, err := RoleOf(NeMo{}, &v1.SeatSpec{Shape: v1.Shape_SHAPE_CHAIN, Role: RoleHead}); err == nil {
		t.Fatal("nemo plays no chain")
	}
}
