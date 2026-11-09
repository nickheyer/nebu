package runtimes

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/gguf"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
	"github.com/nickheyer/nebu/pkg/triage"
)

// Id of the rule every runtime declares for a port another process took
const portRuleID = "port-in-use"

// Devices a launch runs on, the slot's assignment or else the profile devices whose pools the plan filled
func launchDevices(in Launch) ([]*v1.Device, error) {
	if len(in.Devices) > 0 {
		for _, d := range in.Devices {
			if d.GetKind() != v1.DeviceKind_DEVICE_KIND_CPU && !host.OwnMemory(in.Profile, d) {
				return nil, fmt.Errorf("%w: device %q (%s) has no memory of its own, so a launch cannot pin it", ErrParam, d.GetId(), d.GetName())
			}
		}
		return in.Devices, nil
	}
	var pools []string
	for _, pu := range in.Plan.GetPools() {
		if pu.GetKind() == v1.PoolKind_POOL_KIND_DEVICE && pu.GetUsedBytes() > 0 {
			pools = append(pools, pu.GetPoolId())
		}
	}
	if len(pools) == 0 {
		return nil, nil
	}
	if in.Profile == nil {
		return nil, fmt.Errorf("%w: the plan places weights in pools %s and the launch carries no host profile naming their devices", ErrParam, strings.Join(pools, ","))
	}
	want := map[string]bool{}
	for _, id := range pools {
		i := slices.IndexFunc(in.Profile.GetPools(), func(pl *v1.MemoryPool) bool { return pl.GetId() == id })
		if i < 0 {
			return nil, fmt.Errorf("%w: the plan places weights in pool %s, which the host profile does not list", ErrParam, id)
		}
		want[in.Profile.GetPools()[i].GetDeviceId()] = true
	}
	var out []*v1.Device
	for _, d := range in.Profile.GetDevices() {
		if want[d.GetId()] {
			out = append(out, d)
			delete(want, d.GetId())
		}
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for id := range want {
			missing = append(missing, id)
		}
		slices.Sort(missing)
		return nil, fmt.Errorf("%w: the plan's pools belong to devices %s, which the host profile does not list", ErrParam, strings.Join(missing, ","))
	}
	return out, nil
}

// Sets the visibility variables of every accelerator stack to the devices, leaving them unset for none
func pinEnv(env map[string]string, devices []*v1.Device) {
	setEnv(env, "CUDA_VISIBLE_DEVICES", visible(devices, "nvidia", false))
	setEnv(env, "ROCR_VISIBLE_DEVICES", visible(devices, "amd", true))
	setEnv(env, "GGML_VK_VISIBLE_DEVICES", visible(devices, "", true))
}

// Pins the command to the launch's devices and records them on it
func pinDevices(cmd *Command, in Launch) error {
	devices, err := launchDevices(in)
	if err != nil {
		return err
	}
	pinEnv(cmd.Env, devices)
	cmd.Devices = devices
	return nil
}

// An integer from recorded params, zero when absent or not a whole number
func paramInt(params map[string]string, name string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(params[name]), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// Whether a recorded switch or bool param is on
func paramOn(params map[string]string, name string) bool {
	switch strings.ToLower(strings.TrimSpace(params[name])) {
	case "true", "on", "1":
		return true
	}
	return false
}

// A per engine count multiplied over the replicas one endpoint balances itself, zero while the count is unrecorded
func perReplica(params map[string]string, count, replicas string) uint32 {
	n := paramInt(params, count)
	if n <= 0 {
		return 0
	}
	return uint32(n * max(paramInt(params, replicas), 1))
}

// A recorded count of one engine, zero while it is unrecorded
func perEngine(params map[string]string, count string) uint32 {
	return uint32(max(paramInt(params, count), 0))
}

// Data parallel ranks a recorded count names, at least one
func recordedRanks(params map[string]string, name string) int {
	return int(max(paramInt(params, name), 1))
}

// Data parallel ranks a launch's count names, at least one
func launchRanks(p estimate.Params, name string) int {
	return int(max(p.Int(name), 1))
}

// Ports one rank binds, its HTTP port and the gRPC port after it when the launch serves gRPC
func rankSpan(p estimate.Params) int {
	if p.Bool("grpc") {
		return 2
	}
	return 1
}

// Whether the model shares expert layers, which data parallel ranks split between them
func mixtureOfExperts(d *v1.Descriptor) bool {
	return slices.ContainsFunc(d.GetGroups(), func(g *v1.TensorGroup) bool { return g.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS })
}

// The devices one data parallel rank pins, the launch's accelerators in plan pool order split evenly by rank
func rankPins(in Launch, devices []*v1.Device, ranks, per int) ([]*v1.Device, error) {
	if in.Rank < 0 || in.Rank >= ranks {
		return nil, fmt.Errorf("%w: rank %d of a launch of %d data parallel ranks", ErrParam, in.Rank, ranks)
	}
	if ranks == 1 {
		return devices, nil
	}
	gpus := planOrder(in, gpusOf(devices))
	if len(gpus) < ranks*per {
		return nil, fmt.Errorf("%w: %d data parallel ranks of %d devices each need %d accelerators and the launch holds %d", ErrParam, ranks, per, ranks*per, len(gpus))
	}
	return gpus[in.Rank*per : (in.Rank+1)*per], nil
}

// Devices in the order the plan lists their pools, devices of no planned pool after them in their own order
func planOrder(in Launch, devices []*v1.Device) []*v1.Device {
	owner := map[string]string{}
	for _, pl := range in.Profile.GetPools() {
		owner[pl.GetId()] = pl.GetDeviceId()
	}
	pos := map[string]int{}
	for i, pu := range in.Plan.GetPools() {
		id := owner[pu.GetPoolId()]
		if _, seen := pos[id]; id != "" && !seen && pu.GetKind() == v1.PoolKind_POOL_KIND_DEVICE {
			pos[id] = i
		}
	}
	out := slices.Clone(devices)
	slices.SortStableFunc(out, func(a, b *v1.Device) int {
		pa, okA := pos[a.GetId()]
		pb, okB := pos[b.GetId()]
		switch {
		case okA && okB:
			return pa - pb
		case okA:
			return -1
		case okB:
			return 1
		}
		return 0
	})
	return out
}

// Refuses data parallel ranks in a seat that spreads one engine over nodes, ranks running in solo and replicas seats
func oneRankSeat(in Launch, param string) error {
	if n := launchRanks(in.Params, param); n > 1 {
		return fmt.Errorf("%w: a %s seat runs one engine and %s %d asks for data parallel ranks, which run in solo and replicas seats", ErrParam, shapeWord(in.Seat.GetShape()), param, n)
	}
	return nil
}

// Whether a log line from a launch that exited before answering health says one of the span's ports was taken
func PortTaken(rt Runtime, span, port int, lines []string) bool {
	rule := rt.PortRule()
	for _, line := range lines {
		vars, ok := rule.Match(line)
		if !ok {
			continue
		}
		named, says := vars["port"]
		if !says {
			return true
		}
		if n, err := strconv.Atoi(named); err == nil && n >= port && n < port+span {
			return true
		}
	}
	return false
}

// Python servers name the address they failed to bind as ('host', port)
var pythonBindRe = regexp.MustCompile(`\(\s*'[^']*'\s*,\s*(\d+)\s*\)`)

// The bind failure of a uvicorn, Ray or tonic server, the port taken from the address it names
func pythonPortRule(runtime string) triage.Rule {
	return triage.Rule{
		ID:      portRuleID,
		Summary: runtime + " found a port it binds taken by another process",
		Hint:    "reserve another port and launch again",
		Match: func(line string) (map[string]string, bool) {
			if !strings.Contains(strings.ToLower(line), "address already in use") {
				return nil, false
			}
			if m := pythonBindRe.FindStringSubmatch(line); m != nil {
				return map[string]string{"port": m[1]}, true
			}
			return nil, true
		},
	}
}

// A context param as a token count, the fallback when the params record none
func contextOf(params map[string]string, name string, fallback float64) uint32 {
	if n := paramInt(params, name); n > 0 {
		return uint32(n)
	}
	return uint32(fallback)
}

// Trained context length of the launch's model
func trained(in Launch) float64 {
	return formats.ParamsOf(in.Descriptor.GetParams()).ContextTrain
}

// Weight quantization, the runtime's quantization param over the checkpoint's own precision
func quantizationOf(in Launch, params map[string]string) string {
	if q := strings.TrimSpace(params["quantization"]); q != "" {
		return q
	}
	return in.Descriptor.GetPrecision().GetLabel()
}

// Accelerators the launch runs on, counted by name, cpu for a host run
func acceleratorOf(in Launch) (string, error) {
	if in.Placement == v1.Placement_PLACEMENT_HOST {
		return acceleratorCPU, nil
	}
	devices, err := launchDevices(in)
	if err != nil {
		return "", err
	}
	if len(devices) == 0 {
		if in.Profile == nil {
			return "", fmt.Errorf("%w: the launch names no devices and carries no host profile to say what it runs on", ErrParam)
		}
		for _, d := range in.Profile.GetDevices() {
			if host.OwnMemory(in.Profile, d) {
				devices = append(devices, d)
			}
		}
	}
	var names []string
	count := map[string]int{}
	for _, d := range gpusOf(devices) {
		name := d.GetName()
		if name == "" {
			name = d.GetVendor() + " " + d.GetId()
		}
		if count[name] == 0 {
			names = append(names, name)
		}
		count[name]++
	}
	if len(names) == 0 {
		return acceleratorCPU, nil
	}
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = strconv.Itoa(count[name]) + "x " + name
	}
	return strings.Join(parts, " + "), nil
}

// Where the plan put the weights, device, offload when layers spill to host memory, or host
func throughputOf(in Launch) string {
	if in.Placement == v1.Placement_PLACEMENT_HOST {
		return ThroughputHost
	}
	host := text.Enum(v1.PoolKind_POOL_KIND_HOST)
	onHost, onDevice := false, false
	for _, pl := range in.Plan.GetPlacements() {
		if pl.GetBytes() == 0 || pl.GetPoolId() == "" || pl.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING {
			continue
		}
		if pl.GetPoolId() == host {
			onHost = true
		} else {
			onDevice = true
		}
	}
	switch {
	case onHost && !onDevice:
		return ThroughputHost
	case onHost:
		return ThroughputOffload
	}
	return ThroughputDevice
}

// Modalities a launch of a model with its own encoders takes, text first, then images and audio it loads
func encoderModalities(d *v1.Descriptor) []string {
	out := []string{"text"}
	for _, kind := range []struct {
		group v1.TensorGroupKind
		name  string
	}{{v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, "image"}, {v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, "audio"}} {
		if slices.ContainsFunc(d.GetGroups(), func(g *v1.TensorGroup) bool { return g.GetKind() == kind.group }) {
			out = append(out, kind.name)
		}
	}
	return out
}

// Metadata of a GGUF file, its tensors left unread
func ggufMetadata(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	meta, _, err := gguf.ReadHeader(f, info.Size())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return meta, nil
}

// Modalities a llama.cpp projector file adds, read from the encoders its header declares
func projectorModalities(path string) ([]string, error) {
	meta, err := ggufMetadata(path)
	if err != nil {
		return nil, fmt.Errorf("projector %w", err)
	}
	var out []string
	if meta["clip.has_vision_encoder"] == "true" {
		out = append(out, "image")
	}
	if meta["clip.has_audio_encoder"] == "true" {
		out = append(out, "audio")
	}
	return out, nil
}

// Inventory fields shared by every runtime, the context and modalities left to the caller
func baseInventory(in Launch, params map[string]string) (*v1.RuntimeInventory, error) {
	accelerator, err := acceleratorOf(in)
	if err != nil {
		return nil, err
	}
	return &v1.RuntimeInventory{Quantization: quantizationOf(in, params), Accelerator: accelerator, ThroughputClass: throughputOf(in)}, nil
}
