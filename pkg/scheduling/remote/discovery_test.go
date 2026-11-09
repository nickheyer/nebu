package remote_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/internal/schedtest"
	"github.com/nickheyer/nebu/pkg/scheduling/remote"
)

// A configuration naming no URL resolves to the service name on the default port, a bare host takes both
func TestResolveURL(t *testing.T) {
	tests := []struct{ raw, want string }{
		{"", "https://nebu-scheduler:8486"},
		{"  ", "https://nebu-scheduler:8486"},
		{"sched.internal", "https://sched.internal:8486"},
		{"sched.internal:9000", "https://sched.internal:9000"},
		{"http://sched.internal", "http://sched.internal:8486"},
		{"https://10.0.0.5:8486/", "https://10.0.0.5:8486/"},
	}
	for _, tc := range tests {
		if got := remote.ResolveURL(tc.raw); got != tc.want {
			t.Errorf("ResolveURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	if remote.DefaultPort != 8486 || remote.ServiceName != "nebu-scheduler" || remote.HealthPath != "/healthz" || remote.MetricsPath != "/metrics" {
		t.Fatal("the discovery constants moved")
	}
}

// The server answers liveness on the health path and counts schedule calls on the metrics path
func TestHealthAndMetricsPaths(t *testing.T) {
	eps := endpoints()
	srv := server(t, local(t), eps)
	resp, err := http.Get(srv.URL + remote.HealthPath)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz %v %v", resp, err)
	}
	resp.Body.Close()
	c := client(t, srv.URL, time.Second)
	if _, err := c.Schedule(ctx, &framework.Request{ID: "r1"}, eps); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Schedule(ctx, &framework.Request{ID: "r2"}, schedtest.Eps(schedtest.EP("zz").At("10.0.0.9", 1))); err == nil {
		t.Fatal("an unknown candidate is refused")
	}
	resp, err = http.Get(srv.URL + remote.MetricsPath)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics %v %v", resp, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{`nebu_scheduler_schedule_total{outcome="scheduled"} 1`, `nebu_scheduler_schedule_total{outcome="unknown-endpoint"} 1`, "nebu_scheduler_schedule_duration_seconds_count 2"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("metrics lack %q:\n%s", want, body)
		}
	}
}

// A candidate carrying the id its exporter gave it is named by that id on the wire and mapped back to the caller's value
func TestExportedIDCrossesTheWire(t *testing.T) {
	eps := endpoints()
	srv := server(t, local(t), eps)
	c := client(t, srv.URL, time.Second)
	imported := schedtest.Eps(
		schedtest.EP("i1/5001").At("203.0.113.1", 5001).Label(remote.LabelExportedID, "a").Metrics(datalayer.ServerMetrics{QueuedRequests: 9}),
		schedtest.EP("i1/5002").At("203.0.113.1", 5002).Label(remote.LabelExportedID, "b"),
	)
	res, err := c.Schedule(ctx, &framework.Request{ID: "r1"}, imported)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Endpoints()
	if !slices.Equal(schedtest.IDs(got), []string{"i1/5002", "i1/5001"}) {
		t.Fatalf("picked %v", schedtest.IDs(got))
	}
	if _, ok := res.ProfileResults["main"].Scores["i1/5002"]; !ok {
		t.Fatalf("scores are keyed by the caller's ids: %v", res.ProfileResults["main"].Scores)
	}
}

// A pinned transport trusts the certificate the file holds and no other self signed one
func TestPinnedTransport(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	own := filepath.Join(dir, "tls.crt")
	if err := os.WriteFile(own, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}}, &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	stranger := filepath.Join(dir, "other.crt")
	if err := os.WriteFile(stranger, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: otherDER}), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		file string
		ok   bool
	}{
		{"own certificate", own, true},
		{"another certificate", stranger, false},
		{"no pin", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := remote.PinnedTransport(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := (&http.Client{Transport: tr}).Get(srv.URL)
			if (err == nil) != tc.ok {
				t.Fatalf("ok %v err %v", tc.ok, err)
			}
			if err == nil {
				resp.Body.Close()
			}
		})
	}
	if _, err := remote.PinnedTransport(filepath.Join(dir, "missing.crt")); err == nil {
		t.Fatal("a missing pin file is an error")
	}
	if err := os.WriteFile(stranger, []byte("not pem"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.PinnedTransport(stranger); err == nil {
		t.Fatal("a file without a PEM block is an error")
	}
	if _, err := x509.ParseCertificate(srv.Certificate().Raw); err != nil {
		t.Fatal(err)
	}
}
