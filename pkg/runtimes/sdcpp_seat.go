package runtimes

import (
	"fmt"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Install facts the stages shape depends on: the rpc server beside sd-server and its flag
const factRPCServers = "rpc_servers_flag"

func sdShapeProbes() []Probe {
	return []Probe{
		{Key: factRPC, Files: rpcServerCandidates},
		flagProbe(factRPCServers, []string{"--help"}, "--rpc-servers"),
	}
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
			{Name: RoleDenoiser, Phase: 1, Files: FilesNone, Health: rpcStageHealth, Listens: true, Single: true, ConnectsFrom: []string{RoleHead}},
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
		if !has(in.InstallRecord, factRPCServers) {
			return nil, fmt.Errorf("%w: this sd-server does not accept --rpc-servers, so it cannot head a stages formation", ErrParam)
		}
		names, err := rpcDeviceNames(stages)
		if err != nil {
			return nil, err
		}
		p := in.Params.Clone()
		p["rpc_servers"] = stages[0].GetAddress()
		p["backend"] = names[0][0]
		in.Params = p
		cmd, err := r.Launch(in)
		if err != nil {
			return nil, err
		}
		// The text encoders and the decoder stay on the head, only the denoiser runs on the stage.
		cmd.Args = append(cmd.Args, "--clip-on-cpu", "--vae-on-cpu")
		return cmd, nil
	}
	return nil, fmt.Errorf("%w: stable-diffusion.cpp renders no command for role %s", ErrParam, role.Name)
}
