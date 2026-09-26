package formations

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/mesh"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"google.golang.org/protobuf/proto"
)

// Starts a seat on this node for a conductor: the seat block gets this node's mesh address, the
// interface and RDMA devices of the links to its peers, its cache directory, its exposure, the
// addresses its guard listener admits, and, for the rank the others rendezvous with, the
// rendezvous address allocated here
func (m *Manager) RunSeat(ctx context.Context, conductor string, run *v1.RunRequest) (*v1.Instance, *v1.Task, error) {
	seat := run.GetSeat()
	if seat == nil {
		return nil, nil, fmt.Errorf("%w: a seat launch carries a seat block", ErrFormation)
	}
	if seat.GetConductor() != conductor {
		return nil, nil, fmt.Errorf("%w: the seat block names %s as conductor but %s asked", ErrFormation, seat.GetConductor(), conductor)
	}
	rt, err := m.Runtimes.Get(run.GetRuntimeId())
	if err != nil {
		return nil, nil, err
	}
	role, err := runtimes.RoleOf(rt, seat)
	if err != nil {
		return nil, nil, err
	}
	run = proto.Clone(run).(*v1.RunRequest)
	seat = run.Seat
	address := m.Mesh.Address()
	if address == "" {
		return nil, nil, fmt.Errorf("%w: this node has no mesh address for its seats", ErrFormation)
	}
	seat.Address = hostOf(address)
	seat.Interface, seat.RdmaDevices = m.linkFacts(conductor, seat)
	switch {
	case role.Files == runtimes.FilesNone && seat.GetCacheKey() != "":
		seat.CacheDir = filepath.Join(m.CacheDir, seat.GetCacheKey())
	case role.Files == runtimes.FilesNone:
		return nil, nil, fmt.Errorf("%w: a %s seat caches streamed tensors and its seat block names no cache key", ErrFormation, role.Name)
	default:
		seat.CacheDir = filepath.Join(m.CacheDir, seat.GetFormationId())
	}
	seat.Exposed = m.Mesh.Exposure() == mesh.ExposureDirect
	// A relay seat's own protocol takes a second port on the mesh address, a side channel or a
	// bootstrap port, and it saves slot files where the gateway's relay moves them from.
	if seat.GetShape() == v1.Shape_SHAPE_RELAY {
		port, err := m.freePortOn(seat.Address)
		if err != nil {
			return nil, nil, fmt.Errorf("side channel port on %s: %w", seat.Address, err)
		}
		seat.AuxPort = uint32(port)
		seat.SlotDir = m.slotDir(seat.GetFormationId())
	}
	if role.Rendezvous && m.rendezvousRank(rt, seat) {
		if seat.Rendezvous, err = m.rendezvousAddress(seat); err != nil {
			return nil, nil, err
		}
	}
	if seat.Admit, err = m.admitted(conductor, role, seat); err != nil {
		return nil, nil, err
	}
	return m.Instances.Run(ctx, run)
}

// Whether this seat is the rank the others rendezvous with: the head among the ranks, else rank 0
func (m *Manager) rendezvousRank(rt runtimes.Runtime, seat *v1.SeatSpec) bool {
	for _, r := range rt.Roles(seat.GetShape()) {
		if r.Rendezvous && r.Head {
			return r.Name == seat.GetRole()
		}
	}
	return seat.GetRank() == 0
}

// The address the ranks rendezvous at, on this node's mesh address: the one the seat block names
// when a relaunch carries it, checked free here, else a free port allocated now
func (m *Manager) rendezvousAddress(seat *v1.SeatSpec) (string, error) {
	if seat.GetRendezvous() == "" {
		port, err := m.freePortOn(seat.GetAddress())
		if err != nil {
			return "", fmt.Errorf("rendezvous port on %s: %w", seat.GetAddress(), err)
		}
		return net.JoinHostPort(seat.GetAddress(), strconv.Itoa(port)), nil
	}
	host, port, err := net.SplitHostPort(seat.GetRendezvous())
	if err != nil {
		return "", fmt.Errorf("%w: rendezvous %q is not host:port", ErrFormation, seat.GetRendezvous())
	}
	if host != seat.GetAddress() {
		return "", fmt.Errorf("%w: the ranks rendezvous at %s but this node's mesh address is %s", ErrFormation, seat.GetRendezvous(), seat.GetAddress())
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return "", fmt.Errorf("the rendezvous port %s the ranks met at before is not free: %w", seat.GetRendezvous(), err)
	}
	ln.Close()
	return seat.GetRendezvous(), nil
}

// The interface the links to the seat's peers and conductor leave through and the RDMA devices
// bound on them, from this node's link records
func (m *Manager) linkFacts(conductor string, seat *v1.SeatSpec) (string, []string) {
	var others []string
	seen := map[string]bool{}
	consider := func(id string) {
		if id == "" || id == m.self() || seen[id] {
			return
		}
		seen[id] = true
		others = append(others, id)
	}
	for _, p := range seat.GetPeers() {
		consider(p.GetNodeId())
	}
	consider(conductor)
	var iface string
	var rdma []string
	haveRDMA := map[string]bool{}
	for _, id := range others {
		link := m.Mesh.Link(id)
		if link == nil {
			continue
		}
		if iface == "" {
			iface = link.GetInterface()
		}
		for _, dev := range strings.Split(link.GetRdmaDevice(), ",") {
			dev = strings.TrimSpace(dev)
			if dev == "" || haveRDMA[dev] {
				continue
			}
			haveRDMA[dev] = true
			rdma = append(rdma, dev)
		}
	}
	sort.Strings(rdma)
	return iface, rdma
}

// The hosts a seat's guard listener admits: the conductor, which connects as the route's gateway
// and for readiness, and the seats of the roles the runtime says connect to this one. Every one
// must have a known address, so the guard never admits by guesswork.
func (m *Manager) admitted(conductor string, role runtimes.Role, seat *v1.SeatSpec) ([]string, error) {
	var admit []string
	seen := map[string]bool{}
	add := func(nodeID, who string) error {
		host, err := m.nodeHost(nodeID)
		if err != nil {
			return fmt.Errorf("%w: the guard of %s cannot admit %s: %v", ErrFormation, role.Name, who, err)
		}
		if !seen[host] {
			seen[host] = true
			admit = append(admit, host)
		}
		return nil
	}
	if err := add(conductor, "the conductor"); err != nil {
		return nil, err
	}
	connects := map[string]bool{}
	for _, from := range role.ConnectsFrom {
		connects[from] = true
	}
	for _, p := range seat.GetPeers() {
		if !connects[p.GetRole()] {
			continue
		}
		if err := add(p.GetNodeId(), fmt.Sprintf("%s%d on %s", p.GetRole(), p.GetRank(), p.GetNodeId())); err != nil {
			return nil, err
		}
	}
	return admit, nil
}

// The mesh host of a node: this node's own address, or the member's record address
func (m *Manager) nodeHost(id string) (string, error) {
	if id == m.self() {
		address := m.Mesh.Address()
		if address == "" {
			return "", fmt.Errorf("this node has no mesh address")
		}
		return hostOf(address), nil
	}
	rec, err := m.Mesh.Node(id)
	if err != nil {
		return "", err
	}
	if rec.GetAddress() == "" {
		return "", fmt.Errorf("%s has no mesh address in its record", rec.GetName())
	}
	return hostOf(rec.GetAddress()), nil
}

// Stops a seat on this node
func (m *Manager) StopSeat(ctx context.Context, instanceID string) (*v1.Instance, error) {
	return m.Instances.Stop(ctx, instanceID)
}

// Reads a seat's instance on this node, its guard counter and transport line read first
func (m *Manager) GetSeat(instanceID string) (*v1.Instance, error) {
	return m.Instances.Refresh(instanceID)
}

// Streams a seat's output: the head when no role is named, from its node
func (m *Manager) Logs(ctx context.Context, id, nodeID, role string, follow bool, tail int, send func(node, role string, lines []string) error) error {
	f, err := m.Get(id)
	if err != nil {
		return err
	}
	var seat *v1.Seat
	for _, s := range f.GetSeats() {
		if nodeID != "" && s.GetNodeId() != nodeID && s.GetNodeName() != nodeID {
			continue
		}
		if role != "" && s.GetRole() != role {
			continue
		}
		if role == "" && nodeID == "" {
			continue
		}
		seat = s
		break
	}
	if seat == nil {
		if role != "" || nodeID != "" {
			return fmt.Errorf("%w: %s has no %s seat on %s", ErrFormation, f.GetName(), orAny(role), orAny(nodeID))
		}
		seat = m.headSeat(f)
	}
	if seat == nil || seat.GetInstanceId() == "" {
		return fmt.Errorf("%w: the seat has no instance yet", ErrFormation)
	}
	if seat.GetNodeId() == m.self() {
		return m.Instances.Logs(ctx, seat.GetInstanceId(), follow, tail, func(lines []string) error {
			return send(seat.GetNodeId(), seat.GetRole(), lines)
		})
	}
	cl, err := m.Mesh.Client(ctx, seat.GetNodeId())
	if err != nil {
		return err
	}
	stream, err := cl.Mesh.SeatLogs(ctx, connect.NewRequest(&v1.SeatLogsRequest{InstanceId: seat.GetInstanceId(), Follow: follow, Tail: uint32(tail)}))
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
		if err := send(seat.GetNodeId(), seat.GetRole(), stream.Msg().GetLines()); err != nil {
			return err
		}
	}
	return stream.Err()
}

func orAny(s string) string {
	if s == "" {
		return "any"
	}
	return s
}
