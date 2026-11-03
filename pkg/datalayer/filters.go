package datalayer

import (
	"slices"
	"time"
)

// Keeps endpoints every predicate keeps
func All(preds ...func(Endpoint) bool) func(Endpoint) bool {
	return func(ep Endpoint) bool {
		for _, p := range preds {
			if !p(ep) {
				return false
			}
		}
		return true
	}
}

// Keeps endpoints whose metrics were scraped no longer than threshold before now
func FreshMetrics(now time.Time, threshold time.Duration) func(Endpoint) bool {
	return func(ep Endpoint) bool {
		m, ok := Attribute[*ServerMetrics](ep, CollectorMetrics)
		return ok && !m.Stale(now, threshold)
	}
}

// Keeps endpoints whose catalog lists the adapter, whose metrics show it running, or whose adapter label names it
func HasAdapter(name string) func(Endpoint) bool {
	return func(ep Endpoint) bool {
		if name == "" {
			return false
		}
		if ep.Labels()[LabelAdapter] == name {
			return true
		}
		if m, ok := Attribute[*ServerMetrics](ep, CollectorMetrics); ok && m.AdapterActive(name) {
			return true
		}
		c, ok := Attribute[*Catalog](ep, CollectorCatalog)
		return ok && slices.Contains(c.Adapters, name)
	}
}

// Keeps endpoints serving the role, an endpoint labeled both or carrying no role serving every role
func InRole(role string) func(Endpoint) bool {
	return func(ep Endpoint) bool {
		r := ep.Labels()[LabelRole]
		return r == role || r == RoleBoth || r == ""
	}
}

// Keeps endpoints carrying the label with that value
func WithLabel(key, value string) func(Endpoint) bool {
	return func(ep Endpoint) bool {
		v, ok := ep.Labels()[key]
		return ok && v == value
	}
}
