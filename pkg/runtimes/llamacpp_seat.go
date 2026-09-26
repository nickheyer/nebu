package runtimes

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Install facts the llama.cpp shapes depend on: the rpc server beside llama-server and the
// flags its help lists
const (
	factRPC         = "rpc"
	factRPCFlag     = "rpc_flag"
	factDraftDevice = "draft_device"
	factDraftModel  = "draft_model"
	factDraftMax    = "draft_max"
	factSlotSave    = "slot_save"
	factFitFlag     = "fit_flag"
	factSlotsFlag   = "slots_flag"
	factDirectIO    = "direct_io"
)

// The rpc server names ggml ships it under, beside the runtime's own binary
var rpcServerNames = []string{"rpc-server", "ggml-rpc-server", "llama-rpc-server"}

// Paths an rpc server would sit at beside a binary
func rpcServerCandidates(in Install) []string {
	dir := filepath.Dir(in.Path)
	var out []string
	for _, name := range rpcServerNames {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

// Probes recording which flags this build accepts and whether it ships the rpc server. Direct
// reads were --direct-io before llama.cpp folded them into --load-mode dio, so both are looked for
func llamaShapeProbes() []Probe {
	help := []string{"--help"}
	return []Probe{
		{Key: factRPC, Files: rpcServerCandidates},
		flagProbe(factRPCFlag, help, "--rpc"),
		flagProbe(factDraftDevice, help, "--device-draft", "--spec-draft-device"),
		flagProbe(factDraftModel, help, "--model-draft", "--spec-draft-model"),
		flagProbe(factDraftMax, help, "--spec-draft-n-max", "--draft-max"),
		flagProbe(factSlotSave, help, "--slot-save-path"),
		flagProbe(factFitFlag, help, "--fit"),
		flagProbe(factSlotsFlag, help, "--slots"),
		flagProbe(factDirectIO, help, "--direct-io", "--load-mode"),
	}
}

// The arguments that read weights without the page cache on this build: the flag alone when the
// build lists --direct-io, the dio load mode when it lists --load-mode
func directIOArgs(flag string) []string {
	if flag == "--load-mode" {
		return []string{flag, "dio"}
	}
	return []string{flag}
}

func (LlamaCpp) Shapes(in *v1.Install) []v1.Shape {
	out := []v1.Shape{v1.Shape_SHAPE_SOLO, v1.Shape_SHAPE_REPLICAS}
	if has(in, factRPC) || has(in, factRPCFlag) {
		out = append(out, v1.Shape_SHAPE_CHAIN)
		if has(in, factRPC) || (has(in, factDraftDevice) && has(in, factDraftModel)) {
			out = append(out, v1.Shape_SHAPE_DRAFT)
		}
	}
	if has(in, factSlotSave) {
		out = append(out, v1.Shape_SHAPE_RELAY)
	}
	return out
}

// A stage is a ggml rpc server: alive and answering one hello before the head launches
var rpcStageHealth = RoleHealth{Kind: HealthHello, Interval: time.Second, Timeout: 2 * time.Minute}

func (LlamaCpp) Roles(shape v1.Shape) []Role {
	switch shape {
	case v1.Shape_SHAPE_CHAIN, v1.Shape_SHAPE_DRAFT:
		return []Role{
			{Name: RoleStage, Phase: 1, Files: FilesNone, Health: rpcStageHealth, Listens: true, Single: true, ConnectsFrom: []string{RoleHead}},
			{Name: RoleHead, Phase: 2, Files: FilesWeights, Health: RoleHealth{Kind: HealthHTTP}, Head: true},
		}
	case v1.Shape_SHAPE_RELAY:
		return []Role{
			{Name: RolePrefill, Phase: 1, Files: FilesWeights, Health: RoleHealth{Kind: HealthHTTP}, Listens: true, ConnectsFrom: []string{RoleConductor}},
			{Name: RoleDecode, Phase: 1, Files: FilesWeights, Health: RoleHealth{Kind: HealthHTTP}, Listens: true, Head: true, ConnectsFrom: []string{RoleConductor}},
		}
	case v1.Shape_SHAPE_REPLICAS:
		return []Role{{Name: RoleReplica, Phase: 1, Files: FilesWeights, Health: RoleHealth{Kind: HealthHTTP}, Listens: true, Head: true, ConnectsFrom: []string{RoleConductor}}}
	case v1.Shape_SHAPE_SOLO:
		return []Role{{Name: RoleHead, Phase: 1, Files: FilesWeights, Health: RoleHealth{Kind: HealthHTTP}, Head: true}}
	}
	return nil
}

// The transport a llama.cpp seat negotiated. ggml's rpc transport names an upgrade or a failed
// upgrade outright; a connection that stayed on TCP because a peer had no RDMA device logs no
// line of its own, and is known from what follows the hello without an activation: a head
// allocating a model buffer on an rpc device, or a stage saving a streamed tensor to its cache
// after the connection it accepted last
func (LlamaCpp) Transport(lines []string) string {
	if t := transportOf(lines); t != "" {
		return t
	}
	for _, line := range lines {
		if strings.Contains(line, "RPC") && strings.Contains(line, " model buffer size = ") {
			return "sockets"
		}
		if strings.Contains(line, "] saved to '") {
			return "sockets"
		}
	}
	return ""
}

func (r LlamaCpp) LaunchSeat(in Launch) (*Command, error) {
	seat := in.Seat
	if seat == nil {
		return nil, fmt.Errorf("%w: a seat launch needs a seat block", ErrParam)
	}
	role, err := RoleOf(r, seat)
	if err != nil {
		return nil, err
	}
	facts := in.InstallRecord.GetFacts()
	switch role.Name {
	case RoleStage:
		return rpcServerCommand(in, facts[factRPC])
	case RoleHead:
		if seat.GetShape() == v1.Shape_SHAPE_SOLO {
			return r.Launch(in)
		}
		stages := peersOf(seat, RoleStage)
		if len(stages) == 0 {
			return nil, fmt.Errorf("%w: the head of a %s formation has no stage peers", ErrParam, shapeWord(seat.GetShape()))
		}
		if !has(in.InstallRecord, factRPCFlag) {
			return nil, fmt.Errorf("%w: this llama-server does not accept --rpc, so it cannot head a chain", ErrParam)
		}
		if seat.GetShape() == v1.Shape_SHAPE_DRAFT {
			return r.draftHead(in, stages, facts)
		}
		return r.chainHead(in, stages, facts)
	case RolePrefill, RoleDecode:
		if !has(in.InstallRecord, factSlotSave) {
			return nil, fmt.Errorf("%w: this llama-server does not accept --slot-save-path, so it cannot relay", ErrParam)
		}
		if seat.GetSlotDir() == "" {
			return nil, fmt.Errorf("%w: a relay seat needs a slot directory for its saved caches", ErrParam)
		}
		cmd, err := r.Launch(exposedLaunch(in))
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(seat.GetSlotDir(), 0o755); err != nil {
			return nil, err
		}
		cmd.Args = append(cmd.Args, facts[factSlotSave], seat.GetSlotDir())
		if has(in.InstallRecord, factSlotsFlag) {
			cmd.Args = append(cmd.Args, facts[factSlotsFlag])
		}
		cmd.Params["slot_save_path"] = seat.GetSlotDir()
		return cmd, nil
	case RoleReplica:
		return r.Launch(exposedLaunch(in))
	}
	return nil, fmt.Errorf("%w: llama.cpp renders no command for role %s", ErrParam, role.Name)
}

// A launch bound to the mesh address when the seat is exposed
func exposedLaunch(in Launch) Launch {
	in.Host, in.Port = seatBind(in)
	return in
}

// The stage command: an rpc server on loopback or the mesh address, caching streamed tensors on
// disk, holding the seat's devices, with the threads it computes on, the host's core count unless
// the params say otherwise, and the graph workaround its param asks for
func rpcServerCommand(in Launch, bin string) (*Command, error) {
	if bin == "" {
		return nil, fmt.Errorf("%w: this install ships no rpc server beside its binary. Installs made by nebu, built or downloaded, include it, so reinstall llama.cpp from the Runtimes page", ErrParam)
	}
	if in.Seat.GetCacheDir() == "" {
		return nil, fmt.Errorf("%w: a stage needs a cache directory for the tensors the head streams", ErrParam)
	}
	host, port := seatBind(in)
	args := []string{"-H", host, "-p", strconv.Itoa(port), "-c"}
	devices := ggmlDevices(in.InstallRecord, in.Devices)
	if len(devices) == 0 {
		for _, d := range in.Devices {
			if d.GetKind() == v1.DeviceKind_DEVICE_KIND_CPU {
				devices = []string{"CPU"}
			}
		}
	}
	if len(devices) > 0 {
		args = append(args, "-d", strings.Join(devices, ","))
	}
	threads := in.Params.Int("threads")
	if threads <= 0 {
		threads = int64(runtime.NumCPU())
	}
	args = append(args, "-t", strconv.FormatInt(threads, 10))
	if err := os.MkdirAll(in.Seat.GetCacheDir(), 0o755); err != nil {
		return nil, err
	}
	env := map[string]string{"LLAMA_CACHE": in.Seat.GetCacheDir()}
	setEnv(env, "CUDA_VISIBLE_DEVICES", visible(in.Devices, "nvidia", false))
	setEnv(env, "ROCR_VISIBLE_DEVICES", visible(in.Devices, "amd", true))
	setEnv(env, "GGML_VK_VISIBLE_DEVICES", visible(in.Devices, "", true))
	if in.Params.Bool("cuda_disable_graphs") {
		env["GGML_CUDA_DISABLE_GRAPHS"] = "1"
	}
	params := map[string]string{"cache_dir": in.Seat.GetCacheDir(), "host": host, "port": strconv.Itoa(port), "threads": strconv.FormatInt(threads, 10)}
	return &Command{Command: bin, Args: args, Env: env, Params: params}, nil
}

// The seat's device with an id
func deviceByID(devices []*v1.Device, id string) *v1.Device {
	for _, d := range devices {
		if d.GetId() == id {
			return d
		}
	}
	return nil
}

// The chain head: the solo command with every stage's devices as RPC devices before its own, in
// the plan's order, the plan's bytes per device as the tensor split, every layer and the output
// offloaded across them, and the runtime's own fitting off. The plan's device list names each
// stage device as <node id>/<device id> and the head's own by their ids
func (r LlamaCpp) chainHead(in Launch, stages []*v1.SeatPeer, facts map[string]string) (*Command, error) {
	seat := in.Seat
	names, err := rpcDeviceNames(stages)
	if err != nil {
		return nil, err
	}
	ids := seat.GetDevices()
	var addresses, devices []string
	next := 0
	for i, s := range stages {
		addresses = append(addresses, s.GetAddress())
		for _, name := range names[i] {
			if next >= len(ids) || !strings.HasPrefix(ids[next], s.GetNodeId()+"/") {
				return nil, fmt.Errorf("%w: the plan's device list holds %d entries and stage %d on %s exposes %d devices, which come first as %s/<device>", ErrParam, len(ids), s.GetRank(), s.GetNodeId(), s.GetDevices(), s.GetNodeId())
			}
			devices = append(devices, name)
			next++
		}
	}
	var own []*v1.Device
	for _, id := range ids[next:] {
		d := deviceByID(in.Devices, id)
		if d == nil {
			return nil, fmt.Errorf("%w: the plan lists device %s after the stages' devices, which this node's seat does not hold", ErrParam, id)
		}
		if d.GetKind() == v1.DeviceKind_DEVICE_KIND_CPU {
			return nil, fmt.Errorf("%w: the plan lists the CPU %s among the head's tensor split devices; a chain head offloads every layer to the listed devices", ErrParam, id)
		}
		own = append(own, d)
	}
	devices = append(devices, ggmlDevices(in.InstallRecord, own)...)
	if len(seat.GetDeviceBytes()) != len(devices) {
		return nil, fmt.Errorf("%w: the plan gives %d device shares for %d devices", ErrParam, len(seat.GetDeviceBytes()), len(devices))
	}
	layers := int(formats.ParamsOf(in.Descriptor.GetParams()).Layers)
	if layers <= 0 {
		return nil, fmt.Errorf("%w: the descriptor counts no layers, so the head cannot say how many to offload", ErrParam)
	}
	p := in.Params.Clone()
	p["device"] = strings.Join(devices, ",")
	// llama.cpp counts the output as one more layer than the model has, and offloads it with the last
	p["n_gpu_layers"] = int64(layers + 1)
	in.Params = p
	cmd, err := r.Launch(in)
	if err != nil {
		return nil, err
	}
	cmd.Args = append(cmd.Args, facts[factRPCFlag], strings.Join(addresses, ","), "--tensor-split", tensorSplit(seat.GetDeviceBytes()))
	if has(in.InstallRecord, factFitFlag) {
		cmd.Args = append(cmd.Args, facts[factFitFlag], "off")
	}
	cmd.Params["rpc"] = strings.Join(addresses, ",")
	cmd.Params["tensor_split"] = tensorSplit(seat.GetDeviceBytes())
	return cmd, nil
}

// The draft head: the solo command with the draft model on the stage's first RPC device
func (r LlamaCpp) draftHead(in Launch, stages []*v1.SeatPeer, facts map[string]string) (*Command, error) {
	if in.Draft == "" {
		return nil, fmt.Errorf("%w: a draft head needs the draft model's weights", ErrParam)
	}
	if !has(in.InstallRecord, factDraftDevice) || !has(in.InstallRecord, factDraftModel) {
		return nil, fmt.Errorf("%w: this llama-server accepts no draft device flag, so it cannot head a draft formation", ErrParam)
	}
	names, err := rpcDeviceNames(stages[:1])
	if err != nil {
		return nil, err
	}
	stage, device := stages[0], names[0][0]
	p := in.Params.Clone()
	p["draft_model"] = ""
	in.Params = p
	cmd, err := r.Launch(in)
	if err != nil {
		return nil, err
	}
	cmd.Args = append(cmd.Args, facts[factRPCFlag], stage.GetAddress(), facts[factDraftModel], in.Draft, facts[factDraftDevice], device)
	cmd.Params["rpc"] = stage.GetAddress()
	cmd.Params["draft_model"] = in.Draft
	cmd.Params["draft_device"] = device
	return cmd, nil
}

func shapeWord(s v1.Shape) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "SHAPE_"))
}
