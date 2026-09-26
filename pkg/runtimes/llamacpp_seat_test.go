package runtimes

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestLlamaCppStageCommand(t *testing.T) {
	rt := LlamaCpp{}
	cache := filepath.Join(t.TempDir(), "f1")
	seat := &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_CHAIN, Role: RoleStage, Rank: 1, CacheDir: cache, Address: "10.0.0.2", Port: 50052}
	facts := map[string]string{factRPC: "/bin/rpc-server", "devices": "CUDA0,CUDA1"}
	params, err := Resolve(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := Launch{Name: "m/stage1", Params: params, Host: "127.0.0.1", Port: 5005, Install: Install{Path: "/bin/llama-server"}, Devices: []*v1.Device{gpu("GPU-1", "1")}, Seat: seat, InstallRecord: recorded(facts)}
	cmd, err := rt.LaunchSeat(in)
	if err != nil {
		t.Fatal(err)
	}
	cores := strconv.Itoa(runtime.NumCPU())
	if cmd.Command != "/bin/rpc-server" || strings.Join(cmd.Args, " ") != "-H 127.0.0.1 -p 5005 -c -d CUDA1 -t "+cores {
		t.Fatalf("stage %s %v", cmd.Command, cmd.Args)
	}
	if cmd.Env["LLAMA_CACHE"] != cache || cmd.Env["CUDA_VISIBLE_DEVICES"] != "GPU-1" || cmd.Env["GGML_CUDA_DISABLE_GRAPHS"] != "" || cmd.Params["threads"] != cores || cmd.Params["port"] != "5005" {
		t.Fatalf("stage env %v params %v", cmd.Env, cmd.Params)
	}
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Fatal("the cache directory is made for the tensors the head streams")
	}
	// A thread count the params set, the graph workaround a triage fix sets, and the mesh address when exposed.
	params["threads"], params["cuda_disable_graphs"] = int64(6), true
	seat.Exposed = true
	cmd, err = rt.LaunchSeat(in)
	if err != nil {
		t.Fatal(err)
	}
	if line := strings.Join(cmd.Args, " "); line != "-H 10.0.0.2 -p 50052 -c -d CUDA1 -t 6" || cmd.Env["GGML_CUDA_DISABLE_GRAPHS"] != "1" {
		t.Fatalf("exposed stage %s %v", line, cmd.Env)
	}
	// A CPU only stage serves the CPU backend.
	in.Devices = []*v1.Device{{Id: "cpu", Kind: v1.DeviceKind_DEVICE_KIND_CPU}}
	if cmd, err = rt.LaunchSeat(in); err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "-d CPU") {
		t.Fatalf("cpu stage %v %v", cmd, err)
	}
	in.InstallRecord = recorded(map[string]string{})
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "no rpc server") {
		t.Fatalf("no rpc server: %v", err)
	}
	in.InstallRecord, seat.CacheDir = recorded(facts), ""
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "cache directory") {
		t.Fatalf("no cache dir: %v", err)
	}
}

func TestLlamaCppChainHead(t *testing.T) {
	rt := LlamaCpp{}
	facts := map[string]string{factRPC: "/bin/rpc-server", factRPCFlag: "--rpc", factFitFlag: "--fit", "devices": "CUDA0"}
	params, err := Resolve(rt, map[string]string{"n_ctx": "8192"})
	if err != nil {
		t.Fatal(err)
	}
	// Peers arrive in any order; the stages take the rpc list in rank order, their devices first.
	seat := &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_CHAIN, Role: RoleHead, Rank: 0, Count: 3,
		Devices:     []string{"a/GPU-a0", "a/GPU-a1", "b/GPU-b0", "GPU-h0"},
		DeviceBytes: []uint64{10 << 30, 20 << 30, 30 << 30, 40 << 30},
		Peers: []*v1.SeatPeer{
			{NodeId: "b", Role: RoleStage, Rank: 2, Address: "10.0.0.3:50052", Devices: 1},
			{NodeId: "a", Role: RoleStage, Rank: 1, Address: "10.0.0.2:50052", Devices: 2},
		}}
	in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights": "/w.gguf"}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/llama-server"}, Devices: []*v1.Device{gpu("GPU-h0", "0")}, Descriptor: layered(80), Seat: seat, InstallRecord: recorded(facts)}
	cmd, err := rt.LaunchSeat(in)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd.Args, " ")
	for _, want := range []string{
		"--model /w.gguf --host 127.0.0.1 --port 9",
		"--device RPC0,RPC1,RPC2,CUDA0",
		"--n-gpu-layers 81",
		"--rpc 10.0.0.2:50052,10.0.0.3:50052",
		"--tensor-split 10737418240,21474836480,32212254720,42949672960",
		"--fit off",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %s in %s", want, line)
		}
	}
	if cmd.Params["rpc"] != "10.0.0.2:50052,10.0.0.3:50052" || cmd.Params["tensor_split"] != "10737418240,21474836480,32212254720,42949672960" || cmd.Params["n_gpu_layers"] != "81" || cmd.Params["device"] != "RPC0,RPC1,RPC2,CUDA0" {
		t.Fatalf("params %v", cmd.Params)
	}
	// llama-server resolves --device as it parses it, so the rpc list comes first.
	if rpc, dev := strings.Index(line, "--rpc "), strings.Index(line, "--device "); rpc < 0 || dev < 0 || rpc > dev {
		t.Fatalf("--rpc at %d, --device at %d in %s", rpc, dev, line)
	}
	// The head's own devices follow the plan's order, not the profile's.
	in.Devices = []*v1.Device{gpu("GPU-h1", "1"), gpu("GPU-h0", "0")}
	seat.Devices = []string{"a/GPU-a0", "a/GPU-a1", "b/GPU-b0", "GPU-h0", "GPU-h1"}
	seat.DeviceBytes = []uint64{1, 2, 3, 4, 5}
	if cmd, err = rt.LaunchSeat(in); err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--device RPC0,RPC1,RPC2,CUDA0,CUDA1 ") {
		t.Fatalf("own device order %v %v", cmd, err)
	}
	// A head without an accelerator lists the stages' devices alone.
	in.Devices = nil
	seat.Devices, seat.DeviceBytes = []string{"a/GPU-a0", "a/GPU-a1", "b/GPU-b0"}, []uint64{1, 2, 3}
	if cmd, err = rt.LaunchSeat(in); err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--device RPC0,RPC1,RPC2 ") || !strings.Contains(strings.Join(cmd.Args, " "), "--tensor-split 1,2,3") {
		t.Fatalf("cpu head %v %v", cmd, err)
	}
	cases := []struct {
		name string
		edit func()
		want string
	}{
		{"shares", func() { seat.DeviceBytes = []uint64{1, 2} }, "2 device shares for 3 devices"},
		{"unqualified stage device", func() { seat.Devices = []string{"GPU-a0", "a/GPU-a1", "b/GPU-b0"} }, "stage 1 on a exposes 2 devices"},
		{"stage short", func() { seat.Devices = []string{"a/GPU-a0", "a/GPU-a1"}; seat.DeviceBytes = []uint64{1, 2} }, "stage 2 on b exposes 1 devices"},
		{"unknown own device", func() {
			seat.Devices = []string{"a/GPU-a0", "a/GPU-a1", "b/GPU-b0", "GPU-x"}
			seat.DeviceBytes = []uint64{1, 2, 3, 4}
		}, "device GPU-x after the stages' devices"},
		{"cpu in the split", func() {
			in.Devices = []*v1.Device{{Id: "cpu", Kind: v1.DeviceKind_DEVICE_KIND_CPU}}
			seat.Devices = []string{"a/GPU-a0", "a/GPU-a1", "b/GPU-b0", "cpu"}
			seat.DeviceBytes = []uint64{1, 2, 3, 4}
		}, "lists the CPU cpu"},
		{"no layers", func() { in.Descriptor = layered(0) }, "counts no layers"},
		{"no stages", func() { seat.Peers = nil }, "no stage peers"},
		{"no rpc flag", func() { in.InstallRecord = recorded(map[string]string{factRPC: "/bin/rpc-server"}) }, "does not accept --rpc"},
		{"stage without devices", func() { seat.Peers[1].Devices = 0 }, "exposes no device"},
	}
	for _, c := range cases {
		in.Devices, in.Descriptor, in.InstallRecord = nil, layered(80), recorded(facts)
		seat.Devices, seat.DeviceBytes = []string{"a/GPU-a0", "a/GPU-a1", "b/GPU-b0"}, []uint64{1, 2, 3}
		seat.Peers = []*v1.SeatPeer{{NodeId: "b", Role: RoleStage, Rank: 2, Address: "10.0.0.3:50052", Devices: 1}, {NodeId: "a", Role: RoleStage, Rank: 1, Address: "10.0.0.2:50052", Devices: 2}}
		c.edit()
		if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestLlamaCppDraftHead(t *testing.T) {
	rt := LlamaCpp{}
	facts := map[string]string{factRPC: "/bin/rpc-server", factRPCFlag: "--rpc", factDraftDevice: "--spec-draft-device", factDraftModel: "--spec-draft-model", factDraftMax: "--spec-draft-n-max", "devices": "CUDA0"}
	params, _ := Resolve(rt, map[string]string{"draft_max": "4"})
	seat := &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_DRAFT, Role: RoleHead, Rank: 0, Count: 2, Peers: []*v1.SeatPeer{{NodeId: "a", Role: RoleStage, Rank: 1, Address: "10.0.0.2:50052", Devices: 1}}}
	in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights": "/w.gguf"}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/llama-server"}, Devices: []*v1.Device{gpu("GPU-h0", "0")}, Descriptor: layered(32), Seat: seat, InstallRecord: recorded(facts), Draft: "/d.gguf"}
	cmd, err := rt.LaunchSeat(in)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd.Args, " ")
	if !strings.HasSuffix(line, "--rpc 10.0.0.2:50052 --spec-draft-model /d.gguf --spec-draft-device RPC0") || !strings.Contains(line, "--spec-draft-n-max 4") || strings.Contains(line, "--draft-max") || strings.Contains(line, "--tensor-split") {
		t.Fatalf("draft head %s", line)
	}
	if cmd.Params["draft_device"] != "RPC0" || cmd.Params["draft_model"] != "/d.gguf" || cmd.Params["rpc"] != "10.0.0.2:50052" {
		t.Fatalf("params %v", cmd.Params)
	}
	in.Draft = ""
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "draft model's weights") {
		t.Fatalf("no draft: %v", err)
	}
	in.Draft, in.InstallRecord = "/d.gguf", recorded(map[string]string{factRPC: "/bin/rpc-server", factRPCFlag: "--rpc"})
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "draft device flag") {
		t.Fatalf("no draft flags: %v", err)
	}
}

func TestLlamaCppDraftLimit(t *testing.T) {
	rt := LlamaCpp{}
	for _, c := range []struct {
		name, draft, max, flag, want string
	}{
		{name: "ordinary default"},
		{name: "ordinary explicit limit", max: "9"},
		{name: "draft disabled", draft: None},
		{name: "modern default", draft: "/draft.gguf", flag: "--spec-draft-n-max", want: "5"},
		{name: "modern override", draft: "/draft.gguf", max: "9", flag: "--spec-draft-n-max", want: "9"},
		{name: "legacy", draft: "/draft.gguf", flag: "--draft-max", want: "5"},
	} {
		t.Run(c.name, func(t *testing.T) {
			overrides := map[string]string{"draft_model": c.draft}
			if c.max != "" {
				overrides["draft_max"] = c.max
			}
			params, err := Resolve(rt, overrides)
			if err != nil {
				t.Fatal(err)
			}
			rt.Policy().Solve(&estimate.Scope{Params: params})
			in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights": "/w.gguf"}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/llama-server"}, InstallRecord: recorded(map[string]string{factDraftModel: "--spec-draft-model", factDraftMax: c.flag})}
			cmd, err := rt.Launch(in)
			if err != nil {
				t.Fatal(err)
			}
			line := strings.Join(cmd.Args, " ")
			if c.want == "" {
				if strings.Contains(line, "draft") || cmd.Params["draft_max"] != "" {
					t.Fatalf("draft options without a draft model: %s %v", line, cmd.Params)
				}
			} else {
				if !strings.Contains(line, c.flag+" "+c.want) || cmd.Params["draft_max"] != c.want {
					t.Fatalf("draft limit: %s %v", line, cmd.Params)
				}
				if c.flag != "--draft-max" && strings.Contains(line, "--draft-max") {
					t.Fatalf("removed flag in modern command: %s", line)
				}
				in.InstallRecord = recorded(map[string]string{factDraftModel: "--spec-draft-model"})
				if _, err := rt.Launch(in); err == nil || !strings.Contains(err.Error(), "draft token limit flag") {
					t.Fatalf("unprobed draft limit: %v", err)
				}
			}
			if params.Int("draft_max") == 0 {
				t.Fatal("launch modified the requested draft limit")
			}
		})
	}
}

func TestLlamaCppRelayAndReplicaSeats(t *testing.T) {
	rt := LlamaCpp{}
	facts := map[string]string{factSlotSave: "--slot-save-path", factSlotsFlag: "--slots"}
	params, _ := Resolve(rt, nil)
	slots := filepath.Join(t.TempDir(), "slots", "f1")
	seat := &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_RELAY, Role: RolePrefill, Rank: 0, Count: 2, SlotDir: slots, Address: "10.0.0.1", Port: 7000}
	in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights": "/w.gguf"}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/llama-server"}, Seat: seat, InstallRecord: recorded(facts)}
	cmd, err := rt.LaunchSeat(in)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd.Args, " ")
	if !strings.HasPrefix(line, "--model /w.gguf --host 127.0.0.1 --port 9") || !strings.HasSuffix(line, "--slot-save-path "+slots+" --slots") || cmd.Params["slot_save_path"] != slots {
		t.Fatalf("relay %s %v", line, cmd.Params)
	}
	if info, err := os.Stat(slots); err != nil || !info.IsDir() {
		t.Fatal("the slot directory is made")
	}
	seat.Exposed = true
	if cmd, err = rt.LaunchSeat(in); err != nil || !strings.HasPrefix(strings.Join(cmd.Args, " "), "--model /w.gguf --host 10.0.0.1 --port 7000") {
		t.Fatalf("exposed relay %v %v", cmd, err)
	}
	seat.SlotDir = ""
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "slot directory") {
		t.Fatalf("no slot dir: %v", err)
	}
	seat.SlotDir, in.InstallRecord = slots, recorded(nil)
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "--slot-save-path") {
		t.Fatalf("no slot flag: %v", err)
	}
	replica := &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_REPLICAS, Role: RoleReplica, Address: "10.0.0.1", Port: 7000, Exposed: true}
	in.Seat = replica
	if cmd, err = rt.LaunchSeat(in); err != nil || !strings.HasPrefix(strings.Join(cmd.Args, " "), "--model /w.gguf --host 10.0.0.1 --port 7000") {
		t.Fatalf("replica %v %v", cmd, err)
	}
	if _, err := rt.LaunchSeat(Launch{}); err == nil {
		t.Fatal("no seat block")
	}
}

// Direct reads are rendered on every llama-server command from the install's spelling of the flag
func TestLlamaCppDirectIO(t *testing.T) {
	rt := LlamaCpp{}
	params, _ := Resolve(rt, map[string]string{"direct_io": "true"})
	in := Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights": "/w.gguf"}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/llama-server"}}
	in.InstallRecord = recorded(map[string]string{factDirectIO: "--direct-io"})
	if cmd, err := rt.Launch(in); err != nil || !strings.HasSuffix(strings.Join(cmd.Args, " "), " --direct-io") || cmd.Params["direct_io"] != "true" {
		t.Fatalf("direct-io %v %v", cmd, err)
	}
	in.InstallRecord = recorded(map[string]string{factDirectIO: "--load-mode"})
	if cmd, err := rt.Launch(in); err != nil || !strings.HasSuffix(strings.Join(cmd.Args, " "), " --load-mode dio") {
		t.Fatalf("load-mode dio %v %v", cmd, err)
	}
	in.InstallRecord = recorded(nil)
	if _, err := rt.Launch(in); err == nil || !strings.Contains(err.Error(), "direct io flag") {
		t.Fatalf("no flag: %v", err)
	}
	params["direct_io"] = false
	if cmd, err := rt.Launch(in); err != nil || strings.Contains(strings.Join(cmd.Args, " "), "--load-mode") || strings.Contains(strings.Join(cmd.Args, " "), "--direct-io") {
		t.Fatalf("off %v %v", cmd, err)
	}
	// The chain head takes the flag through the same command.
	params["direct_io"] = true
	in.InstallRecord = recorded(map[string]string{factRPC: "/bin/rpc-server", factRPCFlag: "--rpc", factDirectIO: "--load-mode", "devices": "CUDA0"})
	in.Descriptor = layered(32)
	in.Devices = []*v1.Device{gpu("GPU-h0", "0")}
	in.Seat = &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_CHAIN, Role: RoleHead, Count: 2, Devices: []string{"a/GPU-a0", "GPU-h0"}, DeviceBytes: []uint64{1, 2}, Peers: []*v1.SeatPeer{{NodeId: "a", Role: RoleStage, Rank: 1, Address: "10.0.0.2:50052", Devices: 1}}}
	if cmd, err := rt.LaunchSeat(in); err != nil || !strings.Contains(strings.Join(cmd.Args, " "), " --load-mode dio ") {
		t.Fatalf("chain head direct io %v %v", cmd, err)
	}
}

func TestLlamaCppShapesAndProbes(t *testing.T) {
	rt := LlamaCpp{}
	shapes := func(facts map[string]string) string {
		var out []string
		for _, s := range rt.Shapes(recorded(facts)) {
			out = append(out, shapeWord(s))
		}
		return strings.Join(out, ",")
	}
	if shapes(nil) != "solo,replicas" {
		t.Fatalf("bare %s", shapes(nil))
	}
	if got := shapes(map[string]string{factRPC: "/bin/rpc-server", factRPCFlag: "--rpc"}); got != "solo,replicas,chain,draft" {
		t.Fatalf("chain, and a draft stage: %s", got)
	}
	if got := shapes(map[string]string{factRPC: "/bin/rpc-server", factRPCFlag: "--rpc", factDraftDevice: "--spec-draft-device", factDraftModel: "--spec-draft-model", factSlotSave: "--slot-save-path"}); got != "solo,replicas,chain,draft,relay" {
		t.Fatalf("all %s", got)
	}
	if got := shapes(map[string]string{factRPCFlag: "--rpc", factDraftDevice: "--spec-draft-device", factDraftModel: "--spec-draft-model"}); got != "solo,replicas,chain,draft" {
		t.Fatalf("no rpc server, still a head: %s", got)
	}
	byKey := map[string]Probe{}
	for _, p := range rt.Probes() {
		byKey[p.Key] = p
	}
	if byKey[factRPC].Files == nil || byKey[factRPC].Parse != nil {
		t.Fatal("the rpc server is a file probe")
	}
	if c := byKey[factRPC].Files(Install{Path: "/opt/llama/bin/llama-server"}); len(c) != 3 || c[0] != "/opt/llama/bin/rpc-server" || c[1] != "/opt/llama/bin/ggml-rpc-server" {
		t.Fatalf("candidates %v", c)
	}
	if v, ok := byKey[factDirectIO].Parse("  -lm, --load-mode MODE   model loading mode\n- dio: use DirectIO if available"); !ok || v != "--load-mode" {
		t.Fatalf("load-mode spelling %q %v", v, ok)
	}
	if v, ok := byKey[factDirectIO].Parse("  -dio, --direct-io   use direct I/O if available"); !ok || v != "--direct-io" {
		t.Fatalf("direct-io spelling %q %v", v, ok)
	}
	if _, ok := byKey[factDirectIO].Parse("  -ngl N"); ok {
		t.Fatal("no direct io")
	}
	if v, ok := byKey[factDraftDevice].Parse("--spec-draft-device DEV"); !ok || v != "--spec-draft-device" {
		t.Fatalf("draft device %q", v)
	}
	for _, c := range []struct{ help, want string }{
		{"--spec-draft-n-max N", "--spec-draft-n-max"},
		{"--draft-max N", "--draft-max"},
		{"--draft-max N (removed)\n--spec-draft-n-max N", "--spec-draft-n-max"},
		{"--spec-ngram-mod-n-max N", ""},
	} {
		if v, ok := byKey[factDraftMax].Parse(c.help); v != c.want || ok != (c.want != "") {
			t.Errorf("draft token limit in %q: %q %v", c.help, v, ok)
		}
	}
}
