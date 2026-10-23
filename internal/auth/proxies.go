package auth

import (
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
)

// Proxy networks whose forwarded headers are believed
type Proxies struct {
	nets []*net.IPNet
}

// Parses auth.trusted_proxies, a bare address counting as one host
func ParseProxies(cidrs []string) (Proxies, error) {
	var p Proxies
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			ip := net.ParseIP(c)
			if ip == nil {
				return Proxies{}, fmt.Errorf("auth.trusted_proxies %q is not an address or CIDR", c)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			c = fmt.Sprintf("%s/%d", ip, bits)
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return Proxies{}, fmt.Errorf("auth.trusted_proxies %q is not an address or CIDR", c)
		}
		p.nets = append(p.nets, n)
	}
	return p, nil
}

// Whether an address, with or without a port, belongs to a trusted proxy
func (p Proxies) Trusted(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	for _, n := range p.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// The client's address, the nearest untrusted hop of X-Forwarded-For behind a trusted proxy
func (p Proxies) Client(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !p.Trusted(r.RemoteAddr) {
		return host
	}
	for _, hop := range slices.Backward(strings.Split(r.Header.Get("X-Forwarded-For"), ",")) {
		if h := strings.TrimSpace(hop); h != "" && !p.Trusted(h) {
			return h
		}
	}
	return host
}

// Whether the browser reached the daemon over HTTPS, directly or through a trusted proxy
func (p Proxies) Secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return p.Trusted(r.RemoteAddr) && strings.EqualFold(strings.TrimSpace(proto), "https")
}

// The host the browser addressed, X-Forwarded-Host behind a trusted proxy
func (p Proxies) Host(r *http.Request) string {
	if p.Trusted(r.RemoteAddr) {
		if h, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ","); strings.TrimSpace(h) != "" {
			return strings.TrimSpace(h)
		}
	}
	return r.Host
}

// Sends the cookie, marking it secure when the browser used HTTPS
func (p Proxies) SetCookie(w http.ResponseWriter, r *http.Request, c *http.Cookie) {
	c.Secure = p.Secure(r)
	http.SetCookie(w, c)
}

// Removes the cookie from the browser
func (p Proxies) ClearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: p.Secure(r), SameSite: http.SameSiteLaxMode})
}
