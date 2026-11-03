package mesh

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/pools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A plain HTTP server standing in for a runtime, answering its name on every path
func runtimeServer(t *testing.T, name string) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, name+" "+r.URL.Path) }))
	t.Cleanup(srv.Close)
	return srv, srv.Listener.Addr().String()
}

// A client reaching imported endpoints through a node's endpoint dialer
func dialClient(m *Manager) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = m.DialEndpoint
	return &http.Client{Transport: t, Timeout: 5 * time.Second}
}

// Imported endpoints are reached at the exporter's relays over mutual TLS, metrics and health ports behind relays of their own
func TestDialEndpointReachesRelayOverMutualTLS(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, 200*time.Millisecond)
	b := newNode(t, ctx, 200*time.Millisecond)
	formMesh(t, ctx, a, b)
	eventually(t, "a and b meet", 10*time.Second, func() bool { return allMet(a, b) })
	_, serving := runtimeServer(t, "serving")
	_, metrics := runtimeServer(t, "metrics")
	_, metricsPort, _ := net.SplitHostPort(metrics)
	mport, _ := strconv.Atoi(metricsPort)
	pool := testPool(b.Self(), serving, true)
	pool.Status.Endpoints[0].MetricsPort = uint32(mport)
	b.pools.set([]*v1.Pool{pool}, nil)
	b.Bump()
	owner := pools.OwnerRef{Kind: "node", ID: b.Self() + "/" + pool.GetId()}
	var ep pools.Published
	eventually(t, "a publishes b's endpoint", 10*time.Second, func() bool {
		pub, _ := a.pools.snapshot()
		call, ok := pub[owner]
		if ok && len(call.endpoints) == 1 {
			ep = call.endpoints[0]
		}
		return ok && len(call.endpoints) == 1
	})
	if ep.Address.String() == serving || ep.Address.Port == 0 || int(ep.MetricsPort) == mport || ep.MetricsPort == 0 {
		t.Fatalf("the endpoint is published behind relays, not at b's own ports: %+v", ep)
	}
	get := func(client *http.Client, addr, path string) (string, error) {
		resp, err := client.Get("http://" + addr + path)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body), nil
	}
	if body, err := get(dialClient(a.Manager), ep.Address.String(), "/v1/chat"); err != nil || body != "serving /v1/chat" {
		t.Fatalf("a reaches b's runtime through the relay: %q %v", body, err)
	}
	metricsAddr := net.JoinHostPort(ep.Address.Host, strconv.Itoa(int(ep.MetricsPort)))
	if body, err := get(dialClient(a.Manager), metricsAddr, "/metrics"); err != nil || body != "metrics /metrics" {
		t.Fatalf("a reaches b's metrics port through its relay: %q %v", body, err)
	}
	// An address that is no import is dialed plainly
	if body, err := get(dialClient(a.Manager), serving, "/direct"); err != nil || body != "serving /direct" {
		t.Fatalf("a plain address is dialed as it is: %q %v", body, err)
	}
	// Plaintext and a node outside the mesh are refused at the relay
	if _, err := get(&http.Client{Timeout: 2 * time.Second}, ep.Address.String(), "/"); err == nil {
		t.Fatal("the relay took plaintext")
	}
	c := newNode(t, ctx, 200*time.Millisecond)
	strangerTransport := http.DefaultTransport.(*http.Transport).Clone()
	strangerTransport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&tls.Dialer{Config: c.admissionDialConfig(b.Self(), "", nil)}).DialContext(ctx, network, addr)
	}
	if body, err := get(&http.Client{Transport: strangerTransport, Timeout: 2 * time.Second}, ep.Address.String(), "/"); err == nil {
		t.Fatalf("a node outside the mesh was piped to the runtime: %q", body)
	}
	// Withdrawing the pool closes its relays
	b.pools.set(nil, nil)
	b.Bump()
	eventually(t, "b closes the relays", 10*time.Second, func() bool { return b.relayPort(serving) == 0 && b.relayPort(metrics) == 0 })
	if _, err := get(dialClient(a.Manager), ep.Address.String(), "/"); err == nil {
		t.Fatal("a closed relay still answers")
	}
}
