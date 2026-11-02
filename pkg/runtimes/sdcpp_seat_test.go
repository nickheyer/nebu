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
	if cmd.Command != "/bin/sd-server" || !strings.HasPrefix(line, "--listen-ip 127.0.0.1 --listen-port 9 --model /store/sd_xl_base_1.0.safetensors") || strings.Contains(line, "--clip-on-cpu") {
		t.Fatalf("head %s %s", cmd.Command, line)
	}
	for _, want := range []string{"--rpc-servers 10.0.0.2:50052", "--backend cpu,diffusion=RPC0"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %s in %s", want, line)
		}
	}
	if cmd.Params["backend"] != "cpu,diffusion=RPC0" || cmd.Params["rpc_servers"] != "10.0.0.2:50052" {
		t.Fatalf("params %v", cmd.Params)
	}
	// A head with a device of its own keeps the other modules on it
	in.Devices = []*v1.Device{gpu("GPU-0", "0")}
	if cmd, err := rt.LaunchSeat(in); err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--backend CUDA0,diffusion=RPC0") {
		t.Fatalf("head with a device: %v %v", cmd, err)
	}
	in.Devices = nil
	head.Peers = append(head.Peers, &v1.SeatPeer{NodeId: "b", Role: RoleDenoiser, Rank: 2, Address: "10.0.0.3:50052", Devices: 1})
	if _, err := rt.LaunchSeat(in); err == nil || !strings.Contains(err.Error(), "exactly one denoiser") {
		t.Fatalf("two denoisers: %v", err)
	}
	head.Peers = head.Peers[:1]
	// A build whose help listed no rpc servers flag still gets it, sd-server judging it
	in.InstallRecord = recorded(map[string]string{factRPC: "/bin/rpc-server"})
	if cmd, err := rt.LaunchSeat(in); err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--rpc-servers 10.0.0.2:50052") {
		t.Fatalf("unprobed rpc servers flag: %v %v", cmd, err)
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

// Module assignments render as --flash-attn and --conv-direct on a build listing them, as the
// older per module flags on one that does not, and are refused there when they name a module
// the old flags could not
func TestSDCppModuleFlags(t *testing.T) {
	rt := SDCpp{}
	launch := func(overrides map[string]string, facts map[string]string) (string, error) {
		params, err := Resolve(rt, overrides)
		if err != nil {
			t.Fatal(err)
		}
		cmd, err := rt.Launch(Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights": "/w.safetensors", "prepared_dir": t.TempDir()}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/sd-server"}, Descriptor: sdDescriptor("sdxl", true, false), InstallRecord: recorded(facts)})
		if err != nil {
			return "", err
		}
		return strings.Join(cmd.Args, " "), nil
	}
	modern := map[string]string{factSDFlashAttn: "--flash-attn", factSDConvDirect: "--conv-direct"}
	for _, c := range []struct {
		overrides map[string]string
		facts     map[string]string
		want      []string
		absent    []string
	}{
		{nil, modern, nil, []string{"--flash-attn", "--conv-direct", "--fa", "--diffusion-fa"}},
		{map[string]string{"flash_attn": "auto,te=off", "conv_direct": "auto,vae=off"}, modern, []string{"--flash-attn auto,te=off", "--conv-direct auto,vae=off"}, []string{"--fa", "--diffusion-fa"}},
		{map[string]string{"flash_attn": "on", "conv_direct": "on"}, nil, []string{"--fa", "--diffusion-conv-direct --vae-conv-direct"}, []string{"--flash-attn", "--conv-direct"}},
		{map[string]string{"flash_attn": "off,diffusion=on", "conv_direct": "diffusion=on"}, nil, []string{"--diffusion-fa", "--diffusion-conv-direct"}, []string{"--fa ", "--vae-conv-direct"}},
		{map[string]string{"flash_attn": "off", "conv_direct": "on,vae=off"}, nil, []string{"--diffusion-conv-direct"}, []string{"--fa", "--diffusion-fa", "--vae-conv-direct"}},
	} {
		line, err := launch(c.overrides, c.facts)
		if err != nil {
			t.Fatalf("%v: %v", c.overrides, err)
		}
		for _, w := range c.want {
			if !strings.Contains(line, w) {
				t.Errorf("%v on %v: missing %s in %s", c.overrides, c.facts, w, line)
			}
		}
		for _, a := range c.absent {
			if strings.Contains(line, a) {
				t.Errorf("%v on %v: %s in %s", c.overrides, c.facts, a, line)
			}
		}
	}
	if _, err := launch(map[string]string{"flash_attn": "auto,te=off"}, nil); err == nil || !strings.Contains(err.Error(), "flash_attn") {
		t.Fatalf("an old build cannot name the text encoder: %v", err)
	}
	if _, err := launch(map[string]string{"conv_direct": "controlnet=on"}, nil); err == nil || !strings.Contains(err.Error(), "conv_direct") {
		t.Fatalf("an old build cannot name the controlnet: %v", err)
	}
	if !sdFlashAttn("") || !sdFlashAttn("auto") || sdFlashAttn("off") || !sdFlashAttn("off,diffusion=on") || sdFlashAttn("on,diffusion=off") {
		t.Fatal("the denoiser runs with flash attention unless its module or the whole value says off")
	}
}
