package runtimes

import (
	"fmt"
	"strconv"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The relay probe of SGLang: the module of its NIXL transfer backend, and the NIXL agent binding
// the backend's manager imports only when it runs, so an environment without it does not pass
func sglangNIXLProbe() Probe {
	script := "import sglang.srt.disaggregation.nixl.conn\n" + nixlAgentImport + "print('imports NixlKVManager')\n"
	return pythonProbe(factNIXL, script, "imports NixlKVManager", "NixlKVManager")
}

// The speculative settings of this runtime, which pipeline parallel does not compose with outside
// a disaggregated prefill seat, so a chain rank drops them all
var sglangSpeculativeParams = []string{"speculative_algorithm", "speculative_draft_model_path", "speculative_num_steps", "speculative_eagle_topk", "speculative_num_draft_tokens"}

func (SGLang) Shapes(in *v1.Install) []v1.Shape {
	out := []v1.Shape{v1.Shape_SHAPE_SOLO, v1.Shape_SHAPE_REPLICAS}
	if has(in, factNNodes) {
		out = append(out, v1.Shape_SHAPE_CHAIN, v1.Shape_SHAPE_LOCKSTEP)
	}
	if has(in, factDisaggregation) && has(in, factNIXL) {
		out = append(out, v1.Shape_SHAPE_RELAY)
	}
	return out
}

func (SGLang) Roles(shape v1.Shape) []Role {
	switch shape {
	case v1.Shape_SHAPE_CHAIN, v1.Shape_SHAPE_LOCKSTEP:
		return rankRoles()
	case v1.Shape_SHAPE_RELAY:
		return relayRoles()
	case v1.Shape_SHAPE_REPLICAS:
		return replicaRoles()
	case v1.Shape_SHAPE_SOLO:
		return soloRoles()
	}
	return nil
}

func (SGLang) Transport(lines []string) string { return transportOf(lines) }

func (r SGLang) LaunchSeat(in Launch) (*Command, error) {
	seat := in.Seat
	if seat == nil {
		return nil, fmt.Errorf("%w: a seat launch needs a seat block", ErrParam)
	}
	role, err := RoleOf(r, seat)
	if err != nil {
		return nil, err
	}
	grace := int(r.StopGrace().Seconds())
	switch seat.GetShape() {
	case v1.Shape_SHAPE_SOLO:
		return r.Launch(in)
	case v1.Shape_SHAPE_REPLICAS:
		return r.Launch(exposedLaunch(in))
	case v1.Shape_SHAPE_CHAIN, v1.Shape_SHAPE_LOCKSTEP:
		if !has(in.InstallRecord, factNNodes) {
			return nil, fmt.Errorf("%w: this sglang does not accept --nnodes, so it cannot run across nodes", ErrParam)
		}
		if _, _, err := rendezvousOf(seat); err != nil {
			return nil, err
		}
		pp, tp, err := rankSizes(seat, len(gpusOf(in.Devices)))
		if err != nil {
			return nil, err
		}
		p := in.Params.Clone()
		partition := ""
		if seat.GetShape() == v1.Shape_SHAPE_CHAIN {
			for _, name := range sglangSpeculativeParams {
				delete(p, name)
			}
			if partition, err = chainPartition(in); err != nil {
				return nil, err
			}
		}
		p["tp_size"] = int64(tp)
		in.Params = p
		cmd, err := r.Launch(exposedLaunch(in))
		if err != nil {
			return nil, err
		}
		cmd.Args = append(cmd.Args, "--pp-size", strconv.Itoa(pp), "--nnodes", strconv.FormatUint(uint64(seat.GetCount()), 10), "--node-rank", strconv.FormatUint(uint64(seat.GetRank()), 10), "--dist-init-addr", seat.GetRendezvous(), "--watchdog-timeout", strconv.Itoa(grace))
		if partition != "" {
			// The pipeline overlaps prompt chunks, sized as it runs, and its scheduler cannot overlap the
			// CPU with the workers across stages.
			cmd.Args = append(cmd.Args, "--enable-dynamic-chunking", "--disable-overlap-schedule")
			cmd.Env["SGLANG_PP_LAYER_PARTITION"] = partition
			cmd.Params["pp_layer_partition"] = partition
		}
		collectiveEnv(cmd.Env, seat, grace)
		cmd.Params["pp_size"] = strconv.Itoa(pp)
		cmd.Params["nnodes"] = strconv.FormatUint(uint64(seat.GetCount()), 10)
		cmd.Params["node_rank"] = strconv.FormatUint(uint64(seat.GetRank()), 10)
		cmd.Params["dist_init_addr"] = seat.GetRendezvous()
		return cmd, nil
	case v1.Shape_SHAPE_RELAY:
		if !has(in.InstallRecord, factDisaggregation) || !has(in.InstallRecord, factNIXL) {
			return nil, fmt.Errorf("%w: this sglang has no NIXL disaggregation, so it cannot relay", ErrParam)
		}
		if seat.GetAuxPort() == 0 {
			return nil, fmt.Errorf("%w: a relay seat needs a bootstrap port", ErrParam)
		}
		cmd, err := r.Launch(exposedLaunch(in))
		if err != nil {
			return nil, err
		}
		cmd.Args = append(cmd.Args, "--disaggregation-mode", role.Name, "--disaggregation-transfer-backend", "nixl", "--watchdog-timeout", strconv.Itoa(grace))
		if role.Name == RolePrefill {
			cmd.Args = append(cmd.Args, "--disaggregation-bootstrap-port", strconv.FormatUint(uint64(seat.GetAuxPort()), 10))
		}
		if rdma := seat.GetRdmaDevices(); len(rdma) > 0 {
			cmd.Args = append(cmd.Args, "--disaggregation-ib-device", strings.Join(rdma, ","))
			cmd.Params["disaggregation_ib_device"] = strings.Join(rdma, ",")
		}
		collectiveEnv(cmd.Env, seat, grace)
		cmd.Params["disaggregation_mode"] = role.Name
		return cmd, nil
	}
	return nil, fmt.Errorf("%w: SGLang renders no command for shape %s", ErrParam, shapeWord(seat.GetShape()))
}
