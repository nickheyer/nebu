package probe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

func TestCollectorThresholds(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		probes    []bool
		healthy   []bool
		failures  []int
	}{
		{"default threshold", 0, []bool{false, false, false, false}, []bool{true, true, false, false}, []int{1, 2, 3, 4}},
		{"one success heals", 3, []bool{false, false, false, true}, []bool{true, true, false, true}, []int{1, 2, 3, 0}},
		{"flapping never reaches threshold", 3, []bool{false, false, true, false, false, true}, []bool{true, true, true, true, true, true}, []int{1, 2, 0, 1, 2, 0}},
		{"threshold of one", 1, []bool{true, false, true}, []bool{true, false, true}, []int{0, 1, 0}},
		{"recovers then fails again", 2, []bool{false, false, true, false, false}, []bool{true, false, true, true, false}, []int{1, 2, 0, 1, 2}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := datalayer.NewStore()
			store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p", Ready: true})
			c := NewCollector(tc.threshold)
			for i, ok := range tc.probes {
				ep, _ := store.Endpoint("e")
				v, err := c.Extract(ep, &Result{OK: ok, Message: "down", CheckedAt: time.Unix(int64(i), 0)})
				if err != nil {
					t.Fatal(err)
				}
				store.SetAttribute("e", c.Name(), v)
				h := v.(*datalayer.HealthState)
				if h.Healthy != tc.healthy[i] || h.Failures != tc.failures[i] {
					t.Fatalf("probe %d: healthy=%v failures=%d, want %v %d", i, h.Healthy, h.Failures, tc.healthy[i], tc.failures[i])
				}
				ep, _ = store.Endpoint("e")
				if datalayer.ReadyAndHealthy(ep) != tc.healthy[i] {
					t.Fatalf("probe %d: ReadyAndHealthy disagrees", i)
				}
			}
		})
	}
	if _, err := NewCollector(3).Extract(nil, "x"); !errors.Is(err, ErrSampleType) {
		t.Fatalf("wrong sample err = %v", err)
	}
}

// Records every probe outcome
type recorder struct {
	mu   sync.Mutex
	errs []error
}

func (r *recorder) OnProbe(_ datalayer.EndpointID, _ time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs)
}

func health(store datalayer.Store) *datalayer.HealthState {
	ep, ok := store.Endpoint("e")
	if !ok {
		return nil
	}
	h, _ := datalayer.Attribute[*datalayer.HealthState](ep, datalayer.CollectorHealth)
	return h
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestSourceFlaps(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	addr, err := datalayer.ParseAddress(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	store := datalayer.NewStore()
	store.PutPool(datalayer.Pool{ID: "p", Metrics: datalayer.MetricCapabilities{HealthPath: "/health"}})
	store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p", Address: addr, Ready: true})
	rec := &recorder{}
	src := New(store, StoreResolver(store), Options{Interval: 5 * time.Millisecond, Observer: rec})
	if _, scoped := any(src).(datalayer.PoolScoped); scoped {
		t.Fatal("a pool without a health path escapes probing")
	}
	if err := src.Subscribe(NewCollector(3)); err != nil {
		t.Fatal(err)
	}
	if err := src.UpdateEndpoints([]datalayer.EndpointID{"e"}); err != nil {
		t.Fatal(err)
	}
	if err := src.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer src.Stop()

	eventually(t, "healthy", func() bool { h := health(store); return h != nil && h.Healthy })
	status.Store(http.StatusServiceUnavailable)
	eventually(t, "first failure", func() bool { h := health(store); return h.Failures >= 1 })
	if h := health(store); h.Failures < 3 && !h.Healthy {
		t.Fatalf("unhealthy before threshold: %+v", h)
	}
	eventually(t, "unhealthy", func() bool { h := health(store); return !h.Healthy })
	if h := health(store); h.Failures < 3 || !strings.Contains(h.Message, "503") {
		t.Fatalf("unhealthy state = %+v", h)
	}
	status.Store(http.StatusOK)
	eventually(t, "healed", func() bool { h := health(store); return h.Healthy && h.Failures == 0 })
	if rec.count() == 0 {
		t.Fatal("observer never told")
	}

	if err := src.UpdateEndpoints(nil); err != nil {
		t.Fatal(err)
	}
	if health(store) != nil {
		t.Fatal("health attribute survived untracking")
	}
}

func TestUnresolvableCountsAsFailure(t *testing.T) {
	store := datalayer.NewStore()
	store.PutPool(datalayer.Pool{ID: "p"})
	store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p", Ready: true})
	rec := &recorder{}
	src := New(store, StoreResolver(store), Options{Interval: 5 * time.Millisecond, Observer: rec})
	if err := src.Subscribe(NewCollector(2)); err != nil {
		t.Fatal(err)
	}
	if err := src.UpdateEndpoints([]datalayer.EndpointID{"e"}); err != nil {
		t.Fatal(err)
	}
	if err := src.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer src.Stop()
	eventually(t, "unhealthy", func() bool { h := health(store); return h != nil && !h.Healthy })
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !errors.Is(rec.errs[0], ErrUnresolved) {
		t.Fatalf("err = %v", rec.errs[0])
	}
}

func TestTimeoutEqualsInterval(t *testing.T) {
	src := New(datalayer.NewStore(), func(datalayer.EndpointID) (string, bool) { return "", false }, Options{})
	if src.client.Timeout != DefaultInterval {
		t.Fatalf("timeout %v", src.client.Timeout)
	}
}

// An endpoint whose pool names no health path fails its probes and turns unhealthy at the threshold
func TestNoHealthPathTurnsUnhealthy(t *testing.T) {
	store := datalayer.NewStore()
	store.PutPool(datalayer.Pool{ID: "p"})
	store.UpsertEndpoint(datalayer.EndpointSpec{ID: "e", Pool: "p", Address: datalayer.Address{Host: "127.0.0.1", Port: 1}, Ready: true})
	src := New(store, StoreResolver(store), Options{Interval: 5 * time.Millisecond})
	if err := src.Subscribe(NewCollector(2)); err != nil {
		t.Fatal(err)
	}
	if err := src.UpdateEndpoints([]datalayer.EndpointID{"e"}); err != nil {
		t.Fatal(err)
	}
	if err := src.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer src.Stop()
	eventually(t, "unhealthy", func() bool { h := health(store); return h != nil && !h.Healthy && h.Failures >= 2 })
	ep, _ := store.Endpoint("e")
	if datalayer.ReadyAndHealthy(ep) {
		t.Fatal("an unprobeable endpoint stayed selectable")
	}
	if !strings.Contains(health(store).Message, ErrUnresolved.Error()) {
		t.Fatalf("message %q", health(store).Message)
	}
}
