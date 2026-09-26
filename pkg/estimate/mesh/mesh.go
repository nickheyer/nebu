// Package mesh plans one model across the nodes of a mesh: the shape, the seats, what each holds,
// and the time to first token and tokens per second every candidate would give, priced with the
// links the prober measured and the numbers the devices learned or declare.
package mesh

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/text"
)

const (
	// Nodes a chain may hold, the fastest kept beyond
	MaxChainNodes = 8
	// Reference sizes of the chat profile
	ChatPrompt     = 2048
	ChatCompletion = 256
	// Prompt the agent profile assumes when the route has no traces yet
	AgentPrompt = 8192
	// Concurrency the batch profile assumes when the route has no traces yet
	BatchConcurrency = 8
	// Latency of one reduction on a fabric link
	fabricReduction = 45e-6
	// Prompt lengths a relay's break even is searched up to
	relayMaxPrompt = 131072
	// Draft tokens per round before the head's parameter says otherwise
	defaultDraftTokens = 5
	// Seconds a replicas route keeps a conversation on the seat whose cache holds its prefix
	AffinityWindowSeconds = 600
	// The param group speculative settings live in on every runtime
	speculativeGroup = "Speculative decoding"
)

// Returned when the request cannot be planned at all, as opposed to no candidate fitting
var ErrPlan = errors.New("cannot plan")

// One accelerator or CPU with its pool and the numbers the planner prices it with
type Device struct {
	Device  *v1.Device
	Pool    *v1.MemoryPool
	Node    string
	Numbers perf.Numbers
}

func (d *Device) ID() string { return d.Device.GetId() }

// Names the device for a source line
func (d *Device) label() string {
	if d.Device.GetName() != "" {
		return d.Device.GetName()
	}
	return d.ID()
}

// One member as the planner sees it: its profile, the install of the runtime being planned, the
// shapes that install supports, and its devices with numbers
type Node struct {
	ID, Name string
	Profile  *v1.HostProfile
	Install  *v1.Install
	Shapes   []v1.Shape
	// Accelerators with device or unified pools, fastest first
	Devices []*Device
	// The CPU with the host pool, nil when no host pool was probed
	CPU *Device
	// The node's disk, what a relay's saved cache moves through
	Disk perf.Numbers
	// Vendor of the accelerators, empty for a node without any
	Vendor string
}

// Whether the node's install supports a shape
func (n *Node) Supports(shape v1.Shape) bool {
	for _, s := range n.Shapes {
		if s == shape {
			return true
		}
	}
	return false
}

// Streaming bandwidth of the node's fastest device, or its CPU
func (n *Node) beta() float64 {
	if len(n.Devices) > 0 {
		return n.Devices[0].Numbers.Stream
	}
	if n.CPU != nil {
		return n.CPU.Numbers.Stream
	}
	return 0
}

// Prefill compute of the node's fastest device, or its CPU
func (n *Node) gamma() float64 {
	if len(n.Devices) > 0 {
		return n.Devices[0].Numbers.Compute
	}
	if n.CPU != nil {
		return n.CPU.Numbers.Compute
	}
	return 0
}

// The nodes and links a plan may use
type Mesh struct {
	Nodes []*Node
	links map[string]*v1.Link
	// This node's id, the conductor
	Self string
}

// Builds a mesh from nodes and the links measured between them
func New(nodes []*Node, links []*v1.Link, self string) *Mesh {
	m := &Mesh{Nodes: nodes, links: map[string]*v1.Link{}, Self: self}
	for _, l := range links {
		m.links[l.GetFrom()+"\x00"+l.GetTo()] = l
	}
	return m
}

// The link measured from one node to another, nil when unmeasured
func (m *Mesh) Link(from, to string) *v1.Link {
	if from == to {
		return nil
	}
	return m.links[from+"\x00"+to]
}

func (m *Mesh) Node(id string) *Node {
	for _, n := range m.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// Builds a node from a member's record: its devices with pools and numbers, the runtime's install
// and shapes, and the CPU with the host pool
func NodeOf(rec *v1.Node, runtimeID, installID string, capabilities map[string][]v1.Shape, numbers func(*v1.Device) perf.Numbers) *Node {
	profile := rec.GetProfile()
	n := &Node{ID: rec.GetId(), Name: rec.GetName(), Profile: profile}
	for _, in := range rec.GetInstalls() {
		if in.GetRuntimeId() != runtimeID {
			continue
		}
		if in.GetId() == installID {
			n.Install = in
			break
		}
		if n.Install == nil || in.GetCreatedAt().AsTime().After(n.Install.GetCreatedAt().AsTime()) {
			n.Install = in
		}
	}
	if n.Install != nil {
		n.Shapes = capabilities[n.Install.GetId()]
	}
	pools := map[string]*v1.MemoryPool{}
	var host, unified *v1.MemoryPool
	for _, p := range profile.GetPools() {
		if p.GetDeviceId() != "" {
			pools[p.GetDeviceId()] = p
		}
		switch p.GetKind() {
		case v1.PoolKind_POOL_KIND_HOST:
			if host == nil {
				host = p
			}
		case v1.PoolKind_POOL_KIND_UNIFIED:
			if unified == nil {
				unified = p
			}
		}
	}
	for _, d := range profile.GetDevices() {
		dev := &Device{Device: d, Node: rec.GetId(), Numbers: numbers(d)}
		switch d.GetKind() {
		case v1.DeviceKind_DEVICE_KIND_CPU:
			dev.Pool = host
			if dev.Pool == nil {
				dev.Pool = unified
			}
			if n.CPU == nil {
				n.CPU = dev
			}
		default:
			dev.Pool = pools[d.GetId()]
			if dev.Pool == nil {
				dev.Pool = unified
			}
			if dev.Pool == nil {
				continue
			}
			n.Devices = append(n.Devices, dev)
			if n.Vendor == "" {
				n.Vendor = d.GetVendor()
			}
		}
	}
	sort.SliceStable(n.Devices, func(i, j int) bool { return n.Devices[i].Numbers.Stream > n.Devices[j].Numbers.Stream })
	n.Disk = numbers(&v1.Device{Id: perf.DiskDevice, Name: perf.DiskDevice})
	return n
}

// What the route's traces say about its requests
type Stats struct {
	MedianPrompt, MedianCompletion uint32
	PeakConcurrency                uint32
	Traces                         int
}

// A plan request
type Request struct {
	Descriptor *v1.Descriptor
	Family     archs.Arch
	Runtime    runtimes.Runtime
	// Layered overrides: slot defaults under request params
	Params    map[string]string
	Placement v1.Placement
	// The shape asked for, auto when unspecified
	Shape   v1.Shape
	Profile v1.PlanProfile
	// Node ids the plan may use, every node when empty
	Span []string
	// Plan against free memory instead of total
	Free       bool
	Repo       string
	Companions []*v1.StoredModel
	// The draft model the request's draft_model param names, nil when none, with its attention family
	Draft       *v1.Descriptor
	DraftFamily archs.Arch
	DraftRepo   string
	Stats       Stats
	// Ratios and device numbers, nil for a plan corrected by nothing
	Table *perf.Table
	// Learned overhead correction per node
	Calibration func(nodeID string) float64
	// Nodes of the span the caller left out of the mesh, with why, so the plan lists them
	Excluded map[string]string
}

// Whether the request sets any speculative decoding param
func (r Request) speculativeAsked() bool {
	for _, p := range r.Runtime.Params() {
		if p.GetGroup() != speculativeGroup {
			continue
		}
		v := strings.TrimSpace(r.Params[p.GetName()])
		if v != "" && !strings.EqualFold(v, runtimes.Auto) && !strings.EqualFold(v, runtimes.None) {
			return true
		}
	}
	return false
}

// The class of the slowest link among a set of nodes, in either direction, and whether every
// pair was measured
func (m *Mesh) worstClass(ids []string) (v1.LinkClass, *v1.Link, bool) {
	worst := v1.LinkClass_LINK_CLASS_UNSPECIFIED
	var slowest *v1.Link
	for i, a := range ids {
		for _, b := range ids[i+1:] {
			for _, l := range []*v1.Link{m.Link(a, b), m.Link(b, a)} {
				if l == nil {
					return v1.LinkClass_LINK_CLASS_UNSPECIFIED, nil, false
				}
				if l.GetClass() > worst {
					worst, slowest = l.GetClass(), l
				}
			}
		}
	}
	return worst, slowest, true
}

// The first pair among a set of nodes whose link is unmeasured in either direction
func (m *Mesh) unmeasured(ids []string) (string, string, bool) {
	for i, a := range ids {
		for _, b := range ids[i+1:] {
			if m.Link(a, b) == nil {
				return a, b, true
			}
			if m.Link(b, a) == nil {
				return b, a, true
			}
		}
	}
	return "", "", false
}

// Round trip between two nodes in seconds, the slower direction
func (m *Mesh) rtt(a, b string) (float64, bool) {
	x, y := m.Link(a, b), m.Link(b, a)
	if x == nil || y == nil {
		return 0, false
	}
	return float64(max(x.GetRttUs(), y.GetRttUs())) / 1e6, true
}

// Bytes per second from one node to another over one connection
func (m *Mesh) stream(from, to string) (float64, bool) {
	l := m.Link(from, to)
	if l == nil || l.GetStreamBytesPerSecond() == 0 {
		return 0, false
	}
	return float64(l.GetStreamBytesPerSecond()), true
}

// Bytes per second from one node to another over many connections or the RDMA device, the stream
// when the aggregate run was skipped
func (m *Mesh) aggregate(from, to string) (float64, bool) {
	l := m.Link(from, to)
	if l == nil || l.GetAggregateBytesPerSecond() == 0 {
		return m.stream(from, to)
	}
	return float64(l.GetAggregateBytesPerSecond()), true
}

// Bytes per second from one node to another as a transport takes it: the aggregate or one stream
func (m *Mesh) bandwidth(from, to string, aggregate bool) (float64, bool) {
	if aggregate {
		return m.aggregate(from, to)
	}
	return m.stream(from, to)
}

// Whether both ends of a link have an RDMA device bound
func (m *Mesh) rdma(a, b string) bool {
	x, y := m.Link(a, b), m.Link(b, a)
	return x != nil && y != nil && x.GetRdmaDevice() != "" && y.GetRdmaDevice() != ""
}

func className(c v1.LinkClass) string { return text.Enum(c) }

func shapeName(s v1.Shape) string { return strings.ToLower(strings.TrimPrefix(s.String(), "SHAPE_")) }

// Names a node for a reason line
func (n *Node) label() string {
	if n.Name != "" {
		return n.Name
	}
	return n.ID
}

// Human readable seconds
func seconds(s float64) string {
	switch {
	case s >= 1:
		return fmt.Sprintf("%.1f s", s)
	case s >= 1e-3:
		return fmt.Sprintf("%.0f ms", s*1e3)
	}
	return fmt.Sprintf("%.0f µs", s*1e6)
}

// Human readable bytes per second in decimal units
func rate(bps float64) string {
	switch {
	case bps >= 1e12:
		return fmt.Sprintf("%.1f TB/s", bps/1e12)
	case bps >= 1e9:
		return fmt.Sprintf("%.0f GB/s", bps/1e9)
	case bps >= 1e6:
		return fmt.Sprintf("%.0f MB/s", bps/1e6)
	}
	return fmt.Sprintf("%.0f B/s", bps)
}

func gbits(bps float64) string { return fmt.Sprintf("%.1f Gb/s", bps*8/1e9) }

func human(b uint64) string { return estimate.Human(b) }
