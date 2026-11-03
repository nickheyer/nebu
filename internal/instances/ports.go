package instances

import (
	"fmt"

	"github.com/nickheyer/nebu/pkg/proc"
)

// A span of ports a launch holds from reservation until its runtime binds them
type reservation struct {
	host string
	port int
}

// The reservation book of one host, opened on first use
func (m *Manager) book(host string) *proc.Reservations {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.books == nil {
		m.books = map[string]*proc.Reservations{}
	}
	r, ok := m.books[host]
	if !ok {
		r = proc.NewReservations(host)
		m.books[host] = r
	}
	return r
}

// The mesh port range when one is set, the ephemeral range otherwise
func (m *Manager) portRange() (proc.Range, error) {
	if m.MeshPorts != nil {
		if from, to, ok := m.MeshPorts(); ok {
			return proc.Range{Low: from, High: to}, nil
		}
	}
	return ephemeralRange()
}

// Reserves span consecutive ports on host for the instance and returns the first
func (m *Manager) reserve(in *instance, host string, span int) (int, error) {
	rng, err := m.portRange()
	if err != nil {
		return 0, err
	}
	port, err := m.book(host).Reserve(rng, max(span, 1))
	if err != nil {
		return 0, fmt.Errorf("reserve %d ports on %s: %w", max(span, 1), host, err)
	}
	in.mu.Lock()
	in.held = append(in.held, reservation{host: host, port: port})
	in.mu.Unlock()
	return port, nil
}

// Unbinds every span the instance holds so the process about to launch may bind them
func (m *Manager) yield(in *instance) {
	in.mu.Lock()
	held := append([]reservation(nil), in.held...)
	in.mu.Unlock()
	for _, r := range held {
		if err := m.book(r.host).Yield(r.port); err != nil {
			m.Log.Warn("port reservation yield failed", "instance", in.id(), "host", r.host, "port", r.port, "err", err)
		}
	}
}

// Hands every span the instance holds to its process once the process answered on them
func (m *Manager) confirm(in *instance) {
	for _, r := range in.takeHeld() {
		if err := m.book(r.host).Confirm(r.port); err != nil {
			m.Log.Warn("port reservation confirm failed", "instance", in.id(), "host", r.host, "port", r.port, "err", err)
		}
	}
}

// Gives back every span the instance holds so a later launch may take it
func (m *Manager) release(in *instance) {
	for _, r := range in.takeHeld() {
		m.book(r.host).Release(r.port)
	}
}

// Empties the instance's held spans and returns them
func (in *instance) takeHeld() []reservation {
	in.mu.Lock()
	defer in.mu.Unlock()
	held := in.held
	in.held = nil
	return held
}
