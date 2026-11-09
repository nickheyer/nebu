package flowcontrol

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Window a fairness id's usage is metered over when its quota names none
const DefaultQuotaWindow = time.Hour

// Admissions between sweeps of fairness ids whose windows ended
const sweepEvery = 1024

// Usage of one fairness id since its window started
type usageWindow struct {
	start time.Time
	usage Usage
}

// Metered usage per fairness id inside fixed windows, judged against the configured quotas
type meter struct {
	def Quota
	per map[string]Quota

	mu         sync.Mutex
	ids        map[string]*usageWindow
	admissions uint64
}

// Builds a meter, refusing negative limits and a limited quota without a window
func newMeter(cfg Config) (*meter, error) {
	if err := checkQuota("default", cfg.Quota); err != nil {
		return nil, err
	}
	per := make(map[string]Quota, len(cfg.Quotas))
	for id, q := range cfg.Quotas {
		if err := checkQuota(id, q); err != nil {
			return nil, err
		}
		per[strings.ToLower(id)] = q
	}
	return &meter{def: cfg.Quota, per: per, ids: map[string]*usageWindow{}}, nil
}

func checkQuota(id string, q Quota) error {
	switch {
	case q.Tokens < 0 || q.Requests < 0 || q.Window < 0:
		return fmt.Errorf("flowcontrol: quota %s has a negative limit %+v", id, q)
	case (q.Tokens > 0 || q.Requests > 0) && q.Window <= 0:
		return fmt.Errorf("flowcontrol: quota %s limits usage without a window", id)
	}
	return nil
}

// The quota of a fairness id, its own or the default
func (m *meter) quota(id string) Quota {
	if q, ok := m.per[strings.ToLower(id)]; ok {
		return q
	}
	return m.def
}

func window(q Quota) time.Duration {
	if q.Window > 0 {
		return q.Window
	}
	return DefaultQuotaWindow
}

// The id's window, opened or reset when the last one ended
func (m *meter) current(id string, q Quota, now time.Time) *usageWindow {
	w := m.ids[id]
	if w == nil {
		w = &usageWindow{start: now}
		m.ids[id] = w
	}
	if now.Sub(w.start) >= window(q) {
		*w = usageWindow{start: now}
	}
	return w
}

// Refuses the id when its window's requests or tokens reached the quota
func (m *meter) admit(id string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.admissions++; m.admissions%sweepEvery == 0 {
		m.sweep(now)
	}
	q := m.quota(id)
	w := m.current(id, q, now)
	if q.Requests > 0 && w.usage.Requests >= q.Requests || q.Tokens > 0 && w.usage.PromptTokens+w.usage.OutputTokens >= q.Tokens {
		return &QuotaError{FairnessID: id, Retry: w.start.Add(window(q)).Sub(now)}
	}
	return nil
}

// Counts one dispatched request of the id
func (m *meter) count(id string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current(id, m.quota(id), now).usage.Requests++
}

// Adds the tokens a request of the id used
func (m *meter) charge(id string, prompt, output int, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.current(id, m.quota(id), now)
	w.usage.PromptTokens += int64(max(prompt, 0))
	w.usage.OutputTokens += int64(max(output, 0))
}

// The id's usage in its current window, zero once the window ended
func (m *meter) usage(id string, now time.Time) Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.ids[id]
	if w == nil || now.Sub(w.start) >= window(m.quota(id)) {
		return Usage{}
	}
	return w.usage
}

// Drops every id whose window ended
func (m *meter) sweep(now time.Time) {
	for id, w := range m.ids {
		if now.Sub(w.start) >= window(m.quota(id)) {
			delete(m.ids, id)
		}
	}
}
