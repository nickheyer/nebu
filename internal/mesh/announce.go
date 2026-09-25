package mesh

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"runtime"
	"sort"
	"sync"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/net/ipv4"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// The multicast group and port beacons use
	announceGroup = "239.255.73.66:8486"
	// Beacons go out this often
	AnnounceInterval = 10 * time.Second
	beaconMagic      = "nebu-mesh\x00"
	// A node whose beacon has not been heard this long leaves the nearby table
	NearbyTTL = 4*AnnounceInterval + 5*time.Second
	// An address heard is tried again only after this long
	contactBackoff = time.Minute
	// Interfaces are listed again this often, so one brought up later joins the group
	ifaceRefresh = time.Minute
)

// What a beacon carries: who the node is, where it listens, and the mesh it belongs to
type beacon struct {
	Node        string `json:"node"`
	Name        string `json:"name"`
	Address     string `json:"address,omitempty"`
	TLS         bool   `json:"tls,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Version     string `json:"version,omitempty"`
	OS          string `json:"os,omitempty"`
	Arch        string `json:"arch,omitempty"`
	// sha256 of the mesh id in hex, empty when the node belongs to none
	Mesh     string `json:"mesh,omitempty"`
	MeshName string `json:"mesh_name,omitempty"`
	Members  uint32 `json:"members,omitempty"`
	MeshTLS  bool   `json:"mesh_tls,omitempty"`
}

// A node heard on the network
type heard struct {
	rec *v1.NearbyNode
	at  time.Time
}

// Sends a beacon every interval on every multicast interface and hears the others, whether or not
// this node belongs to a mesh: members find each other, and the Mesh page lists the nodes and
// meshes nearby so admission runs from it
func (m *Manager) announceLoop(ctx context.Context) {
	if !m.announceOn() {
		return
	}
	group, err := net.ResolveUDPAddr("udp4", announceGroup)
	if err != nil {
		m.Log.Warn("mesh announce off", "err", err)
		return
	}
	listener, err := net.ListenMulticastUDP("udp4", nil, group)
	if err != nil {
		m.Log.Warn("mesh announce off, the multicast group could not be joined", "group", announceGroup, "err", err)
		return
	}
	defer listener.Close()
	listener.SetReadBuffer(64 << 10)
	receiver := ipv4.NewPacketConn(listener)
	sendConn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		m.Log.Warn("mesh announce off, no socket to send beacons from", "err", err)
		return
	}
	defer sendConn.Close()
	sender := ipv4.NewPacketConn(sendConn)
	sender.SetMulticastTTL(1)
	sender.SetMulticastLoopback(true)
	var tried sync.Map
	go func() {
		buf := make([]byte, 4096)
		for {
			n, _, from, err := receiver.ReadFrom(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			source := ""
			if u, ok := from.(*net.UDPAddr); ok {
				source = u.IP.String()
			}
			m.heard(ctx, buf[:n], source, &tried)
		}
	}()
	ticker := time.NewTicker(AnnounceInterval)
	defer ticker.Stop()
	var ifaces []net.Interface
	listedAt := time.Time{}
	for {
		if time.Since(listedAt) >= ifaceRefresh {
			ifaces = m.multicastInterfaces(receiver, group)
			listedAt = time.Now()
		}
		if payload := m.beacon(); payload != nil {
			for i := range ifaces {
				if err := sender.SetMulticastInterface(&ifaces[i]); err != nil {
					continue
				}
				sender.WriteTo(payload, nil, group)
			}
		}
		m.expireNearby()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// The interfaces beacons go out on, each joined to the group for receiving: up, multicast capable,
// not loopback, and holding an IPv4 address
func (m *Manager) multicastInterfaces(receiver *ipv4.PacketConn, group *net.UDPAddr) []net.Interface {
	list, err := net.Interfaces()
	if err != nil {
		m.Log.Warn("mesh announce: interfaces could not be listed", "err", err)
		return nil
	}
	var out []net.Interface
	for _, ifc := range list {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagMulticast == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		v4 := false
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.To4() != nil {
				v4 = true
				break
			}
		}
		if !v4 {
			continue
		}
		// Joining twice on the same interface is refused by the kernel and changes nothing.
		receiver.JoinGroup(&ifc, &net.UDPAddr{IP: group.IP})
		out = append(out, ifc)
	}
	return out
}

// The beacon this node sends: its identity and listener, and its mesh when it belongs to one
func (m *Manager) beacon() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	fingerprint, hasTLS := m.servedCertLocked()
	b := beacon{
		Node:        m.identity.ID,
		Name:        m.name(),
		Address:     m.advertiseLocked(),
		TLS:         hasTLS,
		Fingerprint: fingerprint,
		Version:     m.Version,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
	}
	if m.mesh != nil {
		b.Mesh, b.MeshName, b.Members, b.MeshTLS = meshHex(m.mesh.ID), m.mesh.Name, uint32(len(m.members)+1), m.mesh.TLS
	}
	body, _ := json.Marshal(b)
	return append([]byte(beaconMagic), body...)
}

// The hash of a mesh id as beacons and admissions carry it
func meshHex(id string) string {
	return hex.EncodeToString(meshHash(id))
}

// The hash of a mesh id as beacons and admissions carry it, for callers outside the package
func HashOf(id string) string { return meshHex(id) }

// Takes a beacon: records the node as nearby and, when it names this node's mesh and this node has
// not met it, contacts it
func (m *Manager) heard(ctx context.Context, payload []byte, source string, tried *sync.Map) {
	if !bytes.HasPrefix(payload, []byte(beaconMagic)) {
		return
	}
	var b beacon
	if err := json.Unmarshal(payload[len(beaconMagic):], &b); err != nil || b.Node == "" {
		return
	}
	if b.Node == m.identity.ID {
		return
	}
	rec := &v1.NearbyNode{
		Id:             b.Node,
		Name:           b.Name,
		Address:        b.Address,
		Tls:            b.TLS,
		TlsFingerprint: b.Fingerprint,
		Version:        b.Version,
		Os:             b.OS,
		Arch:           b.Arch,
		MeshHash:       b.Mesh,
		MeshName:       b.MeshName,
		MeshMembers:    b.Members,
		MeshTls:        b.MeshTLS,
		HeardAt:        timestamppb.Now(),
		From:           source,
	}
	m.mu.Lock()
	mesh := m.mesh
	mb, known := m.members[b.Node]
	met := known && !mb.sketch && mb.rec.GetAddress() == b.Address
	rec.Member = known
	old, seen := m.nearby[b.Node]
	changed := !seen || !sameNearby(old.rec, rec)
	m.nearby[b.Node] = &heard{rec: rec, at: time.Now()}
	reached := m.noteReachedLocked(b.Node, b.Address)
	m.mu.Unlock()
	if changed || reached {
		m.publishStatus()
	}
	if mesh == nil || b.Mesh != meshHex(mesh.ID) {
		return
	}
	if met {
		return
	}
	if b.Address == "" {
		return
	}
	if last, ok := tried.Load(b.Node); ok && time.Since(last.(time.Time)) < contactBackoff {
		return
	}
	tried.Store(b.Node, time.Now())
	go func() {
		resp, err := m.handshake(ctx, b.Address, b.TLS, b.Fingerprint, nil, nil)
		if err != nil {
			m.Log.Debug("mesh beacon contact failed", "node", b.Node, "address", b.Address, "err", err)
			return
		}
		m.absorb(resp.GetNode(), resp.GetMembers(), true)
		m.Log.Info("mesh member found by beacon", "node", b.Node, "address", b.Address)
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}()
}

// Whether two beacons say the same about a node, the time heard aside
func sameNearby(a, b *v1.NearbyNode) bool {
	return a.GetName() == b.GetName() && a.GetAddress() == b.GetAddress() && a.GetTls() == b.GetTls() && a.GetTlsFingerprint() == b.GetTlsFingerprint() &&
		a.GetVersion() == b.GetVersion() && a.GetMeshHash() == b.GetMeshHash() && a.GetMeshName() == b.GetMeshName() && a.GetMeshMembers() == b.GetMeshMembers() &&
		a.GetMeshTls() == b.GetMeshTls() && a.GetMember() == b.GetMember() && a.GetFrom() == b.GetFrom()
}

// Drops nodes unheard past the TTL
func (m *Manager) expireNearby() {
	m.mu.Lock()
	changed := false
	for id, h := range m.nearby {
		if time.Since(h.at) > NearbyTTL {
			delete(m.nearby, id)
			changed = true
		}
	}
	m.mu.Unlock()
	if changed {
		m.publishStatus()
	}
}

// The node heard under an id, nil when none was
func (m *Manager) nearbyLocked(id string) *v1.NearbyNode {
	if h, ok := m.nearby[id]; ok {
		return proto.Clone(h.rec).(*v1.NearbyNode)
	}
	return nil
}

// Every node heard, members flagged, by name
func (m *Manager) nearbyListLocked() []*v1.NearbyNode {
	out := make([]*v1.NearbyNode, 0, len(m.nearby))
	for id, h := range m.nearby {
		rec := proto.Clone(h.rec).(*v1.NearbyNode)
		_, rec.Member = m.members[id]
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetName() != out[j].GetName() {
			return out[i].GetName() < out[j].GetName()
		}
		return out[i].GetId() < out[j].GetId()
	})
	return out
}

// The meshes heard, from their members' beacons, this node's own mesh left out
func (m *Manager) meshesNearbyLocked() []*v1.NearbyMesh {
	own := ""
	if m.mesh != nil {
		own = meshHex(m.mesh.ID)
	}
	byHash := map[string]*v1.NearbyMesh{}
	for _, h := range m.nearby {
		hash := h.rec.GetMeshHash()
		if hash == "" || hash == own {
			continue
		}
		nm, ok := byHash[hash]
		if !ok {
			nm = &v1.NearbyMesh{Hash: hash, Name: h.rec.GetMeshName(), Members: h.rec.GetMeshMembers(), Tls: h.rec.GetMeshTls()}
			byHash[hash] = nm
		}
		if nm.GetMembers() < h.rec.GetMeshMembers() {
			nm.Members = h.rec.GetMeshMembers()
		}
		if nm.GetHeardAt() == nil || h.rec.GetHeardAt().AsTime().After(nm.GetHeardAt().AsTime()) {
			nm.HeardAt = h.rec.GetHeardAt()
		}
		nm.Nodes = append(nm.Nodes, proto.Clone(h.rec).(*v1.NearbyNode))
	}
	out := make([]*v1.NearbyMesh, 0, len(byHash))
	for _, nm := range byHash {
		sort.Slice(nm.Nodes, func(i, j int) bool { return nm.Nodes[i].GetName() < nm.Nodes[j].GetName() })
		out = append(out, nm)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetName() != out[j].GetName() {
			return out[i].GetName() < out[j].GetName()
		}
		return out[i].GetHash() < out[j].GetHash()
	})
	return out
}

// The member of a mesh heard most recently, nil when none was
func (m *Manager) freshestMemberLocked(meshHash string) *v1.NearbyNode {
	var best *heard
	for _, h := range m.nearby {
		if h.rec.GetMeshHash() != meshHash || h.rec.GetAddress() == "" {
			continue
		}
		if best == nil || h.at.After(best.at) {
			best = h
		}
	}
	if best == nil {
		return nil
	}
	return proto.Clone(best.rec).(*v1.NearbyNode)
}
