package daemon

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/pkg/config"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/remote"
)

func tlsDaemon(dir string, t *v1.Tls) *Daemon {
	cfg := &v1.Config{DataDir: dir, Listen: "192.0.2.7:8484", Gateway: &v1.Gateway{}, Mesh: &v1.MeshConfig{}, Tls: t}
	return &Daemon{Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// The first start with tls.enabled generates a pair in data_dir naming the listen host, later starts serve the same one
func TestListenerCertificateGeneratedAndReused(t *testing.T) {
	dir := t.TempDir()
	first, err := tlsDaemon(dir, &v1.Tls{Enabled: true}).tlsConfig()
	if err != nil || first == nil || len(first.Certificates) != 1 {
		t.Fatalf("first start %v %v", first, err)
	}
	leaf := first.Certificates[0].Leaf
	for _, h := range []string{"localhost", "127.0.0.1", "192.0.2.7"} {
		if err := leaf.VerifyHostname(h); err != nil {
			t.Fatalf("certificate does not name %s: %v", h, err)
		}
	}
	for _, name := range []string{config.CertFile, config.KeyFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s not written %v", name, err)
		}
	}
	second, err := tlsDaemon(dir, &v1.Tls{Enabled: true}).tlsConfig()
	if err != nil || auth.Fingerprint(second.Certificates[0]) != auth.Fingerprint(first.Certificates[0]) {
		t.Fatalf("restart made another certificate %v", err)
	}
	// A configured pair is served as it is
	own, err := tlsDaemon(t.TempDir(), &v1.Tls{Enabled: true, CertFile: filepath.Join(dir, config.CertFile), KeyFile: filepath.Join(dir, config.KeyFile)}).tlsConfig()
	if err != nil || auth.Fingerprint(own.Certificates[0]) != auth.Fingerprint(first.Certificates[0]) {
		t.Fatalf("configured pair %v", err)
	}
	if _, err := tlsDaemon(t.TempDir(), &v1.Tls{Enabled: true, CertFile: "/nonexistent/c.pem", KeyFile: "/nonexistent/k.pem"}).tlsConfig(); err == nil {
		t.Fatal("a missing configured pair should fail rather than be generated")
	}
}

// A daemon with no tls settings serves plaintext and writes no pair, and a certificate named without tls.enabled is refused
func TestPlaintextIsDefault(t *testing.T) {
	dir := t.TempDir()
	off, err := tlsDaemon(dir, &v1.Tls{}).tlsConfig()
	if err != nil || off != (*tls.Config)(nil) {
		t.Fatalf("default %v %v", off, err)
	}
	for _, name := range []string{config.CertFile, config.KeyFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Fatalf("%s written without tls.enabled", name)
		}
	}
	if _, err := tlsDaemon(t.TempDir(), &v1.Tls{CertFile: "c.pem"}).tlsConfig(); err == nil {
		t.Fatal("a certificate named without tls.enabled should fail")
	}
}

// Picks the first candidate, standing in for a scheduler behind the listener
type firstPicker struct{}

func (firstPicker) Schedule(_ context.Context, _ *framework.Request, candidates []datalayer.Endpoint) (*framework.Result, error) {
	return &framework.Result{PrimaryProfile: "main", ProfileResults: map[string]*framework.ProfileRunResult{"main": {Endpoints: candidates[:1]}}}, nil
}

func (firstPicker) Profiles() map[string]*framework.Profile { return map[string]*framework.Profile{} }

// Resolves no endpoint, the health and metrics paths needing none
type noEndpoints struct{}

func (noEndpoints) Endpoint(datalayer.EndpointID) (datalayer.Endpoint, bool) { return nil, false }

// The scheduler listener serves the remote handler in plaintext, and with tls.enabled under the generated certificate a client pinning the file trusts
func TestSchedulerListenerFollowsTLS(t *testing.T) {
	serveOn := func(t *testing.T, tlsCfg *v1.Tls) (*Daemon, string) {
		t.Helper()
		d := tlsDaemon(t.TempDir(), tlsCfg)
		var err error
		if d.tls, err = d.tlsConfig(); err != nil {
			t.Fatal(err)
		}
		ln, err := d.listen("127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := d.server(remote.NewHandler(firstPicker{}, noEndpoints{}), context.Background())
		go srv.Serve(ln)
		t.Cleanup(func() { srv.Close() })
		return d, ln.Addr().String()
	}
	d, addr := serveOn(t, &v1.Tls{Enabled: true})
	pinned, err := remote.PinnedTransport(d.Config.GetTls().GetCertFile())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: pinned}).Get("https://" + addr + remote.HealthPath)
	if err != nil {
		t.Fatalf("a client pinning the generated certificate: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz %d", resp.StatusCode)
	}
	if resp, err = (&http.Client{Transport: pinned}).Get("https://" + addr + remote.MetricsPath); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics %v %v", resp, err)
	}
	resp.Body.Close()
	// A TLS listener answers a plaintext request with 400 and never the handler
	resp, err = http.Get("http://" + addr + remote.HealthPath)
	if err != nil {
		t.Fatalf("plaintext to the scheduler listener: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("the scheduler listener answered plaintext with %d under tls.enabled", resp.StatusCode)
	}
	unpinned, _ := remote.PinnedTransport("")
	if _, err := (&http.Client{Transport: unpinned}).Get("https://" + addr + remote.HealthPath); err == nil {
		t.Fatal("a client without the pin trusted the generated certificate")
	}
	_, plain := serveOn(t, &v1.Tls{})
	resp, err = http.Get("http://" + plain + remote.HealthPath)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the default serves plaintext: %v %v", resp, err)
	}
	resp.Body.Close()
}
