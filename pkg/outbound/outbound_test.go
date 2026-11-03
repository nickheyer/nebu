package outbound

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Addresses of this host, its links and multicast groups are refused, the rest allowed
func TestAllowed(t *testing.T) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		ip      string
		refused bool
	}{
		{"loopback v4", "127.0.0.1", true},
		{"loopback v6", "::1", true},
		{"unspecified v4", "0.0.0.0", true},
		{"unspecified v6", "::", true},
		{"this network", "0.1.2.3", true},
		{"link local v4", "169.254.10.10", true},
		{"link local v6", "fe80::1", true},
		{"multicast v4", "224.0.0.1", true},
		{"multicast v6", "ff02::1", true},
		{"interface local multicast", "ff01::1", true},
		{"public v4", "93.184.216.34", false},
		{"public v6", "2606:2800:220:1:248:1893:25c8:1946", false},
		{"private lan", "10.20.30.40", false},
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			cases = append(cases, struct {
				name    string
				ip      string
				refused bool
			}{"own interface " + n.IP.String(), n.IP.String(), true})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Allowed(net.ParseIP(tc.ip))
			if errors.Is(err, ErrRefused) != tc.refused || err != nil && !tc.refused {
				t.Fatalf("Allowed(%s) = %v", tc.ip, err)
			}
		})
	}
}

// Dialing refuses a loopback server by literal address and by name, before any connection is made
func TestDialRefusesThisHost(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	client := Client(2 * time.Second)
	for _, url := range []string{srv.URL, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)} {
		_, err := client.Get(url)
		if !errors.Is(err, ErrRefused) {
			t.Fatalf("%s: %v", url, err)
		}
	}
	if hits != 0 {
		t.Fatalf("the server was reached %d times", hits)
	}
	if _, err := dial(context.Background(), "tcp", "no-port"); err == nil {
		t.Fatal("an address without a port must fail")
	}
}

// The transport ignores the environment proxy and dials through the guard, redirects included
func TestTransportHasNoProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	tr := Transport()
	if tr.Proxy != nil {
		t.Fatal("the transport must not take a proxy from the environment")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:9/", http.StatusFound)
	}))
	defer srv.Close()
	if _, err := (&http.Client{Transport: tr}).Get(srv.URL); !errors.Is(err, ErrRefused) {
		t.Fatalf("loopback through the transport: %v", err)
	}
}
