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

// A guard listener in front of a seat: a TCP forwarder on the mesh address that admits the
// member addresses the role names and forwards to the seat's loopback port. Copies between two
// TCP connections use the kernel's splice on Linux, one hop of a few microseconds.
type forwarder struct {
	ln     net.Listener
	target string
	admit  []net.IP
	single bool
	log    *slog.Logger
	name   string

	mu     sync.Mutex
	active int
	closed bool
	conns  map[net.Conn]struct{}
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
	f := &forwarder{ln: ln, target: target, single: single, log: log, name: name, conns: map[net.Conn]struct{}{}}
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
		if !f.admits(conn.RemoteAddr()) {
			f.log.Warn("guard refused a connection from an address the role does not admit", "seat", f.name, "from", conn.RemoteAddr().String())
			conn.Close()
			continue
		}
		f.mu.Lock()
		if f.closed || f.single && f.active > 0 {
			f.mu.Unlock()
			f.log.Warn("guard refused a second connection to a seat that serves one client", "seat", f.name, "from", conn.RemoteAddr().String())
			conn.Close()
			continue
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

// Whether the remote address is one the role admits: nothing is admitted when none is named
func (f *forwarder) admits(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	for _, a := range f.admit {
		if a.Equal(ip) {
			return true
		}
	}
	return false
}

// Splices one admitted connection to the seat's loopback port until either side closes
func (f *forwarder) serve(client net.Conn) {
	defer func() {
		f.mu.Lock()
		f.active--
		delete(f.conns, client)
		f.mu.Unlock()
		client.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), guardDialTimeout)
	upstream, err := (&net.Dialer{}).DialContext(ctx, "tcp", f.target)
	cancel()
	if err != nil {
		f.log.Warn("guard could not reach the seat", "seat", f.name, "target", f.target, "err", err)
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
	// Copies in bounded rounds, so the counter moves while the connection stays open. A bounded
	// copy between two TCP connections still takes the kernel's splice.
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
