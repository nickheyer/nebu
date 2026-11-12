package pools

import (
	"context"
	"slices"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// The record an event names
type eventKey struct {
	kind v1.EventKind
	id   string
}

// When a record's last event went out and whether a newer state waits for its window to end
type pendingEvent struct {
	last  time.Time
	timer *time.Timer
	dirty bool
}

// Publishes an event now and restarts the record's coalescing window
func (m *Manager) emitLocked(kind v1.EventKind, action v1.EventAction, id string, record proto.Message) {
	m.bus.Publish(kind, action, id, record)
	key := eventKey{kind: kind, id: id}
	if action == v1.EventAction_EVENT_ACTION_DELETED {
		if p := m.pending[key]; p != nil {
			p.dirty = false
			if p.timer != nil {
				p.timer.Stop()
			}
			delete(m.pending, key)
		}
		return
	}
	p := m.pendingFor(key)
	p.last, p.dirty = time.Now(), false
}

// Publishes the record's update now when its window passed, else once it ends, at most one per window
func (m *Manager) coalesceLocked(key eventKey) {
	if m.closed {
		return
	}
	p := m.pendingFor(key)
	p.dirty = true
	if p.timer != nil {
		return
	}
	wait := coalesceWindow - time.Since(p.last)
	if wait <= 0 {
		m.flushLocked(key, p)
		return
	}
	p.timer = time.AfterFunc(wait, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		p.timer = nil
		if !m.closed {
			m.flushLocked(key, p)
		}
	})
}

func (m *Manager) pendingFor(key eventKey) *pendingEvent {
	p := m.pending[key]
	if p == nil {
		p = &pendingEvent{}
		m.pending[key] = p
	}
	return p
}

// Publishes the record's current state when a newer one waits
func (m *Manager) flushLocked(key eventKey, p *pendingEvent) {
	if !p.dirty {
		return
	}
	p.dirty = false
	var record proto.Message
	switch key.kind {
	case v1.EventKind_EVENT_KIND_POOL:
		if ps := m.st.pools[datalayer.PoolID(key.id)]; ps != nil {
			record = m.renderPoolLocked(ps)
		}
	case v1.EventKind_EVENT_KIND_ROUTE:
		if rs := m.st.routes[key.id]; rs != nil {
			record = rs.route
		}
	}
	if record == nil {
		return
	}
	m.bus.Publish(key.kind, v1.EventAction_EVENT_ACTION_UPDATED, key.id, record)
	p.last = time.Now()
}

// Follows store changes, refreshing a pool when an endpoint's health flips and the served names when a catalog changes
func (m *Manager) watch(changes <-chan datalayer.Change) {
	healthy := map[datalayer.EndpointID]bool{}
	catalogs := map[datalayer.EndpointID]*datalayer.Catalog{}
	for ch := range changes {
		switch {
		case ch.Kind == datalayer.ChangeEndpointDeleted:
			delete(healthy, ch.Endpoint)
			delete(catalogs, ch.Endpoint)
		case ch.Kind != datalayer.ChangeAttribute:
		case ch.Collector == datalayer.CollectorHealth:
			ep, ok := m.store.Endpoint(ch.Endpoint)
			if !ok {
				continue
			}
			now := true
			if h, ok := datalayer.Attribute[*datalayer.HealthState](ep, datalayer.CollectorHealth); ok {
				now = h.Healthy
			}
			prev, seen := healthy[ch.Endpoint]
			healthy[ch.Endpoint] = now
			if (seen && prev != now) || (!seen && !now) {
				m.healthChanged(ep.Pool())
			}
		case ch.Collector == datalayer.CollectorScrapeFailure:
			if ep, ok := m.store.Endpoint(ch.Endpoint); ok {
				m.healthChanged(ep.Pool())
			}
		case ch.Collector == datalayer.CollectorCatalog:
			ep, ok := m.store.Endpoint(ch.Endpoint)
			if !ok {
				continue
			}
			c, _ := datalayer.Attribute[*datalayer.Catalog](ep, datalayer.CollectorCatalog)
			if prev := catalogs[ch.Endpoint]; sameCatalog(prev, c) {
				continue
			}
			catalogs[ch.Endpoint] = c
			m.catalogChanged()
		}
	}
}

// Recounts a pool whose endpoint health or scrape failure changed and the routes reading it
func (m *Manager) healthChanged(id datalayer.PoolID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ps := m.st.pools[id]
	if ps == nil || m.closed {
		return
	}
	if m.refreshPoolLocked(ps) {
		m.coalesceLocked(eventKey{kind: v1.EventKind_EVENT_KIND_POOL, id: string(id)})
	}
	m.refreshRoutesLocked(context.Background())
	m.publishViewLocked()
}

// Rebuilds the served names and the route statuses judged by them after an endpoint's catalog changed
func (m *Manager) catalogChanged() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.refreshRoutesLocked(context.Background())
		m.publishViewLocked()
	}
}

// Whether two catalogs list the same models and adapters
func sameCatalog(a, b *datalayer.Catalog) bool {
	if a == nil || b == nil {
		return a == b
	}
	return slices.Equal(a.Models, b.Models) && slices.Equal(a.Adapters, b.Adapters)
}
