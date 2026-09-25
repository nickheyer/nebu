package runtimes

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Install facts the vLLM and SGLang shapes depend on
const (
	factNNodes         = "nnodes"
	factNIXL           = "nixl"
	factDisaggregation = "disaggregation"
)

// The interpreter an install's environment runs: the venv's python beside the binary, the
// binary's own shebang, else python3 on PATH
func pythonOf(in Install) string {
	for _, name := range []string{"python", "python3"} {
		if p := filepath.Join(filepath.Dir(in.Path), name); fileExists(p) {
			return p
		}
	}
	if f, err := os.Open(in.Path); err == nil {
		defer f.Close()
		line, _ := bufio.NewReader(f).ReadString('\n')
		if strings.HasPrefix(line, "#!") {
			fields := strings.Fields(strings.TrimPrefix(line, "#!"))
			if len(fields) > 0 && strings.Contains(fields[0], "python") {
				return fields[0]
			}
			if len(fields) > 1 && strings.HasSuffix(fields[0], "env") {
				return fields[1]
			}
		}
	}
	return "python3"
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// A probe that runs a python snippet in the install's environment and records a value when the
// snippet prints its marker
func pythonProbe(key, script, marker, value string) Probe {
	return Probe{Key: key, Command: pythonOf, Args: []string{"-c", script}, Timeout: time.Minute, Parse: func(out string) (string, bool) {
		if strings.Contains(out, marker) {
			return value, true
		}
		return "", false
	}}
}

// A probe that imports a module in the install's environment
func importProbe(key, module string) Probe {
	return pythonProbe(key, "import "+module+"; print('imports "+module+"')", "imports "+module, module)
}

// The import that proves the NIXL agent binding is in an environment: the nixl package, or the
// nixl_rocm package vLLM takes on ROCm
const nixlAgentImport = `try:
    from nixl._api import nixl_agent
except ImportError:
    from nixl_rocm._api import nixl_agent
`

// The relay probe of vLLM: the connector class a kv_transfer_config naming NixlConnector loads,
// at the package vLLM moved it to or the module it lived in before, and the NIXL agent binding
// the connector's worker imports only when it runs, so an environment without it does not pass
func nixlConnectorProbe() Probe {
	script := `try:
    from vllm.distributed.kv_transfer.kv_connector.v1.nixl import NixlConnector
except ImportError:
    from vllm.distributed.kv_transfer.kv_connector.v1.nixl_connector import NixlConnector
` + nixlAgentImport + `print('imports NixlConnector')
`
	return pythonProbe(factNIXL, script, "imports NixlConnector", "NixlConnector")
}

func (VLLM) Shapes(in *v1.Install) []v1.Shape {
	out := []v1.Shape{v1.Shape_SHAPE_SOLO, v1.Shape_SHAPE_REPLICAS}
	if has(in, factNNodes) {
		out = append(out, v1.Shape_SHAPE_CHAIN, v1.Shape_SHAPE_LOCKSTEP)
	}
	if has(in, factNIXL) {
		out = append(out, v1.Shape_SHAPE_RELAY)
	}
	return out
}

// Rank roles of the two engines that group ranks, one phase that rendezvous at one address: rank
// 0 heads and answers health, the others are headless and known by their process alone until the
// head is healthy. Ranks connect to each other on the mesh address, so none has a guard listener
func rankRoles() []Role {
	return []Role{
		{Name: RoleHead, Phase: 1, Files: FilesWeights, Health: RoleHealth{Kind: HealthHTTP}, Rendezvous: true, Head: true},
		{Name: RoleRank, Phase: 1, Files: FilesWeights, Health: RoleHealth{Kind: HealthProcess}, Rendezvous: true},
	}
}

// Relay roles of the two engines: the conductor's gateway drives both seats and the seats hand
// the cache to each other, and the pair is ready once each answers its health and one canary
// completion has gone through both
func relayRoles() []Role {
	return []Role{
		{Name: RolePrefill, Phase: 1, Files: FilesWeights, Health: RoleHealth{Kind: HealthHTTP}, Listens: true, ConnectsFrom: []string{RoleConductor, RoleDecode}},
		{Name: RoleDecode, Phase: 1, Files: FilesWeights, Health: RoleHealth{Kind: HealthCanary}, Listens: true, Head: true, ConnectsFrom: []string{RoleConductor, RolePrefill}},
	}
}

func replicaRoles() []Role {
	return []Role{{Name: RoleReplica, Phase: 1, Files: FilesWeights, Health: RoleHealth{Kind: HealthHTTP}, Listens: true, Head: true, ConnectsFrom: []string{RoleConductor}}}
}

func soloRoles() []Role {
	return []Role{{Name: RoleHead, Phase: 1, Files: FilesWeights, Health: RoleHealth{Kind: HealthHTTP}, Head: true}}
}

func (VLLM) Roles(shape v1.Shape) []Role {
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

func (VLLM) Transport(lines []string) string { return transportOf(lines) }

// Environment every rank of a collective takes: the interface the link probe found, and the
// fabric when the link has RDMA devices, merged when it has two, so the library takes it and not
// sockets. NCCL names the net it took only when asked, so its init and net lines are turned on
// for the transport measurement. The heartbeat follows the stop grace and the idle monitor is off,
// because the default monitor aborts an idle rank after ten minutes
func collectiveEnv(env map[string]string, seat *v1.SeatSpec, grace int) {
	if seat.GetInterface() != "" {
		env["NCCL_SOCKET_IFNAME"] = seat.GetInterface()
		env["GLOO_SOCKET_IFNAME"] = seat.GetInterface()
	}
	if rdma := seat.GetRdmaDevices(); len(rdma) > 0 {
		env["NCCL_NET"] = "IB"
		env["NCCL_IB_HCA"] = strings.Join(rdma, ",")
		env["NCCL_NET_PLUGIN"] = "none"
		if len(rdma) > 1 {
			env["NCCL_IB_MERGE_NICS"] = "1"
		}
	}
	env["NCCL_DEBUG"] = "INFO"
	env["NCCL_DEBUG_SUBSYS"] = "INIT,NET"
	env["TORCH_NCCL_HEARTBEAT_TIMEOUT_SEC"] = strconv.Itoa(grace)
	env["TORCH_NCCL_ENABLE_MONITORING"] = "0"
}

// Splits the rendezvous address, the head's mesh address and a port the conductor chose, into
// host and port
func rendezvousOf(seat *v1.SeatSpec) (string, string, error) {
	host, port, ok := strings.Cut(seat.GetRendezvous(), ":")
	if !ok || host == "" || port == "" {
		return "", "", fmt.Errorf("%w: a rank needs a rendezvous address as host:port, got %q", ErrParam, seat.GetRendezvous())
	}
	if i := strings.LastIndex(seat.GetRendezvous(), ":"); i > 0 {
		host, port = seat.GetRendezvous()[:i], seat.GetRendezvous()[i+1:]
	}
	return strings.Trim(host, "[]"), port, nil
}

// The pipeline and tensor sizes of a rank group: a chain is one pipeline stage per rank with the
// node's devices in tensor parallel, lockstep is one stage with every device across the ranks in
// tensor parallel. Every seat of these shapes is a rank, so the count is the peers and this seat
func rankSizes(seat *v1.SeatSpec, own int) (pp, tp int, err error) {
	if own == 0 {
		return 0, 0, fmt.Errorf("%w: a rank needs at least one GPU on its node", ErrParam)
	}
	if int(seat.GetCount()) != len(seat.GetPeers())+1 {
		return 0, 0, fmt.Errorf("%w: the seat block counts %d ranks and lists %d peers", ErrParam, seat.GetCount(), len(seat.GetPeers()))
	}
	if seat.GetShape() == v1.Shape_SHAPE_LOCKSTEP {
		return 1, rankDevices(seat, own), nil
	}
	return int(seat.GetCount()), own, nil
}

// The layer partition of a chain, as the engines take it in their partition variable: layers per
// rank in rank order, summing to the model's layers
func chainPartition(in Launch) (string, error) {
	layers := int(formats.ParamsOf(in.Descriptor.GetParams()).Layers)
	if layers <= 0 {
		return "", fmt.Errorf("%w: the descriptor counts no layers, so the ranks cannot partition them", ErrParam)
	}
	return rankPartition(in.Seat, layers)
}

// The NIXL handoff configuration a relay seat launches with
func nixlConfig(role string) string {
	kvRole := "kv_producer"
	if role == RoleDecode {
		kvRole = "kv_consumer"
	}
	return `{"kv_connector":"NixlConnector","kv_role":"` + kvRole + `"}`
}

func (r VLLM) LaunchSeat(in Launch) (*Command, error) {
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
			return nil, fmt.Errorf("%w: this vllm does not accept --nnodes, so it cannot run across nodes", ErrParam)
		}
		host, port, err := rendezvousOf(seat)
		if err != nil {
			return nil, err
		}
		pp, tp, err := rankSizes(seat, len(gpusOf(in.Devices)))
		if err != nil {
			return nil, err
		}
		p := in.Params.Clone()
		partition := ""
		if seat.GetShape() == v1.Shape_SHAPE_CHAIN {
			// Speculative settings do not compose with pipeline parallel on this runtime.
			p["speculative_config"] = ""
			if partition, err = chainPartition(in); err != nil {
				return nil, err
			}
		}
		p["tensor_parallel_size"] = int64(tp)
		in.Params = p
		cmd, err := r.Launch(exposedLaunch(in))
		if err != nil {
			return nil, err
		}
		cmd.Args = append(cmd.Args, "--pipeline-parallel-size", strconv.Itoa(pp), "--nnodes", strconv.FormatUint(uint64(seat.GetCount()), 10), "--node-rank", strconv.FormatUint(uint64(seat.GetRank()), 10), "--master-addr", host, "--master-port", port, "--distributed-executor-backend", "mp")
		if role.Name == RoleRank {
			cmd.Args = append(cmd.Args, "--headless")
		}
		cmd.Env["VLLM_HOST_IP"] = seat.GetAddress()
		if partition != "" {
			cmd.Env["VLLM_PP_LAYER_PARTITION"] = partition
			cmd.Params["pp_layer_partition"] = partition
		}
		collectiveEnv(cmd.Env, seat, grace)
		cmd.Params["pipeline_parallel_size"] = strconv.Itoa(pp)
		cmd.Params["nnodes"] = strconv.FormatUint(uint64(seat.GetCount()), 10)
		cmd.Params["node_rank"] = strconv.FormatUint(uint64(seat.GetRank()), 10)
		cmd.Params["master"] = seat.GetRendezvous()
		return cmd, nil
	case v1.Shape_SHAPE_RELAY:
		if !has(in.InstallRecord, factNIXL) {
			return nil, fmt.Errorf("%w: the NIXL connector does not import in this vllm's environment, so it cannot relay", ErrParam)
		}
		if seat.GetAuxPort() == 0 {
			return nil, fmt.Errorf("%w: a relay seat needs a side channel port", ErrParam)
		}
		cmd, err := r.Launch(exposedLaunch(in))
		if err != nil {
			return nil, err
		}
		cmd.Args = append(cmd.Args, "--kv-transfer-config", nixlConfig(role.Name))
		cmd.Env["VLLM_HOST_IP"] = seat.GetAddress()
		cmd.Env["VLLM_NIXL_SIDE_CHANNEL_HOST"] = seat.GetAddress()
		cmd.Env["VLLM_NIXL_SIDE_CHANNEL_PORT"] = strconv.FormatUint(uint64(seat.GetAuxPort()), 10)
		collectiveEnv(cmd.Env, seat, grace)
		cmd.Params["kv_role"] = role.Name
		return cmd, nil
	}
	return nil, fmt.Errorf("%w: vLLM renders no command for shape %s", ErrParam, shapeWord(seat.GetShape()))
}

// The GPU devices among a seat's
func gpusOf(devices []*v1.Device) []*v1.Device {
	var out []*v1.Device
	for _, d := range devices {
		if d.GetKind() == v1.DeviceKind_DEVICE_KIND_GPU || d.GetKind() == v1.DeviceKind_DEVICE_KIND_ACCELERATOR {
			out = append(out, d)
		}
	}
	return out
}
