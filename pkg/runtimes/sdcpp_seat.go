package runtimes

import (
	"fmt"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Install facts the stages shape and the module flags depend on, the rpc server beside
// sd-server and the flags its help lists
const (
	factRPCServers   = "rpc_servers_flag"
	factSDFlashAttn  = "flash_attn_flag"
	factSDConvDirect = "conv_direct_flag"
)

// The flags sd-server has renamed across builds, what the help probe looks for
var sdFlags = map[string][]string{
	factRPCServers:   {"--rpc-servers"},
	factSDFlashAttn:  {"--flash-attn"},
	factSDConvDirect: {"--conv-direct"},
}

func sdShapeProbes() []Probe {
	return []Probe{{Key: factRPC, Files: rpcServerCandidates}, helpProbe([]string{"--help"}, sdFlags)}
}

func (SDCpp) Shapes(in *v1.Install) []v1.Shape {
	out := []v1.Shape{v1.Shape_SHAPE_SOLO, v1.Shape_SHAPE_REPLICAS}
	if has(in, factRPC) && has(in, factRPCServers) {
		out = append(out, v1.Shape_SHAPE_STAGES)
	}
	return out
}

func (SDCpp) Roles(shape v1.Shape) []Role {
	switch shape {
	case v1.Shape_SHAPE_STAGES:
		return []Role{
			{Name: RoleDenoiser, Phase: 1, Files: FilesNone, Health: rpcStageHealth, Listens: true, Single: true, Helper: true, ConnectsFrom: []string{RoleHead}},
			{Name: RoleHead, Phase: 2, Files: FilesParts, Health: RoleHealth{Kind: HealthHTTP}, Head: true},
		}
	case v1.Shape_SHAPE_REPLICAS:
		return []Role{{Name: RoleReplica, Phase: 1, Files: FilesParts, Health: RoleHealth{Kind: HealthHTTP}, Listens: true, Head: true, ConnectsFrom: []string{RoleConductor}}}
	case v1.Shape_SHAPE_SOLO:
		return []Role{{Name: RoleHead, Phase: 1, Files: FilesParts, Health: RoleHealth{Kind: HealthHTTP}, Head: true}}
	}
	return nil
}

// The transport a stable-diffusion.cpp seat negotiated, from the lines ggml's rpc transport logs
// on the head and the denoiser stage, and on the stage from a tensor saved to its cache after
// the connection it accepted without an activation
func (SDCpp) Transport(lines []string) string {
	if t := transportOf(lines); t != "" {
		return t
	}
	for _, line := range lines {
		if strings.Contains(line, "] saved to '") {
			return "sockets"
		}
	}
	return ""
}

func (r SDCpp) LaunchSeat(in Launch) (*Command, error) {
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
	case RoleDenoiser:
		return rpcServerCommand(in, facts[factRPC])
	case RoleReplica:
		return r.Launch(exposedLaunch(in))
	case RoleHead:
		if seat.GetShape() == v1.Shape_SHAPE_SOLO {
			return r.Launch(in)
		}
		stages := peersOf(seat, RoleDenoiser)
		if len(stages) != 1 {
			return nil, fmt.Errorf("%w: a stages head needs exactly one denoiser peer, got %d", ErrParam, len(stages))
		}
		names, err := rpcDeviceNames(stages)
		if err != nil {
			return nil, err
		}
		// Only the denoiser runs on the stage, every other module on the head's own first device
		// or its CPU when it has none
		own := "cpu"
		if devices := ggmlDevices(in.InstallRecord, in.Devices); len(devices) > 0 {
			own = devices[0]
		}
		p := in.Params.Clone()
		p["rpc_servers"] = stages[0].GetAddress()
		p["backend"] = own + ",diffusion=" + names[0][0]
		in.Params = p
		return r.Launch(in)
	}
	return nil, fmt.Errorf("%w: stable-diffusion.cpp renders no command for role %s", ErrParam, role.Name)
}
