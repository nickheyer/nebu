package mesh

import (
	"context"
	"crypto/sha256"
	"runtime"
	"slices"
	"sort"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Every part of a node record, in the order a snapshot lists them
var partOrder = []v1.RecordPart{
	v1.RecordPart_RECORD_PART_IDENTITY,
	v1.RecordPart_RECORD_PART_PROFILE,
	v1.RecordPart_RECORD_PART_INSTALLS,
	v1.RecordPart_RECORD_PART_STORED,
	v1.RecordPart_RECORD_PART_CAPABILITIES,
	v1.RecordPart_RECORD_PART_THROUGHPUT,
	v1.RecordPart_RECORD_PART_LINKS,
	v1.RecordPart_RECORD_PART_ROUTES,
	v1.RecordPart_RECORD_PART_OBJECTIVES,
	v1.RecordPart_RECORD_PART_POOLS,
	v1.RecordPart_RECORD_PART_SEATS,
	v1.RecordPart_RECORD_PART_SLOTS,
	v1.RecordPart_RECORD_PART_FORMATIONS,
	v1.RecordPart_RECORD_PART_ADMISSIONS,
}

// Whether changed parts call for an immediate push, learned throughput riding the next round instead
func pushes(changed []v1.RecordPart) bool {
	for _, p := range changed {
		if p != v1.RecordPart_RECORD_PART_THROUGHPUT {
			return true
		}
	}
	return false
}

// Copies one part's fields from one record onto another
func copyPart(dst, src *v1.Node, p v1.RecordPart) {
	switch p {
	case v1.RecordPart_RECORD_PART_IDENTITY:
		dst.Name, dst.Addresses, dst.Version, dst.Os, dst.Arch = src.GetName(), src.GetAddresses(), src.GetVersion(), src.GetOs(), src.GetArch()
		dst.Address, dst.Certificate, dst.Issuer = src.GetAddress(), src.GetCertificate(), src.GetIssuer()
	case v1.RecordPart_RECORD_PART_PROFILE:
		dst.Profile = src.GetProfile()
	case v1.RecordPart_RECORD_PART_INSTALLS:
		dst.Installs = src.GetInstalls()
	case v1.RecordPart_RECORD_PART_STORED:
		dst.Stored = src.GetStored()
	case v1.RecordPart_RECORD_PART_CAPABILITIES:
		dst.Capabilities = src.GetCapabilities()
	case v1.RecordPart_RECORD_PART_THROUGHPUT:
		dst.Throughput = src.GetThroughput()
	case v1.RecordPart_RECORD_PART_LINKS:
		dst.Links = src.GetLinks()
	case v1.RecordPart_RECORD_PART_ROUTES:
		dst.Routes = src.GetRoutes()
	case v1.RecordPart_RECORD_PART_OBJECTIVES:
		dst.Objectives = src.GetObjectives()
	case v1.RecordPart_RECORD_PART_POOLS:
		dst.Pools = src.GetPools()
	case v1.RecordPart_RECORD_PART_SEATS:
		dst.Seats = src.GetSeats()
	case v1.RecordPart_RECORD_PART_SLOTS:
		dst.Slots = src.GetSlots()
	case v1.RecordPart_RECORD_PART_FORMATIONS:
		dst.FormationIds = src.GetFormationIds()
	}
}

// The sequence each part of a record last changed at, as the record lists them
func partSeqs(rec *v1.Node) map[v1.RecordPart]uint64 {
	out := make(map[v1.RecordPart]uint64, len(rec.GetParts()))
	for _, p := range rec.GetParts() {
		out[p.GetPart()] = p.GetSequence()
	}
	return out
}

// The parts list of a record from sequences, in snapshot order
func partList(seqs map[v1.RecordPart]uint64) []*v1.NodePart {
	out := make([]*v1.NodePart, 0, len(seqs))
	for _, p := range partOrder {
		if seq, ok := seqs[p]; ok {
			out = append(out, &v1.NodePart{Part: p, Sequence: seq})
		}
	}
	return out
}

// The digest of one part's content, formations and admissions included for their parts
func partHash(rec *v1.Node, p v1.RecordPart, formations []*v1.Formation, admissions []*v1.Admission) [32]byte {
	var msg proto.Message
	switch p {
	case v1.RecordPart_RECORD_PART_FORMATIONS:
		bare := &v1.SyncRequest{Node: &v1.Node{FormationIds: rec.GetFormationIds()}}
		for _, f := range formations {
			c := proto.Clone(f).(*v1.Formation)
			// Bookkeeping that moves with every write is carried but never counts as a change
			c.Sequence, c.UpdatedAt = 0, nil
			bare.Formations = append(bare.Formations, c)
		}
		msg = bare
	case v1.RecordPart_RECORD_PART_ADMISSIONS:
		msg = &v1.SyncRequest{Admissions: admissions}
	default:
		one := &v1.Node{}
		copyPart(one, rec, p)
		msg = one
	}
	raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	return sha256.Sum256(raw)
}

// Rebuilds this node's record, raising the sequence of every part whose content changed
func (m *Manager) refresh() {
	m.building.Lock()
	defer m.building.Unlock()
	m.mu.Lock()
	m.dirty = false
	m.mu.Unlock()
	rec, formations, shared := m.gather(m.addresses())
	hashes := make(map[v1.RecordPart][32]byte, len(partOrder))
	for _, p := range partOrder {
		hashes[p] = partHash(rec, p, formations, shared)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.seq + 1
	var changed []v1.RecordPart
	for _, p := range partOrder {
		if st := m.parts[p]; st == nil || st.hash != hashes[p] {
			m.parts[p] = &partState{seq: next, hash: hashes[p]}
			changed = append(changed, p)
		}
	}
	if len(changed) > 0 {
		m.seq = next
	}
	m.unpushed = m.unpushed || pushes(changed)
	m.record, m.formations, m.shared = rec, formations, shared
}

// Gathers the record from every part of the daemon it describes, with the formations and admissions it shares
func (m *Manager) gather(addresses []string) (*v1.Node, []*v1.Formation, []*v1.Admission) {
	m.mu.Lock()
	links := make([]*v1.Link, 0, len(m.links))
	for _, l := range m.links {
		links = append(links, proto.Clone(l).(*v1.Link))
	}
	address := m.advertiseLocked()
	var certificate []byte
	issuer := false
	if m.creds != nil {
		certificate, issuer = m.creds.leaf.Raw, m.creds.caKey != nil
	}
	shared := m.admissionsForSyncLocked()
	prev := m.record
	m.mu.Unlock()
	sort.Slice(links, func(i, j int) bool { return links[i].GetTo() < links[j].GetTo() })
	sort.Slice(shared, func(i, j int) bool { return shared[i].GetId() < shared[j].GetId() })
	rec := &v1.Node{
		Id:          m.identity.ID,
		Name:        m.name(),
		Version:     m.Version,
		Links:       links,
		Os:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Address:     address,
		Addresses:   addresses,
		Certificate: certificate,
		Issuer:      issuer,
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	// A source that fails keeps the parts it fills as last built, so a passing error never empties them
	keep := func(what string, err error, parts ...v1.RecordPart) {
		m.Log.Warn("mesh record part kept as last built", "part", what, "err", err)
		for _, p := range parts {
			copyPart(rec, prev, p)
		}
	}
	if profile, err := m.Host.Profile(ctx, false); err == nil {
		rec.Profile = profile
	} else {
		keep("profile", err, v1.RecordPart_RECORD_PART_PROFILE)
	}
	if list, err := m.Installs.List(ctx, ""); err == nil {
		rec.Installs = list
		for _, in := range list {
			// An install of a runtime this build does not know carries no shapes to plan with
			rt, err := m.Runtimes.Get(in.GetRuntimeId())
			if err != nil {
				continue
			}
			rec.Capabilities = append(rec.Capabilities, &v1.Capability{RuntimeId: rt.ID(), InstallId: in.GetId(), Version: in.GetVersion(), Shapes: rt.Shapes(in)})
		}
	} else {
		keep("installs", err, v1.RecordPart_RECORD_PART_INSTALLS, v1.RecordPart_RECORD_PART_CAPABILITIES)
	}
	if manifests, err := m.Store.ListManifests(); err == nil {
		for _, s := range manifests {
			rec.Stored = append(rec.Stored, Summary(s))
		}
	} else {
		keep("stored", err, v1.RecordPart_RECORD_PART_STORED)
	}
	if m.Perf != nil {
		rec.Throughput = m.Perf.Throughputs()
	}
	rec.Pools = m.exportedPools(m.Pools.Exported())
	rec.Routes = sharedRoutes(m.Pools.Routes())
	rec.Objectives = namedObjectives(m.Pools, rec.Routes)
	var formations []*v1.Formation
	if m.Formations != nil {
		for _, f := range m.Formations.Conducted() {
			rec.FormationIds = append(rec.FormationIds, f.GetId())
			formations = append(formations, shareFormation(f))
		}
		rec.Seats = m.Formations.Seats()
		rec.Slots = m.Formations.Slots()
	}
	return rec, formations, shared
}

// A formation as the sync carries it, the byte counters its seats measure left out
func shareFormation(f *v1.Formation) *v1.Formation {
	out := proto.Clone(f).(*v1.Formation)
	out.Measurements = nil
	for _, s := range out.GetSeats() {
		s.Measurements = nil
	}
	if out.GetPlan() != nil {
		for _, s := range out.GetPlan().GetSeats() {
			s.Measurements = nil
		}
	}
	return out
}

// This node's record as every other member sees it, rebuilt once something in it may have changed
func (m *Manager) Record() *v1.Node {
	m.mu.Lock()
	stale := m.record == nil || m.dirty
	m.mu.Unlock()
	if !stale {
		addresses := m.addresses()
		m.mu.Lock()
		stale = !slices.Equal(m.record.GetAddresses(), addresses)
		m.mu.Unlock()
	}
	if stale {
		m.refresh()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := proto.Clone(m.record).(*v1.Node)
	out.Sequence, out.Parts = m.seq, m.partsLocked()
	out.State, out.Self, out.SeenAt = v1.NodeState_NODE_STATE_READY, true, timestamppb.Now()
	out.Conditions = m.selfConditionsLocked()
	return out
}

// This node's parts list with each part's sequence
func (m *Manager) partsLocked() []*v1.NodePart {
	seqs := make(map[v1.RecordPart]uint64, len(m.parts))
	for p, st := range m.parts {
		seqs[p] = st.seq
	}
	return partList(seqs)
}

// The parts changed since a base, or every part with a zero delta base when the base is unknown
func (m *Manager) outgoing(base uint64) (*v1.Node, []*v1.Formation, []*v1.Admission) {
	m.mu.Lock()
	stale := m.record == nil || m.dirty
	m.mu.Unlock()
	if stale {
		m.refresh()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := base == 0 || base < m.epoch || base > m.seq
	out := &v1.Node{Id: m.identity.ID, Sequence: m.seq, DeltaBase: base}
	if snapshot {
		out.DeltaBase = 0
	}
	var formations []*v1.Formation
	var admissions []*v1.Admission
	for _, p := range partOrder {
		st := m.parts[p]
		if st == nil || !snapshot && st.seq <= base {
			continue
		}
		out.Parts = append(out.Parts, &v1.NodePart{Part: p, Sequence: st.seq})
		copyPart(out, m.record, p)
		switch p {
		case v1.RecordPart_RECORD_PART_FORMATIONS:
			formations = m.formations
		case v1.RecordPart_RECORD_PART_ADMISSIONS:
			admissions = m.shared
		}
	}
	return out, formations, admissions
}

// Replaces each held part with a newer listed one, returning the sequence held whole and the parts changed
func (m *Manager) applyRecordLocked(mb *member, in *v1.Node) (uint64, []v1.RecordPart) {
	have := partSeqs(mb.rec)
	held := mb.held()
	var changed []v1.RecordPart
	for _, p := range in.GetParts() {
		if p.GetSequence() <= have[p.GetPart()] {
			continue
		}
		copyPart(mb.rec, in, p.GetPart())
		have[p.GetPart()] = p.GetSequence()
		changed = append(changed, p.GetPart())
	}
	mb.rec.Parts = partList(have)
	whole := in.GetDeltaBase() == 0 || in.GetDeltaBase() <= held && !mb.sketch
	if whole && in.GetSequence() > held {
		mb.rec.Sequence = in.GetSequence()
		mb.sketch = false
		held = in.GetSequence()
	}
	if in.GetSequence() > mb.advertised {
		mb.advertised = in.GetSequence()
	}
	return held, changed
}
