// Package health answers liveness and readiness over HTTP and the gRPC health checking protocol
package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// Returned by the store check until every pool bridge has reported once
var ErrNotSynced = errors.New("endpoint set has not synced")

// Probes one subsystem, nil when it is ready
type Check func(ctx context.Context) error

// The outcome of one named check
type CheckResult struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// The outcome of a probe and of every check it ran
type Report struct {
	OK     bool          `json:"ok"`
	Checks []CheckResult `json:"checks"`
}

// The result of a named check and whether the report holds it
func (r Report) Result(name string) (CheckResult, bool) {
	i := slices.IndexFunc(r.Checks, func(c CheckResult) bool { return c.Name == name })
	if i < 0 {
		return CheckResult{}, false
	}
	return r.Checks[i], true
}

// Named readiness checks, safe for concurrent registration and probing
type Registry struct {
	mu     sync.RWMutex
	checks map[string]Check
}

func NewRegistry() *Registry { return &Registry{checks: map[string]Check{}} }

// Adds or replaces the readiness check of a name
func (r *Registry) Register(name string, c Check) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks[name] = c
}

// Removes the readiness check of a name
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.checks, name)
}

// Passes whenever the process is able to answer
func (r *Registry) Liveness() Report { return Report{OK: true, Checks: []CheckResult{}} }

// Runs every readiness check concurrently and passes only when all of them pass
func (r *Registry) Readiness(ctx context.Context) Report {
	r.mu.RLock()
	names := make([]string, 0, len(r.checks))
	checks := make([]Check, 0, len(r.checks))
	for name := range r.checks {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		checks = append(checks, r.checks[name])
	}
	r.mu.RUnlock()

	results := make([]CheckResult, len(names))
	var wg sync.WaitGroup
	for i := range names {
		wg.Go(func() {
			results[i] = CheckResult{Name: names[i], OK: true}
			if err := checks[i](ctx); err != nil {
				results[i] = CheckResult{Name: names[i], Error: err.Error()}
			}
		})
	}
	wg.Wait()
	rep := Report{OK: true, Checks: results}
	for _, c := range results {
		rep.OK = rep.OK && c.OK
	}
	return rep
}

// Answers 200 with the liveness report
func (r *Registry) LivezHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeReport(w, r.Liveness()) })
}

// Answers 200 when ready and 503 otherwise, with the readiness report
func (r *Registry) ReadyzHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { writeReport(w, r.Readiness(req.Context())) })
}

// Mounts the liveness and readiness handlers at /livez and /readyz
func (r *Registry) Mount(mux *http.ServeMux) {
	mux.Handle("/livez", r.LivezHandler())
	mux.Handle("/readyz", r.ReadyzHandler())
}

func writeReport(w http.ResponseWriter, rep Report) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if rep.OK {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(rep)
}

// Fails until every pool bridge has reported its endpoints to the store
func StoreSynced(store datalayer.Store) Check {
	return func(context.Context) error {
		if !store.Synced() {
			return ErrNotSynced
		}
		return nil
	}
}
