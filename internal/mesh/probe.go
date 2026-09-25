package mesh

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/mesh/links"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// Measures links to members whose measurement is older than the interval
func (m *Manager) probeLoop(ctx context.Context) {
	// Sessions come first, so the first sync round settles before the first probe.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * SyncInterval):
	}
	ticker := time.NewTicker(SyncInterval)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		var due []string
		for id, mb := range m.members {
			if mb.rec.GetState() != v1.NodeState_NODE_STATE_READY || mb.rec.GetAddress() == "" {
				continue
			}
			if time.Since(mb.probedAt) >= ProbeInterval {
				mb.probedAt = time.Now()
				due = append(due, id)
			}
		}
		m.mu.Unlock()
		for _, id := range due {
			if _, err := m.probeOne(ctx, id, false); err != nil && ctx.Err() == nil {
				m.Log.Warn("link probe failed", "node", id, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Measures the link to one member, or to every member when the id is empty. Force runs the
// bandwidth measurements even while a formation is serving.
func (m *Manager) Probe(ctx context.Context, nodeID string, force bool) ([]*v1.Link, error) {
	if !m.Joined() {
		return nil, ErrNoMesh
	}
	var ids []string
	if nodeID != "" {
		rec, err := m.Node(nodeID)
		if err != nil {
			return nil, err
		}
		if rec.GetId() == m.identity.ID {
			return nil, fmt.Errorf("%w: a node has no link to itself", ErrMesh)
		}
		ids = []string{rec.GetId()}
	} else {
		m.mu.Lock()
		for id := range m.members {
			ids = append(ids, id)
		}
		m.mu.Unlock()
		sort.Strings(ids)
	}
	var out []*v1.Link
	var errs []error
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			l, err := m.probeOne(ctx, id, force)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", id, err))
				return
			}
			out = append(out, l)
		}(id)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].GetTo() < out[j].GetTo() })
	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	for _, err := range errs {
		m.Log.Warn("link probe failed", "err", err)
	}
	return out, nil
}

// Measures one link and records it
func (m *Manager) probeOne(ctx context.Context, id string, force bool) (*v1.Link, error) {
	cl, err := m.Client(ctx, id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	mb := m.members[id]
	if mb == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w %q", ErrUnknownNode, id)
	}
	mb.probedAt = time.Now()
	last := m.links[id]
	peerRDMA := peerRDMALocked(mb.rec, m.identity.ID)
	m.mu.Unlock()
	hold := false
	if !force && m.Formations != nil {
		hold = m.Formations.Serving(m.identity.ID) || m.Formations.Serving(id)
	}
	if hold && last == nil {
		hold = false
	}
	link, err := m.prober.Measure(ctx, m.identity.ID, links.Target{ID: id, Address: cl.Address, TLS: cl.TLS, Header: cl.Header, PeerRDMA: peerRDMA}, last, hold)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.links[id] = link
	m.mu.Unlock()
	if err := m.DB.PutLink(context.Background(), link); err != nil {
		m.Log.Warn("link write failed", "to", id, "err", err)
	}
	m.Log.Info("link measured", "to", id, "class", links.ClassName(link.GetClass()), "rtt_us", link.GetRttUs(), "stream_bps", link.GetStreamBytesPerSecond(), "aggregate_bps", link.GetAggregateBytesPerSecond(), "interface", link.GetInterface(), "rdma", link.GetRdmaDevice())
	m.bump()
	return proto.Clone(link).(*v1.Link), nil
}

// The RDMA device a member reports on its end of the link to this node
func peerRDMALocked(rec *v1.Node, self string) string {
	for _, l := range rec.GetLinks() {
		if l.GetTo() == self {
			return l.GetRdmaDevice()
		}
	}
	return ""
}

// Sorts this node's link to a member again once the member's own link facts arrive
func (m *Manager) reclassifyLocked(id string) {
	link, ok := m.links[id]
	mb := m.members[id]
	if !ok || mb == nil || link.GetMeasuredAt() == nil {
		return
	}
	class, detail := links.Classify(link, peerRDMALocked(mb.rec, m.identity.ID))
	if class == link.GetClass() {
		return
	}
	link.Class, link.Detail = class, detail
	row := proto.Clone(link).(*v1.Link)
	go func() {
		if err := m.DB.PutLink(context.Background(), row); err != nil {
			m.Log.Warn("link write failed", "to", id, "err", err)
		}
		m.bump()
	}()
}

// The link measured from this node to a member, nil when unmeasured
func (m *Manager) Link(to string) *v1.Link {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.links[to]; ok {
		return proto.Clone(l).(*v1.Link)
	}
	return nil
}
