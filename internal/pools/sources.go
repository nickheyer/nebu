package pools

import (
	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/datalayer/kvevents"
)

// The KV event publisher of an endpoint on this node, from its labels, and its pool's model
func (m *Manager) kvEventsTarget(id datalayer.EndpointID) (kvevents.Target, bool) {
	ep, ok := m.store.Endpoint(id)
	if !ok {
		return kvevents.Target{}, false
	}
	labels := ep.Labels()
	events := labels[datalayer.LabelKVEvents]
	if events == "" || (labels[datalayer.LabelNode] != "" && labels[datalayer.LabelNode] != m.nodeID) {
		return kvevents.Target{}, false
	}
	p, ok := m.store.Pool(ep.Pool())
	if !ok {
		return kvevents.Target{}, false
	}
	return kvevents.Target{Events: events, Replay: labels[datalayer.LabelKVReplay], Model: p.Model}, true
}

// The metric names of an endpoint's pool and the address serving them, the endpoint's own metrics port first
func (m *Manager) metricsTarget(id datalayer.EndpointID) (datalayer.MetricCapabilities, datalayer.Address, bool) {
	ep, ok := m.store.Endpoint(id)
	if !ok {
		return datalayer.MetricCapabilities{}, datalayer.Address{}, false
	}
	p, ok := m.store.Pool(ep.Pool())
	if !ok || !p.Metrics.Complete() || p.Metrics.Path == "" {
		return datalayer.MetricCapabilities{}, datalayer.Address{}, false
	}
	addr := ep.Address()
	switch port := (*m.ports.Load())[id].metrics; {
	case port != 0:
		addr.Port = port
	case p.Metrics.MetricsPort != 0:
		addr.Port = p.Metrics.MetricsPort
	}
	return p.Metrics, addr, true
}

// The health URL of an endpoint, its pool's health path on the endpoint's health port
func (m *Manager) healthURL(id datalayer.EndpointID) (string, bool) {
	ep, ok := m.store.Endpoint(id)
	if !ok {
		return "", false
	}
	p, ok := m.store.Pool(ep.Pool())
	if !ok || p.Metrics.HealthPath == "" {
		return "", false
	}
	addr := ep.Address()
	if port := (*m.ports.Load())[id].health; port != 0 {
		addr.Port = port
	}
	return "http://" + addr.String() + p.Metrics.HealthPath, true
}
