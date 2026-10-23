// Package outbound reaches the internet and the LAN on a client's behalf, never this host's own
// addresses, link local ranges, or an environment proxy that could reach them
package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

const dialTimeout = 30 * time.Second

// Returned when a destination resolves to an address a client may not reach through this host
var ErrRefused = errors.New("address refused")

// Why an address may not be reached, nil when it may
func Allowed(ip net.IP) error {
	switch {
	case ip.IsUnspecified(), ip.IsLoopback():
		return fmt.Errorf("%w: %s is this host", ErrRefused, ip)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return fmt.Errorf("%w: %s is link local", ErrRefused, ip)
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 0 {
		return fmt.Errorf("%w: %s is this network", ErrRefused, ip)
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return fmt.Errorf("%w: %s is this host", ErrRefused, ip)
		}
	}
	return nil
}

// Dials a host after checking every address it resolves to
func dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		found, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, a := range found {
			ips = append(ips, a.IP)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s resolves to no address", host)
	}
	for _, ip := range ips {
		if err := Allowed(ip); err != nil {
			return nil, fmt.Errorf("%s: %w", host, err)
		}
	}
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	var last error
	for _, ip := range ips {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

// A transport that dials only what Allowed permits, redirects included
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = dial
	return t
}

// A client over the guarded transport with a whole request timeout
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Transport: Transport(), Timeout: timeout}
}
