package runtimes

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestSGLangRankSeats(t *testing.T) {
	rt := SGLang{}
	params, err := Resolve(rt, map[string]string{"speculative_algorithm": "EAGLE", "speculative_draft_model_path": "/d", "speculative_num_steps": "3", "speculative_eagle_topk": "1", "speculative_num_draft_tokens": "4"})
	if err != nil {
		t.Fatal(err)
	}
	in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights_dir": "/w"}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/venv/bin/python"}, Devices: []*v1.Device{gpu("GPU-0", "0"), gpu("GPU-1", "1")}, Descriptor: layered(32), Seat: chainSeat(0), InstallRecord: recorded(map[string]string{factNNodes: "--nnodes"})}
	cmd, err := rt.LaunchSeat(in)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd.Args, " ")
	if cmd.Command != "/venv/bin/python" || !strings.HasPrefix(line, "-m sglang.launch_server --model-path /w --host 10.0.0.1 --port 8000") {
		t.Fatalf("head %s %s", cmd.Command, line)
	}
	for _, want := range []string{"--tp-size 2", "--chunked-prefill-size 8192", "--pp-size 2 --nnodes 2 --node-rank 0 --dist-init-addr 10.0.0.5:29500 --watchdog-timeout 30 --enable-dynamic-chunking --disable-overlap-schedule"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %s in %s", want, line)
		}
	}
	if strings.Contains(line, "--speculative") {
		t.Fatalf("a chain rank runs no speculative decoding: %s", line)
	}
	for key, want := range map[string]string{
		"SGLANG_PP_LAYER_PARTITION": "20,12",
		"NCCL_SOCKET_IFNAME":        "eth0", "GLOO_SOCKET_IFNAME": "eth0",
		"NCCL_NET": "IB", "NCCL_IB_HCA": "mlx5_0,mlx5_1", "NCCL_IB_MERGE_NICS": "1", "NCCL_NET_PLUGIN": "none",
		"NCCL_DEBUG": "INFO", "NCCL_DEBUG_SUBSYS": "INIT,NET",
		"TORCH_NCCL_HEARTBEAT_TIMEOUT_SEC": "30", "TORCH_NCCL_ENABLE_MONITORING": "0",
		"CUDA_VISIBLE_DEVICES": "GPU-0,GPU-1",
	} {
		if cmd.Env[key] != want {
			t.Errorf("%s = %q, want %q", key, cmd.Env[key], want)
		}
	}
	if _, set := cmd.Env["VLLM_HOST_IP"]; set {
		t.Fatal("sglang takes no vllm variable")
	}
	if cmd.Params["pp_layer_partition"] != "20,12" || cmd.Params["dist_init_addr"] != "10.0.0.5:29500" || cmd.Params["pp_size"] != "2" || cmd.Params["node_rank"] != "0" {
		t.Fatalf("params %v", cmd.Params)
	}
	in.Seat = chainSeat(1)
	cmd, err = rt.LaunchSeat(in)
	if err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--node-rank 1 --dist-init-addr 10.0.0.5:29500") || cmd.Env["SGLANG_PP_LAYER_PARTITION"] != "20,12" || cmd.Env["NCCL_IB_MERGE_NICS"] != "" {
		t.Fatalf("rank 1 %v %v", cmd, err)
	}
	// Lockstep keeps the speculative settings and partitions nothing.
	in.Seat = &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_LOCKSTEP, Role: RoleHead, Rank: 0, Count: 3, Rendezvous: "10.0.0.5:29500", Address: "10.0.0.1", Port: 8000, Exposed: true,
		Peers: []*v1.SeatPeer{{NodeId: "n2", Role: RoleRank, Rank: 1, Devices: 2}, {NodeId: "n3", Role: RoleRank, Rank: 2, Devices: 2}}}
	cmd, err = rt.LaunchSeat(in)
	if err != nil {
		t.Fatal(err)
	}
	line = strings.Join(cmd.Args, " ")
	for _, want := range []string{"--tp-size 6", "--pp-size 1 --nnodes 3 --node-rank 0 --dist-init-addr 10.0.0.5:29500 --watchdog-timeout 30", "--speculative-algorithm EAGLE", "--speculative-draft-model-path /d", "--speculative-num-steps 3", "--speculative-eagle-topk 1", "--speculative-num-draft-tokens 4"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %s in %s", want, line)
		}
	}
	if strings.Contains(line, "--enable-dynamic-chunking") || strings.Contains(line, "--disable-overlap-schedule") {
		t.Fatalf("pipeline flags on one stage: %s", line)
	}
	if _, set := cmd.Env["SGLANG_PP_LAYER_PARTITION"]; set {
		t.Fatal("lockstep partitions no layers")
	}
	cases := []struct {
		name string
		edit func()
		want string
	}{
		{"no nnodes", func() { in.InstallRecord = recorded(nil) }, "does not accept --nnodes"},
		{"no rendezvous", func() { in.Seat.Rendezvous = "10.0.0.5" }, "rendezvous address"},
		{"no gpus", func() { in.Devices = nil }, "at least one GPU"},
		{"count", func() { in.Seat.Count = 4 }, "counts 4 ranks and lists 1 peers"},
		{"layers", func() { in.Descriptor = layered(33) }, "cover 32 layers of a 33 layer model"},
	}
	for _, c := range cases {
		in.Seat, in.Devices, in.Descriptor, in.InstallRecord = chainSeat(0), []*v1.Device{gpu("GPU-0", "0"), gpu("GPU-1", "1")}, layered(32), recorded(map[string]string{factNNodes: "--nnodes"})
		c.edit()
		if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestSGLangRelaySeats(t *testing.T) {
	rt := SGLang{}
	params, _ := Resolve(rt, nil)
	seat := &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_RELAY, Role: RolePrefill, Rank: 0, Count: 2, Address: "10.0.0.1", Port: 8000, AuxPort: 8998, Interface: "ib0", RdmaDevices: []string{"mlx5_0", "mlx5_1"}, Peers: []*v1.SeatPeer{{NodeId: "n2", Role: RoleDecode, Rank: 1, Devices: 1}}}
	in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights_dir": "/w"}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/venv/bin/python"}, Devices: []*v1.Device{gpu("GPU-0", "0")}, Seat: seat, InstallRecord: recorded(map[string]string{factDisaggregation: "--disaggregation-mode", factNIXL: "NixlKVManager"})}
	cmd, err := rt.LaunchSeat(in)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd.Args, " ")
	if !strings.HasPrefix(line, "-m sglang.launch_server --model-path /w --host 127.0.0.1 --port 9") || !strings.HasSuffix(line, "--disaggregation-mode prefill --disaggregation-transfer-backend nixl --watchdog-timeout 30 --disaggregation-bootstrap-port 8998 --disaggregation-ib-device mlx5_0,mlx5_1") {
		t.Fatalf("prefill %s", line)
	}
	if cmd.Env["NCCL_IB_HCA"] != "mlx5_0,mlx5_1" || cmd.Env["NCCL_IB_MERGE_NICS"] != "1" || cmd.Env["NCCL_SOCKET_IFNAME"] != "ib0" || cmd.Params["disaggregation_mode"] != RolePrefill || cmd.Params["disaggregation_ib_device"] != "mlx5_0,mlx5_1" {
		t.Fatalf("prefill env %v params %v", cmd.Env, cmd.Params)
	}
	seat.Role, seat.RdmaDevices = RoleDecode, nil
	cmd, err = rt.LaunchSeat(in)
	if err != nil || !strings.HasSuffix(strings.Join(cmd.Args, " "), "--disaggregation-mode decode --disaggregation-transfer-backend nixl --watchdog-timeout 30") || cmd.Env["NCCL_NET"] != "" {
		t.Fatalf("decode %v %v", cmd, err)
	}
	seat.AuxPort = 0
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "bootstrap port") {
		t.Fatalf("no bootstrap port: %v", err)
	}
	seat.AuxPort, in.InstallRecord = 8998, recorded(map[string]string{factDisaggregation: "--disaggregation-mode"})
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "NIXL disaggregation") {
		t.Fatalf("no nixl: %v", err)
	}
}

func TestSGLangProbes(t *testing.T) {
	byKey := map[string]Probe{}
	for _, p := range (SGLang{}).Probes() {
		byKey[p.Key] = p
	}
	for _, key := range []string{"version", factNNodes, factDisaggregation, factNIXL} {
		if byKey[key].Timeout != time.Minute {
			t.Errorf("%s takes a minute, got %v", key, byKey[key].Timeout)
		}
	}
	script := byKey[factNIXL].Args[len(byKey[factNIXL].Args)-1]
	for _, want := range []string{"import sglang.srt.disaggregation.nixl.conn", "from nixl._api import nixl_agent", "from nixl_rocm._api import nixl_agent"} {
		if !strings.Contains(script, want) {
			t.Errorf("the relay probe lacks %q:\n%s", want, script)
		}
	}
	if v, ok := byKey[factNIXL].Parse("imports NixlKVManager"); !ok || v != "NixlKVManager" {
		t.Fatalf("parse %q %v", v, ok)
	}
	shapes := (SGLang{}).Shapes(recorded(map[string]string{factNNodes: "--nnodes", factDisaggregation: "--disaggregation-mode"}))
	if len(shapes) != 4 || shapes[3] != v1.Shape_SHAPE_LOCKSTEP {
		t.Fatalf("no relay without nixl: %v", shapes)
	}
}
