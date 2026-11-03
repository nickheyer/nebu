package instances

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	guardDialTimeout = 5 * time.Second
	// Bytes one copy round moves before the counter is updated
	guardCopyRound = 256 << 10
)

// A mesh address forwarder admitting the addresses a role names, one client address at a time for single seats
type forwarder struct {
	ln     net.Listener
	target atomic.Value
	admit  []net.IP
	single bool
	log    *slog.Logger
	name   string

	mu     sync.Mutex
	active int
	closed bool
	conns  map[net.Conn]struct{}
	// The address whose connections are open, for a seat that serves one client
	client net.IP
	// Bytes forwarded to the seat, what its head streamed to it
	received atomic.Int64
}

// Bytes the guard forwarded to the seat so far
func (f *forwarder) Received() uint64 { return uint64(max(f.received.Load(), 0)) }

// Binds the guard on the mesh address, admitting the given hosts
func newForwarder(bind, target string, admit []string, single bool, name string, log *slog.Logger) (*forwarder, error) {
	ln, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, fmt.Errorf("guard listener %s: %w", bind, err)
	}
	f := &forwarder{ln: ln, single: single, log: log, name: name, conns: map[net.Conn]struct{}{}}
	f.target.Store(target)
	for _, a := range admit {
		host := a
		if h, _, err := net.SplitHostPort(a); err == nil {
			host = h
		}
		if ip := net.ParseIP(host); ip != nil {
			f.admit = append(f.admit, ip)
			continue
		}
		ips, err := net.LookupIP(host)
		if err != nil {
			ln.Close()
			return nil, fmt.Errorf("guard admits %s: %w", a, err)
		}
		f.admit = append(f.admit, ips...)
	}
	go f.accept()
	return f, nil
}

// The address the guard listens on
func (f *forwarder) Addr() string { return f.ln.Addr().String() }

// Points connections accepted from now on at another seat port
func (f *forwarder) Retarget(target string) { f.target.Store(target) }

// The port the guard listens on
func (f *forwarder) Port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *forwarder) accept() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				f.log.Warn("guard accept failed", "seat", f.name, "err", err)
			}
			return
		}
		ip, ok := f.admits(conn.RemoteAddr())
		if !ok {
			f.log.Warn("guard refused a connection from an address the role does not admit", "seat", f.name, "from", conn.RemoteAddr().String())
			conn.Close()
			continue
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			conn.Close()
			return
		}
		if f.single && f.active > 0 && !f.client.Equal(ip) {
			serving := f.client.String()
			f.mu.Unlock()
			f.log.Warn("guard refused a connection from a second address to a seat that serves one client", "seat", f.name, "from", conn.RemoteAddr().String(), "serving", serving)
			conn.Close()
			continue
		}
		if f.active == 0 {
			f.client = ip
		}
		f.active++
		f.conns[conn] = struct{}{}
		f.mu.Unlock()
		go f.serve(conn)
	}
}

// Connections the guard holds open to the seat
func (f *forwarder) Active() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}

// The remote IP when the role admits it, nothing admitted when the role names no address
func (f *forwarder) admits(addr net.Addr) (net.IP, bool) {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil, false
	}
	ip := net.ParseIP(host)
	for _, a := range f.admit {
		if a.Equal(ip) {
			return ip, true
		}
	}
	return nil, false
}

// Splices one admitted connection to the seat's loopback port until either side closes
func (f *forwarder) serve(client net.Conn) {
	defer func() {
		f.mu.Lock()
		f.active--
		if f.active == 0 {
			f.client = nil
		}
		delete(f.conns, client)
		f.mu.Unlock()
		client.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), guardDialTimeout)
	target := f.target.Load().(string)
	upstream, err := (&net.Dialer{}).DialContext(ctx, "tcp", target)
	cancel()
	if err != nil {
		f.log.Warn("guard could not reach the seat", "seat", f.name, "target", target, "err", err)
		return
	}
	defer upstream.Close()
	if tc, ok := client.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	if tc, ok := upstream.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	done := make(chan struct{}, 2)
	// Copies in bounded rounds so the counter moves while open, each round still using the kernel splice
	pipe := func(dst, src net.Conn, count bool) {
		for {
			n, err := io.CopyN(dst, src, guardCopyRound)
			if count {
				f.received.Add(n)
			}
			if err != nil {
				break
			}
		}
		if tc, ok := dst.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(upstream, client, true)
	go pipe(client, upstream, false)
	<-done
	<-done
}

// Stops listening and drops open connections
func (f *forwarder) Close() {
	f.mu.Lock()
	f.closed = true
	conns := make([]net.Conn, 0, len(f.conns))
	for c := range f.conns {
		conns = append(conns, c)
	}
	f.mu.Unlock()
	f.ln.Close()
	for _, c := range conns {
		c.Close()
	}
}
