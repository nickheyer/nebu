package runtimes

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// What a seat's node must hold before the seat launches
type FileNeed int

const (
	// Nothing, the head streams tensors to the seat at start
	FilesNone FileNeed = iota
	// The weight group and its projector
	FilesWeights
	// Every part the blueprint fills
	FilesParts
)

// How a seat is known ready
type HealthKind int

const (
	// An HTTP path answering 200
	HealthHTTP HealthKind = iota
	// The process alive and one hello exchange on its port
	HealthHello
	// The process alive
	HealthProcess
	// An HTTP path answering 200 on the seat's node, then one short completion the conductor sends
	// through the relay pair before the formation is ready, so the cache handoff is proven
	HealthCanary
)

// Readiness of one role
type RoleHealth struct {
	Kind     HealthKind
	Path     string
	Interval time.Duration
	Timeout  time.Duration
}

// One role of a shape: what the seat runs, when it launches, what it needs, and how it is known ready
type Role struct {
	Name  string
	Phase int
	// What the seat's node must hold: weights, parts, or nothing
	Files FileNeed
	// How the seat is known ready: an HTTP path, a hello exchange, the process alone, or an HTTP
	// path followed by the conductor's canary request through the relay pair
	Health RoleHealth
	// Whether other seats or the conductor's gateway connect to it, so it gets a guard listener
	Listens bool
	// Whether the guard listener admits exactly one connection at a time
	Single bool
	// Whether ranks rendezvous at one address and launch together
	Rendezvous bool
	// Whether the seat answers the route
	Head bool
	// The roles whose seats connect to this one, RoleConductor standing for the conductor's gateway
	// and readiness checks, so the guard listener admits their addresses and refuses every other
	ConnectsFrom []string
}

// Role names shared across runtimes
const (
	RoleHead     = "head"
	RoleStage    = "stage"
	RoleRank     = "rank"
	RolePrefill  = "prefill"
	RoleDecode   = "decode"
	RoleReplica  = "replica"
	RoleDenoiser = "denoiser"
	// The conductor's node, which connects to a seat as the route's gateway and for readiness
	RoleConductor = "conductor"
)

// The role of a seat block on a runtime, or an error naming the shape it does not play
func RoleOf(rt Runtime, seat *v1.SeatSpec) (Role, error) {
	for _, r := range rt.Roles(seat.GetShape()) {
		if r.Name == seat.GetRole() {
			return r, nil
		}
	}
	return Role{}, fmt.Errorf("%w: %s has no role %q in shape %s", ErrParam, rt.ID(), seat.GetRole(), strings.ToLower(strings.TrimPrefix(seat.GetShape().String(), "SHAPE_")))
}

// The transport a seat's log names and the line naming it: rdma when the collective or the rpc
// transport activated an RDMA device, sockets when it fell back or never had one, empty until a
// line says. ggml's rpc transport logs "RDMA activated: qpn=…" ("RDMA(Apple/UC) activated" over
// Thunderbolt) once a connection upgrades and "RDMA activate failed, staying on TCP" when the
// upgrade fails, and a server built without RDMA prints "transport      : TCP" at start. NCCL
// logs "NET/IB" or "NET/Socket" for the net it took.
func transportLine(lines []string) (string, string) {
	for _, line := range lines {
		switch {
		case strings.Contains(line, "NET/IB"), strings.Contains(line, "RDMA activated"), strings.Contains(line, "RDMA(Apple/UC) activated"):
			return "rdma", line
		case strings.Contains(line, "NET/Socket"), strings.Contains(line, "staying on TCP"):
			return "sockets", line
		case strings.Contains(line, "transport      : TCP") && !strings.Contains(line, "RDMA"):
			return "sockets", line
		}
	}
	return "", ""
}

// The transport a seat's log names, empty until a line says
func transportOf(lines []string) string {
	t, _ := transportLine(lines)
	return t
}

// The transport as a measurement, keyed transport.rdma or transport.sockets with the line that
// named it, none until a line says
func transportMeasurement(lines []string) []*v1.Measurement {
	t, line := transportLine(lines)
	if t == "" {
		return nil
	}
	return []*v1.Measurement{{Key: "transport." + t, Line: line}}
}

// Health of a role, filled from the runtime's solo health where the role leaves fields empty
func (r Role) health(base Health) RoleHealth {
	h := r.Health
	if (h.Kind == HealthHTTP || h.Kind == HealthCanary) && h.Path == "" {
		h.Path = base.Path
	}
	if h.Interval <= 0 {
		h.Interval = base.Interval
	}
	if h.Timeout <= 0 {
		h.Timeout = base.Timeout
	}
	return h
}

// Health of a seat's role on its own node, the solo health filling what the role leaves empty. A
// canary role's seat is ready on its node by its HTTP path; the canary request is the conductor's
// step after every seat is ready, read from the role itself
func SeatHealth(rt Runtime, seat *v1.SeatSpec) (RoleHealth, error) {
	role, err := RoleOf(rt, seat)
	if err != nil {
		return RoleHealth{}, err
	}
	h := role.health(rt.Health())
	if h.Kind == HealthCanary {
		h.Kind = HealthHTTP
	}
	return h, nil
}

// Whether an install fact holds a flag or path
func has(in *v1.Install, key string) bool {
	return strings.TrimSpace(in.GetFacts()[key]) != ""
}

// A probe that records which of several flag spellings the binary's help lists, the first listed
// spelling winning
func flagProbe(key string, args []string, spellings ...string) Probe {
	return Probe{Key: key, Args: args, Parse: func(out string) (string, bool) {
		for _, f := range spellings {
			if strings.Contains(out, f) {
				return f, true
			}
		}
		return "", false
	}}
}

// The seat's peers with a role, in rank order
func peersOf(seat *v1.SeatSpec, role string) []*v1.SeatPeer {
	var out []*v1.SeatPeer
	for _, p := range seat.GetPeers() {
		if p.GetRole() == role {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetRank() < out[j].GetRank() })
	return out
}

// The devices a rank group holds across every node: this seat's own and every peer's, since the
// peers of a rank are the other ranks
func rankDevices(seat *v1.SeatSpec, own int) int {
	n := own
	for _, p := range seat.GetPeers() {
		n += int(p.GetDevices())
	}
	return n
}

// The layer counts of a rank group by rank, as the pipeline partition lists them: the seat's own
// range and every peer's, every rank 0..N-1 present once and the counts summing to the model's
// layers, or an error naming what the plan left out
func rankPartition(seat *v1.SeatSpec, layers int) (string, error) {
	type span struct{ from, to uint32 }
	byRank := map[uint32]span{seat.GetRank(): {seat.GetLayerFrom(), seat.GetLayerTo()}}
	for _, p := range seat.GetPeers() {
		if _, dup := byRank[p.GetRank()]; dup {
			return "", fmt.Errorf("%w: the seat block lists rank %d twice", ErrParam, p.GetRank())
		}
		byRank[p.GetRank()] = span{p.GetLayerFrom(), p.GetLayerTo()}
	}
	counts := make([]string, len(byRank))
	sum := 0
	for rank := range byRank {
		if int(rank) >= len(byRank) {
			return "", fmt.Errorf("%w: the seat block names rank %d among %d ranks", ErrParam, rank, len(byRank))
		}
	}
	for rank := 0; rank < len(byRank); rank++ {
		s := byRank[uint32(rank)]
		if s.to <= s.from {
			return "", fmt.Errorf("%w: the plan gives rank %d no layer range", ErrParam, rank)
		}
		counts[rank] = strconv.Itoa(int(s.to - s.from))
		sum += int(s.to - s.from)
	}
	if sum != layers {
		return "", fmt.Errorf("%w: the plan's layer ranges cover %d layers of a %d layer model", ErrParam, sum, layers)
	}
	return strings.Join(counts, ","), nil
}

// Names the devices a ggml backend enumerates them by, matching the install's device fact:
// CUDA0, ROCm0, Vulkan0, or Metal0, in the order the seat lists them
func ggmlDevices(in *v1.Install, devices []*v1.Device) []string {
	known := strings.Split(in.GetFacts()["devices"], ",")
	prefixFor := func(d *v1.Device) string {
		candidates := []string{}
		switch d.GetVendor() {
		case "nvidia":
			candidates = []string{"CUDA", "Vulkan"}
		case "amd":
			candidates = []string{"ROCm", "HIP", "Vulkan"}
		case "apple":
			candidates = []string{"Metal"}
		default:
			candidates = []string{"Vulkan", "CUDA", "ROCm", "Metal"}
		}
		for _, c := range candidates {
			for _, k := range known {
				if strings.HasPrefix(strings.TrimSpace(k), c) {
					return c
				}
			}
		}
		return candidates[0]
	}
	var out []string
	for _, d := range devices {
		if d.GetKind() == v1.DeviceKind_DEVICE_KIND_CPU {
			continue
		}
		index := d.GetFacts()["index"]
		if index == "" {
			index = strconv.Itoa(len(out))
		}
		out = append(out, prefixFor(d)+index)
	}
	return out
}

// The host and port a seat process binds: loopback, or the mesh address when exposed
func seatBind(in Launch) (string, int) {
	if in.Seat.GetExposed() && in.Seat.GetAddress() != "" {
		return in.Seat.GetAddress(), int(in.Seat.GetPort())
	}
	return in.Host, in.Port
}

// The names ggml gives the devices of rpc servers: RPC0, RPC1, … counted across every server in
// the order the --rpc list names them, one per device a server exposes. Returns the names per
// stage, or an error for a stage that exposes no device or has no address yet
func rpcDeviceNames(stages []*v1.SeatPeer) ([][]string, error) {
	out := make([][]string, len(stages))
	n := 0
	for i, s := range stages {
		if s.GetAddress() == "" {
			return nil, fmt.Errorf("%w: stage %d on %s has no address yet", ErrParam, s.GetRank(), s.GetNodeId())
		}
		if s.GetDevices() == 0 {
			return nil, fmt.Errorf("%w: stage %d on %s exposes no device", ErrParam, s.GetRank(), s.GetNodeId())
		}
		for range int(s.GetDevices()) {
			out[i] = append(out[i], "RPC"+strconv.Itoa(n))
			n++
		}
	}
	return out, nil
}

// Joins planned bytes per device into a tensor split
func tensorSplit(bytes []uint64) string {
	parts := make([]string, len(bytes))
	for i, b := range bytes {
		parts[i] = strconv.FormatUint(b, 10)
	}
	return strings.Join(parts, ",")
}
