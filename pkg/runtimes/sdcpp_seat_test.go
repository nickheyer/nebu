package runtimes

import (
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestSDCppStagesSeats(t *testing.T) {
	rt := SDCpp{}
	params, err := Resolve(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared := t.TempDir()
	facts := map[string]string{factRPC: "/bin/rpc-server", factRPCServers: "--rpc-servers", "devices": "CUDA0"}
	head := &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_STAGES, Role: RoleHead, Rank: 0, Count: 2, Peers: []*v1.SeatPeer{{NodeId: "a", Role: RoleDenoiser, Rank: 1, Address: "10.0.0.2:50052", Devices: 1}}}
	in := Launch{Name: "sdxl", Params: params, Artifacts: map[string]string{"weights": "/store/sd_xl_base_1.0.safetensors", "prepared_dir": prepared}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/sd-server"}, Descriptor: sdDescriptor("sdxl", true, false), Seat: head, InstallRecord: recorded(facts)}
	cmd, err := rt.LaunchSeat(in)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd.Args, " ")
	if cmd.Command != "/bin/sd-server" || !strings.HasPrefix(line, "--listen-ip 127.0.0.1 --listen-port 9 --model /store/sd_xl_base_1.0.safetensors") || !strings.HasSuffix(line, "--clip-on-cpu --vae-on-cpu") {
		t.Fatalf("head %s %s", cmd.Command, line)
	}
	for _, want := range []string{"--rpc-servers 10.0.0.2:50052", "--backend RPC0"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %s in %s", want, line)
		}
	}
	if cmd.Params["backend"] != "RPC0" || cmd.Params["rpc_servers"] != "10.0.0.2:50052" {
		t.Fatalf("params %v", cmd.Params)
	}
	head.Peers = append(head.Peers, &v1.SeatPeer{NodeId: "b", Role: RoleDenoiser, Rank: 2, Address: "10.0.0.3:50052", Devices: 1})
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "exactly one denoiser") {
		t.Fatalf("two denoisers: %v", err)
	}
	head.Peers = head.Peers[:1]
	in.InstallRecord = recorded(map[string]string{factRPC: "/bin/rpc-server"})
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "--rpc-servers") {
		t.Fatalf("no rpc servers flag: %v", err)
	}
	in.InstallRecord = recorded(facts)
	head.Peers[0].Devices = 0
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "exposes no device") {
		t.Fatalf("denoiser without a device: %v", err)
	}
	// The denoiser is an rpc server holding the seat's device.
	cache := t.TempDir()
	in.Seat = &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_STAGES, Role: RoleDenoiser, Rank: 1, CacheDir: cache}
	in.Devices = []*v1.Device{gpu("GPU-0", "0")}
	cmd, err = rt.LaunchSeat(in)
	if err != nil || cmd.Command != "/bin/rpc-server" || !strings.HasPrefix(strings.Join(cmd.Args, " "), "-H 127.0.0.1 -p 9 -c -d CUDA0 -t ") || cmd.Env["LLAMA_CACHE"] != cache {
		t.Fatalf("denoiser %v %v", cmd, err)
	}
	shapes := rt.Shapes(recorded(facts))
	if len(shapes) != 3 || shapes[2] != v1.Shape_SHAPE_STAGES {
		t.Fatalf("shapes %v", shapes)
	}
	if len(rt.Shapes(recorded(map[string]string{factRPCServers: "--rpc-servers"}))) != 2 {
		t.Fatal("no rpc server, no stages")
	}
	for _, p := range rt.Probes() {
		if p.Key == factRPC && (p.Files == nil || p.Parse != nil) {
			t.Fatal("the rpc server is a file probe")
		}
	}
}
