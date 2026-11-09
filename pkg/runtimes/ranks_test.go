package runtimes

import (
	"strconv"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A host of four GPUs and a plan that priced them last first, so rank devices follow the plan's order
func fourGPUs() (*v1.HostProfile, *v1.MemoryPlan) {
	profile := &v1.HostProfile{
		Devices: []*v1.Device{card("GPU-a", "0", "NVIDIA H100", 80), card("GPU-b", "1", "NVIDIA H100", 80), card("GPU-c", "2", "NVIDIA H100", 80), card("GPU-d", "3", "NVIDIA H100", 80)},
	}
	plan := &v1.MemoryPlan{}
	for _, id := range []string{"GPU-d", "GPU-c", "GPU-b", "GPU-a"} {
		profile.Pools = append(profile.Pools, &v1.MemoryPool{Id: id, Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: id})
		plan.Pools = append(plan.Pools, &v1.PoolUsage{PoolId: id, Kind: v1.PoolKind_POOL_KIND_DEVICE, UsedBytes: 10})
	}
	return profile, plan
}

// A model whose layers carry experts, which data parallel ranks share
func experts() *v1.Descriptor {
	d := layered(2)
	d.Groups = []*v1.TensorGroup{{Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER}, {Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS}}
	return d
}

// Each vLLM rank serves its own port on its own devices, expert ranks joining external load balancing
func TestVLLMRanks(t *testing.T) {
	profile, plan := fourGPUs()
	grpc := map[string]string{factGRPCPort: "--grpc-port"}
	cases := []struct {
		name      string
		params    map[string]string
		model     *v1.Descriptor
		span      int
		ports     []int
		devices   []string
		want      [][]string
		forbidden []string
	}{
		{
			name: "dense ranks serve as independent servers", params: map[string]string{"data_parallel_size": "2", "tensor_parallel_size": "2", "max_num_seqs": "64"}, model: layered(2),
			span: 6, ports: []int{8000, 8001}, devices: []string{"GPU-d,GPU-c", "GPU-b,GPU-a"},
			want: [][]string{
				{"--port 8000", "--tensor-parallel-size 2", `"endpoint":"tcp://*:8002"`, `"replay_endpoint":"tcp://*:8004"`},
				{"--port 8001", "--tensor-parallel-size 2", `"endpoint":"tcp://*:8003"`, `"replay_endpoint":"tcp://*:8005"`},
			},
			forbidden: []string{"--data-parallel"},
		},
		{
			name: "expert ranks join one data parallel group", params: map[string]string{"data_parallel_size": "2", "tensor_parallel_size": "2", "max_num_seqs": "64"}, model: experts(),
			span: 7, ports: []int{8000, 8001}, devices: []string{"GPU-d,GPU-c", "GPU-b,GPU-a"},
			want: [][]string{
				{"--port 8000", "--data-parallel-size 2", "--data-parallel-rank 0 --data-parallel-size-local 1 --data-parallel-address 127.0.0.1 --data-parallel-rpc-port 8002", `"endpoint":"tcp://*:8003"`, `"replay_endpoint":"tcp://*:8005"`},
				{"--port 8001", "--data-parallel-size 2", "--data-parallel-rank 1 --data-parallel-size-local 1 --data-parallel-address 127.0.0.1 --data-parallel-rpc-port 8002", `"endpoint":"tcp://*:8003"`, `"replay_endpoint":"tcp://*:8005"`},
			},
		},
		{
			name: "four expert ranks on one device each serving gRPC", params: map[string]string{"data_parallel_size": "4", "max_num_seqs": "64", "grpc": "true"}, model: experts(),
			span: 17, ports: []int{8000, 8002, 8004, 8006}, devices: []string{"GPU-d", "GPU-c", "GPU-b", "GPU-a"},
			want: [][]string{
				{"--port 8000", "--grpc-port 8001", "--data-parallel-rank 0", "--data-parallel-rpc-port 8008", `"endpoint":"tcp://*:8009"`, `"replay_endpoint":"tcp://*:8013"`},
				{"--port 8002", "--grpc-port 8003", "--data-parallel-rank 1", "--data-parallel-rpc-port 8008", `"endpoint":"tcp://*:8009"`},
				{"--port 8004", "--grpc-port 8005", "--data-parallel-rank 2", "--data-parallel-rpc-port 8008", `"endpoint":"tcp://*:8009"`},
				{"--port 8006", "--grpc-port 8007", "--data-parallel-rank 3", "--data-parallel-rpc-port 8008", `"endpoint":"tcp://*:8009"`},
			},
		},
		{
			name: "one rank keeps every launch device", params: map[string]string{"max_num_seqs": "64"}, model: experts(),
			span: 3, ports: []int{8000}, devices: []string{"GPU-a,GPU-b,GPU-c,GPU-d"},
			want:      [][]string{{"--port 8000", `"endpoint":"tcp://*:8001"`, `"replay_endpoint":"tcp://*:8002"`}},
			forbidden: []string{"--data-parallel"},
		},
	}
	for _, c := range cases {
		params, err := Resolve(VLLM{}, c.params)
		if err != nil {
			t.Fatal(err)
		}
		in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights_dir": "/w"}, Host: "127.0.0.1", Port: 8000, Install: Install{Path: "/venv/bin/vllm"}, Plan: plan, Profile: profile, Descriptor: c.model, InstallRecord: recorded(grpc)}
		if got := (VLLM{}).PortSpan(in); got != c.span {
			t.Errorf("%s: span %d, want %d", c.name, got, c.span)
		}
		for rank, port := range c.ports {
			in.Rank = rank
			cmd, err := VLLM{}.Launch(in)
			if err != nil {
				t.Fatalf("%s rank %d: %v", c.name, rank, err)
			}
			line := strings.Join(cmd.Args, " ")
			if cmd.Port != port || cmd.Env["CUDA_VISIBLE_DEVICES"] != c.devices[rank] {
				t.Errorf("%s rank %d: port %d devices %s, want %d %s", c.name, rank, cmd.Port, cmd.Env["CUDA_VISIBLE_DEVICES"], port, c.devices[rank])
			}
			for _, want := range c.want[rank] {
				if !strings.Contains(line, want) {
					t.Errorf("%s rank %d: %q lacks %q", c.name, rank, line, want)
				}
			}
			for _, bad := range c.forbidden {
				if strings.Contains(line, bad) {
					t.Errorf("%s rank %d: %q holds %q", c.name, rank, line, bad)
				}
			}
			if got := (VLLM{}).Ranks(cmd.Params); got != len(c.ports) {
				t.Errorf("%s rank %d: recorded params count %d ranks", c.name, rank, got)
			}
			if (VLLM{}).Capacity(cmd.Params) != 64 {
				t.Errorf("%s rank %d: capacity %d, one rank's sequences", c.name, rank, (VLLM{}).Capacity(cmd.Params))
			}
			if scope := ScopeOf(in, cmd); scope.Port != port {
				t.Errorf("%s rank %d: templates see port %d", c.name, rank, scope.Port)
			}
		}
	}
}

// A vLLM rank group refuses an outside rank, too few devices, and a seat spreading one engine over nodes
func TestVLLMRankRefusals(t *testing.T) {
	profile, plan := fourGPUs()
	params, err := Resolve(VLLM{}, map[string]string{"data_parallel_size": "3", "tensor_parallel_size": "2", "max_num_seqs": "8"})
	if err != nil {
		t.Fatal(err)
	}
	in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights_dir": "/w"}, Host: "127.0.0.1", Port: 8000, Install: Install{Path: "/venv/bin/vllm"}, Plan: plan, Profile: profile, Descriptor: layered(2), InstallRecord: recorded(nil)}
	if _, err := (VLLM{}).Launch(in); err == nil || !strings.Contains(err.Error(), "need 6 accelerators and the launch holds 4") {
		t.Fatalf("three ranks of two devices on four: %v", err)
	}
	in.Params["data_parallel_size"] = int64(2)
	in.Rank = 2
	if _, err := (VLLM{}).Launch(in); err == nil || !strings.Contains(err.Error(), "rank 2 of a launch of 2") {
		t.Fatalf("a rank past the group: %v", err)
	}
	in.Rank = 0
	for _, shape := range []v1.Shape{v1.Shape_SHAPE_CHAIN, v1.Shape_SHAPE_LOCKSTEP, v1.Shape_SHAPE_RELAY} {
		in.Seat = &v1.SeatSpec{Shape: shape, Role: VLLM{}.Roles(shape)[0].Name, Rendezvous: "10.0.0.1:29500", AuxPort: 5600, Count: 1}
		if _, err := (VLLM{}).LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "data parallel ranks, which run in solo and replicas seats") {
			t.Errorf("%s seat with ranks: %v", shape, err)
		}
	}
}

// Every SGLang rank is a server of its own with data parallel one, on its own port and devices
func TestSGLangRanks(t *testing.T) {
	profile, plan := fourGPUs()
	cases := []struct {
		name    string
		params  map[string]string
		span    int
		ports   []int
		devices []string
		grpc    []string
	}{
		{name: "two ranks of two devices", params: map[string]string{"dp_size": "2", "tp_size": "2"}, span: 6, ports: []int{30000, 30001}, devices: []string{"GPU-d,GPU-c", "GPU-b,GPU-a"}, grpc: []string{`"endpoint":"tcp://127.0.0.1:30002"`, `"endpoint":"tcp://127.0.0.1:30003"`}},
		{name: "two ranks serving gRPC", params: map[string]string{"dp_size": "2", "grpc": "true"}, span: 8, ports: []int{30000, 30002}, devices: []string{"GPU-d", "GPU-c"}, grpc: []string{"--grpc-port 30001", "--grpc-port 30003"}},
		{name: "one rank", params: nil, span: 3, ports: []int{30000}, devices: []string{"GPU-a,GPU-b,GPU-c,GPU-d"}, grpc: []string{`"bind":true,"endpoint":"tcp://127.0.0.1:30001","publisher":"zmq","replay_endpoint":"tcp://127.0.0.1:30002","topic":"kv-events"`}},
	}
	for _, c := range cases {
		params, err := Resolve(SGLang{}, c.params)
		if err != nil {
			t.Fatal(err)
		}
		in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights_dir": "/w"}, Host: "127.0.0.1", Port: 30000, Install: Install{Path: "/venv/bin/python"}, Plan: plan, Profile: profile, Descriptor: experts(), InstallRecord: recorded(map[string]string{factGRPCPort: "--grpc-port"})}
		if got := (SGLang{}).PortSpan(in); got != c.span {
			t.Errorf("%s: span %d, want %d", c.name, got, c.span)
		}
		for rank, port := range c.ports {
			in.Rank = rank
			cmd, err := SGLang{}.Launch(in)
			if err != nil {
				t.Fatalf("%s rank %d: %v", c.name, rank, err)
			}
			line := strings.Join(cmd.Args, " ")
			if cmd.Port != port || !strings.Contains(line, "--port "+strconv.Itoa(port)) || cmd.Env["CUDA_VISIBLE_DEVICES"] != c.devices[rank] || strings.Contains(line, "--dp-size") {
				t.Errorf("%s rank %d: %q port %d devices %s", c.name, rank, line, cmd.Port, cmd.Env["CUDA_VISIBLE_DEVICES"])
			}
			if c.grpc != nil && !strings.Contains(line, c.grpc[rank]) {
				t.Errorf("%s rank %d: %q lacks %q", c.name, rank, line, c.grpc[rank])
			}
			if got := (SGLang{}).Ranks(cmd.Params); got != len(c.ports) {
				t.Errorf("%s rank %d: recorded params count %d ranks", c.name, rank, got)
			}
		}
	}
	in := Launch{Params: estimate.Params{"dp_size": int64(2)}, Seat: &v1.SeatSpec{Shape: v1.Shape_SHAPE_RELAY, Role: RolePrefill, AuxPort: 1}}
	if _, err := (SGLang{}).LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "data parallel ranks") {
		t.Fatalf("a relay seat with ranks: %v", err)
	}
}
