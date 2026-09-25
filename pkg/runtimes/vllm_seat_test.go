package runtimes

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A chain rank seat on the runtime: rank 0 of two, the rendezvous at the head's address, two RDMA
// devices on the link, and a planned layer range on every rank
func chainSeat(rank uint32) *v1.SeatSpec {
	seat := &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_CHAIN, Role: RoleHead, Rank: 0, Count: 2, Rendezvous: "10.0.0.5:29500", Address: "10.0.0.1", Port: 8000, Exposed: true, Interface: "eth0", RdmaDevices: []string{"mlx5_0", "mlx5_1"}, LayerFrom: 0, LayerTo: 20,
		Peers: []*v1.SeatPeer{{NodeId: "n2", Role: RoleRank, Rank: 1, Devices: 2, LayerFrom: 20, LayerTo: 32}}}
	if rank == 1 {
		seat.Role, seat.Rank, seat.LayerFrom, seat.LayerTo = RoleRank, 1, 20, 32
		seat.Peers = []*v1.SeatPeer{{NodeId: "n1", Role: RoleHead, Rank: 0, Devices: 2, LayerFrom: 0, LayerTo: 20}}
		seat.RdmaDevices = []string{"mlx5_0"}
	}
	return seat
}

func TestVLLMRankSeats(t *testing.T) {
	rt := VLLM{}
	params, err := Resolve(rt, map[string]string{"speculative_config": `{"method":"mtp","num_speculative_tokens":1}`})
	if err != nil {
		t.Fatal(err)
	}
	in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights_dir": "/w"}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/venv/bin/vllm"}, Devices: []*v1.Device{gpu("GPU-0", "0"), gpu("GPU-1", "1")}, Descriptor: layered(32), Seat: chainSeat(0), InstallRecord: recorded(map[string]string{factNNodes: "--nnodes"})}
	cmd, err := rt.LaunchSeat(in)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd.Args, " ")
	if cmd.Command != "/venv/bin/vllm" || !strings.HasPrefix(line, "serve /w --host 10.0.0.1 --port 8000") {
		t.Fatalf("head %s %s", cmd.Command, line)
	}
	for _, want := range []string{"--tensor-parallel-size 2", "--pipeline-parallel-size 2 --nnodes 2 --node-rank 0 --master-addr 10.0.0.5 --master-port 29500 --distributed-executor-backend mp"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %s in %s", want, line)
		}
	}
	if strings.Contains(line, "--headless") || strings.Contains(line, "--speculative-config") {
		t.Fatalf("a chain head is not headless and runs no speculative decoding: %s", line)
	}
	for key, want := range map[string]string{
		"VLLM_HOST_IP": "10.0.0.1", "VLLM_PP_LAYER_PARTITION": "20,12",
		"NCCL_SOCKET_IFNAME": "eth0", "GLOO_SOCKET_IFNAME": "eth0",
		"NCCL_NET": "IB", "NCCL_IB_HCA": "mlx5_0,mlx5_1", "NCCL_IB_MERGE_NICS": "1", "NCCL_NET_PLUGIN": "none",
		"NCCL_DEBUG": "INFO", "NCCL_DEBUG_SUBSYS": "INIT,NET",
		"TORCH_NCCL_HEARTBEAT_TIMEOUT_SEC": "30", "TORCH_NCCL_ENABLE_MONITORING": "0",
		"CUDA_VISIBLE_DEVICES": "GPU-0,GPU-1",
	} {
		if cmd.Env[key] != want {
			t.Errorf("%s = %q, want %q", key, cmd.Env[key], want)
		}
	}
	if cmd.Params["pp_layer_partition"] != "20,12" || cmd.Params["master"] != "10.0.0.5:29500" || cmd.Params["node_rank"] != "0" || cmd.Params["nnodes"] != "2" || cmd.Params["pipeline_parallel_size"] != "2" {
		t.Fatalf("params %v", cmd.Params)
	}
	// Rank 1 is headless, with one RDMA device unmerged, and the same partition.
	in.Seat = chainSeat(1)
	cmd, err = rt.LaunchSeat(in)
	if err != nil {
		t.Fatal(err)
	}
	line = strings.Join(cmd.Args, " ")
	if !strings.Contains(line, "--node-rank 1 --master-addr 10.0.0.5 --master-port 29500 --distributed-executor-backend mp --headless") || cmd.Env["VLLM_PP_LAYER_PARTITION"] != "20,12" || cmd.Env["NCCL_IB_HCA"] != "mlx5_0" || cmd.Env["NCCL_IB_MERGE_NICS"] != "" {
		t.Fatalf("rank 1 %s %v", line, cmd.Env)
	}
	// A link without RDMA devices takes sockets on the interface alone.
	in.Seat.RdmaDevices = nil
	cmd, _ = rt.LaunchSeat(in)
	if cmd.Env["NCCL_NET"] != "" || cmd.Env["NCCL_IB_HCA"] != "" || cmd.Env["NCCL_NET_PLUGIN"] != "" || cmd.Env["NCCL_SOCKET_IFNAME"] != "eth0" {
		t.Fatalf("sockets %v", cmd.Env)
	}
	// Lockstep: one stage, every device across three ranks in tensor parallel, speculative decoding kept.
	lock := &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_LOCKSTEP, Role: RoleRank, Rank: 1, Count: 3, Rendezvous: "[fd00::5]:29500", Address: "fd00::2", Port: 8000, Exposed: true,
		Peers: []*v1.SeatPeer{{NodeId: "n1", Role: RoleHead, Rank: 0, Devices: 2}, {NodeId: "n3", Role: RoleRank, Rank: 2, Devices: 2}}}
	in.Seat = lock
	cmd, err = rt.LaunchSeat(in)
	if err != nil {
		t.Fatal(err)
	}
	line = strings.Join(cmd.Args, " ")
	if !strings.Contains(line, "--tensor-parallel-size 6") || !strings.Contains(line, "--pipeline-parallel-size 1 --nnodes 3 --node-rank 1 --master-addr fd00::5 --master-port 29500") || !strings.Contains(line, "--speculative-config ") || !strings.Contains(line, "--headless") {
		t.Fatalf("lockstep %s", line)
	}
	if _, set := cmd.Env["VLLM_PP_LAYER_PARTITION"]; set {
		t.Fatal("lockstep partitions no layers")
	}
	cases := []struct {
		name string
		edit func()
		want string
	}{
		{"no nnodes", func() { in.InstallRecord = recorded(nil) }, "does not accept --nnodes"},
		{"no rendezvous", func() { in.Seat.Rendezvous = "" }, "rendezvous address"},
		{"no gpus", func() { in.Devices = nil }, "at least one GPU"},
		{"count", func() { in.Seat.Count = 3 }, "counts 3 ranks and lists 1 peers"},
		{"no layer range", func() { in.Seat.Peers[0].LayerTo = 0 }, "rank 1 no layer range"},
		{"layers", func() { in.Descriptor = layered(40) }, "cover 32 layers of a 40 layer model"},
		{"no layers", func() { in.Descriptor = layered(0) }, "counts no layers"},
		{"no seat", func() { in.Seat = nil }, "seat block"},
		{"bad role", func() { in.Seat.Role = RoleStage }, "has no role"},
	}
	for _, c := range cases {
		in.Seat, in.Devices, in.Descriptor, in.InstallRecord = chainSeat(0), []*v1.Device{gpu("GPU-0", "0"), gpu("GPU-1", "1")}, layered(32), recorded(map[string]string{factNNodes: "--nnodes"})
		c.edit()
		if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestVLLMRelaySeats(t *testing.T) {
	rt := VLLM{}
	params, _ := Resolve(rt, nil)
	seat := &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_RELAY, Role: RolePrefill, Rank: 0, Count: 2, Address: "10.0.0.1", Port: 8000, AuxPort: 5600, Interface: "ib0", RdmaDevices: []string{"mlx5_0"}, Peers: []*v1.SeatPeer{{NodeId: "n2", Role: RoleDecode, Rank: 1, Devices: 1}}}
	in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights_dir": "/w"}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/venv/bin/vllm"}, Devices: []*v1.Device{gpu("GPU-0", "0")}, Seat: seat, InstallRecord: recorded(map[string]string{factNIXL: "NixlConnector"})}
	cmd, err := rt.LaunchSeat(in)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd.Args, " ")
	if !strings.HasPrefix(line, "serve /w --host 127.0.0.1 --port 9") || !strings.HasSuffix(line, `--kv-transfer-config {"kv_connector":"NixlConnector","kv_role":"kv_producer"}`) {
		t.Fatalf("prefill %s", line)
	}
	if cmd.Env["VLLM_NIXL_SIDE_CHANNEL_HOST"] != "10.0.0.1" || cmd.Env["VLLM_NIXL_SIDE_CHANNEL_PORT"] != "5600" || cmd.Env["VLLM_HOST_IP"] != "10.0.0.1" || cmd.Env["NCCL_SOCKET_IFNAME"] != "ib0" || cmd.Env["NCCL_IB_HCA"] != "mlx5_0" || cmd.Params["kv_role"] != RolePrefill {
		t.Fatalf("prefill env %v params %v", cmd.Env, cmd.Params)
	}
	seat.Role = RoleDecode
	if cmd, err = rt.LaunchSeat(in); err != nil || !strings.HasSuffix(strings.Join(cmd.Args, " "), `"kv_role":"kv_consumer"}`) {
		t.Fatalf("decode %v %v", cmd, err)
	}
	seat.AuxPort = 0
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "side channel port") {
		t.Fatalf("no side channel: %v", err)
	}
	seat.AuxPort, in.InstallRecord = 5600, recorded(nil)
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "NIXL connector") {
		t.Fatalf("no connector: %v", err)
	}
	replica := &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_REPLICAS, Role: RoleReplica, Address: "10.0.0.1", Port: 8000, Exposed: true}
	in.Seat = replica
	if cmd, err = rt.LaunchSeat(in); err != nil || !strings.HasPrefix(strings.Join(cmd.Args, " "), "serve /w --host 10.0.0.1 --port 8000") {
		t.Fatalf("replica %v %v", cmd, err)
	}
	in.Seat = &v1.SeatSpec{Shape: v1.Shape_SHAPE_SOLO, Role: RoleHead}
	if cmd, err = rt.LaunchSeat(in); err != nil || !strings.HasPrefix(strings.Join(cmd.Args, " "), "serve /w --host 127.0.0.1 --port 9") {
		t.Fatalf("solo %v %v", cmd, err)
	}
}

// The shape probes import the engine and take a minute; the relay probe imports the connector at
// either of its homes and the NIXL agent binding
func TestVLLMProbes(t *testing.T) {
	byKey := map[string]Probe{}
	for _, p := range (VLLM{}).Probes() {
		byKey[p.Key] = p
	}
	if byKey[factNNodes].Timeout != time.Minute || byKey[factNIXL].Timeout != time.Minute || byKey["version"].Timeout != 0 {
		t.Fatalf("timeouts %v %v", byKey[factNNodes].Timeout, byKey[factNIXL].Timeout)
	}
	if v, ok := byKey[factNNodes].Parse("  --nnodes NNODES, -n NNODES"); !ok || v != "--nnodes" {
		t.Fatalf("nnodes %q %v", v, ok)
	}
	nixl := byKey[factNIXL]
	script := nixl.Args[len(nixl.Args)-1]
	for _, want := range []string{
		"from vllm.distributed.kv_transfer.kv_connector.v1.nixl import NixlConnector",
		"from vllm.distributed.kv_transfer.kv_connector.v1.nixl_connector import NixlConnector",
		"from nixl._api import nixl_agent",
		"from nixl_rocm._api import nixl_agent",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the relay probe lacks %q:\n%s", want, script)
		}
	}
	if nixl.Command == nil || nixl.Command(Install{Path: "/venv/bin/vllm"}) == "" {
		t.Fatal("the relay probe runs in the install's python")
	}
	if v, ok := nixl.Parse("INFO nixl loaded\nimports NixlConnector\n"); !ok || v != "NixlConnector" {
		t.Fatalf("parse %q %v", v, ok)
	}
	if _, ok := nixl.Parse("ImportError: No module named nixl"); ok {
		t.Fatal("an import error is no connector")
	}
	shapes := (VLLM{}).Shapes(recorded(map[string]string{factNNodes: "--nnodes", factNIXL: "NixlConnector"}))
	if len(shapes) != 5 || shapes[2] != v1.Shape_SHAPE_CHAIN || shapes[3] != v1.Shape_SHAPE_LOCKSTEP || shapes[4] != v1.Shape_SHAPE_RELAY {
		t.Fatalf("shapes %v", shapes)
	}
}
