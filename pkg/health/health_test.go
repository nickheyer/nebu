package health

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// A check whose result the test flips
type switchCheck struct{ failing atomic.Bool }

func (s *switchCheck) check(context.Context) error {
	if s.failing.Load() {
		return errors.New("down")
	}
	return nil
}

func getReport(t *testing.T, h http.Handler, path string) (int, Report) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("%s content type %q", path, ct)
	}
	var rep Report
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("%s body %q: %v", path, rec.Body.String(), err)
	}
	return rec.Code, rep
}

func TestHTTPReadinessFlips(t *testing.T) {
	reg := NewRegistry()
	db, sched := &switchCheck{}, &switchCheck{}
	reg.Register("datalayer", db.check)
	reg.Register("scheduler", sched.check)
	mux := http.NewServeMux()
	reg.Mount(mux)

	tests := []struct {
		name       string
		dbFail     bool
		schedFail  bool
		wantCode   int
		wantErrors map[string]string
	}{
		{"all pass", false, false, http.StatusOK, map[string]string{"datalayer": "", "scheduler": ""}},
		{"one fails", true, false, http.StatusServiceUnavailable, map[string]string{"datalayer": "down", "scheduler": ""}},
		{"both fail", true, true, http.StatusServiceUnavailable, map[string]string{"datalayer": "down", "scheduler": "down"}},
		{"recovered", false, false, http.StatusOK, map[string]string{"datalayer": "", "scheduler": ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db.failing.Store(tc.dbFail)
			sched.failing.Store(tc.schedFail)
			code, rep := getReport(t, mux, "/readyz")
			if code != tc.wantCode || rep.OK != (tc.wantCode == http.StatusOK) {
				t.Fatalf("code %d ok %v, want %d", code, rep.OK, tc.wantCode)
			}
			if len(rep.Checks) != len(tc.wantErrors) {
				t.Fatalf("checks %+v", rep.Checks)
			}
			for _, c := range rep.Checks {
				want, ok := tc.wantErrors[c.Name]
				if !ok || c.Error != want || c.OK != (want == "") {
					t.Fatalf("check %+v, want error %q", c, want)
				}
			}
			code, rep = getReport(t, mux, "/livez")
			if code != http.StatusOK || !rep.OK || len(rep.Checks) != 0 {
				t.Fatalf("livez %d %+v", code, rep)
			}
		})
	}

	reg.Unregister("datalayer")
	reg.Unregister("scheduler")
	if code, rep := getReport(t, mux, "/readyz"); code != http.StatusOK || len(rep.Checks) != 0 {
		t.Fatalf("readyz with no checks %d %+v", code, rep)
	}
}

// A store stub answering only Synced
type syncStore struct {
	datalayer.Store
	synced atomic.Bool
}

func (s *syncStore) Synced() bool { return s.synced.Load() }

func TestStoreSynced(t *testing.T) {
	st := &syncStore{}
	c := StoreSynced(st)
	if err := c(context.Background()); !errors.Is(err, ErrNotSynced) {
		t.Fatalf("unsynced err %v", err)
	}
	st.synced.Store(true)
	if err := c(context.Background()); err != nil {
		t.Fatalf("synced err %v", err)
	}
}

func dialHealth(t *testing.T, ctx context.Context, reg *Registry, cfg GRPCConfig) healthpb.HealthClient {
	t.Helper()
	lis := bufconn.Listen(1 << 16)
	srv := grpc.NewServer()
	if _, err := Serve(ctx, srv, reg, cfg); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return healthpb.NewHealthClient(conn)
}

func status(t *testing.T, c healthpb.HealthClient, service string) healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()
	resp, err := c.Check(context.Background(), &healthpb.HealthCheckRequest{Service: service})
	if err != nil {
		t.Fatalf("check %s: %v", service, err)
	}
	return resp.GetStatus()
}

func waitStatus(t *testing.T, c healthpb.HealthClient, service string, want healthpb.HealthCheckResponse_ServingStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := status(t, c, service)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s = %v, want %v", service, got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestGRPCFlipsWithChecks(t *testing.T) {
	const (
		serving    = healthpb.HealthCheckResponse_SERVING
		notServing = healthpb.HealthCheckResponse_NOT_SERVING
	)
	reg := NewRegistry()
	db, sched := &switchCheck{}, &switchCheck{}
	db.failing.Store(true)
	reg.Register("datalayer", db.check)
	reg.Register("scheduler", sched.check)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := dialHealth(t, ctx, reg, GRPCConfig{Subsystems: []string{"datalayer", "scheduler", "flowcontrol"}, Interval: 10 * time.Millisecond})

	steps := []struct {
		name      string
		dbFail    bool
		schedFail bool
		readiness healthpb.HealthCheckResponse_ServingStatus
		datalayer healthpb.HealthCheckResponse_ServingStatus
		scheduler healthpb.HealthCheckResponse_ServingStatus
	}{
		{"datalayer failing", true, false, notServing, notServing, serving},
		{"all passing", false, false, serving, serving, serving},
		{"scheduler failing", false, true, notServing, serving, notServing},
		{"recovered", false, false, serving, serving, serving},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			db.failing.Store(s.dbFail)
			sched.failing.Store(s.schedFail)
			waitStatus(t, c, ServiceReadiness, s.readiness)
			waitStatus(t, c, "datalayer", s.datalayer)
			waitStatus(t, c, "scheduler", s.scheduler)
			if got := status(t, c, ServiceLiveness); got != serving {
				t.Fatalf("liveness %v", got)
			}
			if got := status(t, c, "flowcontrol"); got != notServing {
				t.Fatalf("subsystem without a check %v", got)
			}
		})
	}

	cancel()
	waitStatus(t, c, ServiceLiveness, notServing)
	waitStatus(t, c, ServiceReadiness, notServing)
}

func TestNewGRPCRejectsBadConfig(t *testing.T) {
	reg := NewRegistry()
	for _, cfg := range []GRPCConfig{
		{Interval: 0},
		{Interval: time.Second, Subsystems: []string{ServiceReadiness}},
		{Interval: time.Second, Subsystems: []string{"a", "a"}},
		{Interval: time.Second, Subsystems: []string{""}},
	} {
		if _, err := NewGRPC(reg, cfg); err == nil {
			t.Fatalf("config %+v accepted", cfg)
		}
	}
}
