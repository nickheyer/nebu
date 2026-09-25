// Package links measures the connection between two mesh nodes: round trip by HTTP/2 ping frames,
// bandwidth by streaming bytes over one and over several connections, and the facts of the local
// interface the route leaves through. Measurements sort a link into the class the planner and the
// Mesh page read.
package links

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Ping frames per round trip measurement
	Pings = 20
	// Bytes one bandwidth run moves, and the smaller run for links measured under 100 Mb/s
	StreamBytes      = 64 << 20
	SmallStreamBytes = 8 << 20
	smallBelow       = 100e6 / 8
	// Connections the aggregate run opens at once
	ParallelConns = 4
	// The path members post bytes to for an upload measurement, under the session credential
	SinkPath = "/mesh/sink"
	// Bytes one Stream chunk carries
	ChunkBytes  = 256 << 10
	dialTimeout = 5 * time.Second
	pingTimeout = 5 * time.Second
	runTimeout  = 2 * time.Minute

	// Class cutoffs
	fabricRTT       = 100 * time.Microsecond
	fabricAggregate = 100e9 / 8
	fastRTT         = 300 * time.Microsecond
	fastStream      = 10e9 / 8
	lanRTT          = 2 * time.Millisecond
	lanStream       = 1e9 / 8
)

// One member to measure, and how to reach it
type Target struct {
	ID      string
	Address string
	// The TLS configuration to dial with, nil for a plain listener
	TLS *tls.Config
	// The session credential every request carries
	Header http.Header
	// The RDMA device the peer reports bound on its end, from its own link record, empty when none
	PeerRDMA string
}

// Measures links from this node
type Prober struct {
	Log *slog.Logger
}

// Measures the link to one member. The last measurement sizes the bandwidth run and stands in
// for it when hold is set, because a formation is serving on one of the two nodes.
func (p *Prober) Measure(ctx context.Context, from string, t Target, last *v1.Link, hold bool) (*v1.Link, error) {
	link := &v1.Link{From: from, To: t.ID, MeasuredAt: timestamppb.Now()}
	if err := p.interfaceFacts(t.Address, link); err != nil {
		return nil, err
	}
	median, p95, err := p.roundTrip(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("round trip to %s: %w", t.Address, err)
	}
	link.RttUs, link.RttP95Us = uint32(median.Microseconds()), uint32(p95.Microseconds())
	var download float64
	switch {
	case hold:
		link.StreamBytesPerSecond, link.AggregateBytesPerSecond = last.GetStreamBytesPerSecond(), last.GetAggregateBytesPerSecond()
		link.BandwidthHeld = true
	default:
		n := int64(StreamBytes)
		if last != nil && last.GetStreamBytesPerSecond() > 0 && float64(last.GetStreamBytesPerSecond()) < smallBelow {
			n = SmallStreamBytes
		}
		stream, err := p.upload(ctx, t, n, 1)
		if err != nil {
			return nil, fmt.Errorf("upload to %s: %w", t.Address, err)
		}
		aggregate, err := p.upload(ctx, t, n, ParallelConns)
		if err != nil {
			return nil, fmt.Errorf("aggregate upload to %s: %w", t.Address, err)
		}
		if download, err = p.download(ctx, t, n); err != nil {
			return nil, fmt.Errorf("download from %s: %w", t.Address, err)
		}
		link.StreamBytesPerSecond, link.AggregateBytesPerSecond = uint64(stream), uint64(max(aggregate, stream))
	}
	link.Class, link.Detail = Classify(link, t.PeerRDMA)
	if download > 0 {
		link.Detail += fmt.Sprintf("; %s back", gbits(download))
	}
	if hold {
		link.Detail += "; bandwidth kept from the last run, a formation is serving"
	}
	return link, nil
}

// The local interface the route to the peer leaves through, with its facts
func (p *Prober) interfaceFacts(address string, link *v1.Link) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("mesh address %q: %w", address, err)
	}
	peerIPs, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil || len(peerIPs) == 0 {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	peer := peerIPs[0].IP
	conn, err := net.Dial("udp", net.JoinHostPort(peer.String(), "9"))
	if err != nil {
		return fmt.Errorf("route to %s: %w", peer, err)
	}
	local := conn.LocalAddr().(*net.UDPAddr).IP
	conn.Close()
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || !ipNet.IP.Equal(local) {
				continue
			}
			link.Interface = ifc.Name
			link.Mtu = uint32(ifc.MTU)
			link.Subnet = ipNet.Contains(peer)
			link.InterfaceBitsPerSecond = speedOf(ifc.Name)
			link.RdmaDevice = rdmaOf(ifc.Name)
			return nil
		}
	}
	return fmt.Errorf("no interface holds %s, the address the route to %s leaves from", local, peer)
}

// Median and 95th percentile of ping frames on one HTTP/2 connection
func (p *Prober) roundTrip(ctx context.Context, t Target) (time.Duration, time.Duration, error) {
	conn, cc, err := dialH2(ctx, t)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close()
	defer cc.Close()
	samples := make([]time.Duration, 0, Pings)
	for i := 0; i < Pings; i++ {
		pctx, cancel := context.WithTimeout(ctx, pingTimeout)
		start := time.Now()
		err := cc.Ping(pctx)
		cancel()
		if err != nil {
			return 0, 0, err
		}
		samples = append(samples, time.Since(start))
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[len(samples)/2], samples[(len(samples)*95+99)/100-1], nil
}

// Sends n bytes to the peer's sink over conns connections at once, bytes per second overall
func (p *Prober) upload(ctx context.Context, t Target, n int64, conns int) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	share := n / int64(conns)
	var wg sync.WaitGroup
	errs := make([]error, conns)
	start := time.Now()
	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			client := httpClient(t)
			defer client.CloseIdleConnections()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, base(t)+SinkPath, io.LimitReader(zeros{}, share))
			if err != nil {
				errs[i] = err
				return
			}
			req.ContentLength = share
			for k, v := range t.Header {
				req.Header[k] = v
			}
			req.Header.Set("Content-Type", "application/octet-stream")
			resp, err := client.Do(req)
			if err != nil {
				errs[i] = err
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs[i] = fmt.Errorf("sink answered %d", resp.StatusCode)
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	if err := errors.Join(errs...); err != nil {
		return 0, err
	}
	if elapsed <= 0 {
		return 0, errors.New("the upload took no time")
	}
	return float64(share*int64(conns)) / elapsed, nil
}

// Pulls n bytes from the peer's Stream over one connection, bytes per second
func (p *Prober) download(ctx context.Context, t Target, n int64) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	client := httpClient(t)
	defer client.CloseIdleConnections()
	mesh := nebuv1connect.NewMeshServiceClient(client, base(t))
	req := connect.NewRequest(&v1.StreamRequest{Bytes: uint64(n)})
	for k, v := range t.Header {
		req.Header()[k] = v
	}
	start := time.Now()
	stream, err := mesh.Stream(ctx, req)
	if err != nil {
		return 0, err
	}
	defer stream.Close()
	var got int64
	for stream.Receive() {
		got += int64(len(stream.Msg().GetChunk()))
	}
	if err := stream.Err(); err != nil {
		return 0, err
	}
	elapsed := time.Since(start).Seconds()
	if got < n {
		return 0, fmt.Errorf("the stream sent %d of %d bytes", got, n)
	}
	return float64(got) / elapsed, nil
}

// Sorts a link into its class from the cutoffs, naming why in the detail
func Classify(l *v1.Link, peerRDMA string) (v1.LinkClass, string) {
	rtt := time.Duration(l.GetRttUs()) * time.Microsecond
	stream, aggregate := float64(l.GetStreamBytesPerSecond()), float64(l.GetAggregateBytesPerSecond())
	facts := fmt.Sprintf("%s round trip, %s per stream, %s aggregate", seconds(rtt), gbits(stream), gbits(aggregate))
	switch {
	case l.GetRdmaDevice() != "" && peerRDMA != "" && rtt < fabricRTT && aggregate >= fabricAggregate:
		return v1.LinkClass_LINK_CLASS_FABRIC, fmt.Sprintf("fabric: RDMA %s here and %s on the peer, %s", l.GetRdmaDevice(), peerRDMA, facts)
	case rtt < fastRTT && stream >= fastStream:
		why := ""
		switch {
		case l.GetRdmaDevice() == "" && peerRDMA == "":
			why = ", not fabric: no RDMA device on either end"
		case l.GetRdmaDevice() == "":
			why = ", not fabric: no RDMA device bound on this end"
		case peerRDMA == "":
			why = ", not fabric: no RDMA device bound on the peer's end"
		case rtt >= fabricRTT:
			why = fmt.Sprintf(", not fabric: round trip over %s", seconds(fabricRTT))
		default:
			why = fmt.Sprintf(", not fabric: aggregate under %s", gbits(fabricAggregate))
		}
		return v1.LinkClass_LINK_CLASS_FAST, "fast: " + facts + why
	case rtt < lanRTT && stream >= lanStream:
		why := ""
		if rtt >= fastRTT {
			why = fmt.Sprintf(", not fast: round trip over %s", seconds(fastRTT))
		} else {
			why = fmt.Sprintf(", not fast: stream under %s", gbits(fastStream))
		}
		return v1.LinkClass_LINK_CLASS_LAN, "lan: " + facts + why
	}
	why := ""
	if rtt >= lanRTT {
		why = fmt.Sprintf(", not lan: round trip over %s", seconds(lanRTT))
	} else {
		why = fmt.Sprintf(", not lan: stream under %s", gbits(lanStream))
	}
	return v1.LinkClass_LINK_CLASS_SLOW, "slow: " + facts + why
}

// Opens one HTTP/2 connection to the target, plain prior knowledge or TLS
func dialH2(ctx context.Context, t Target) (net.Conn, *http2.ClientConn, error) {
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	conn, err := dial(dctx, t)
	if err != nil {
		return nil, nil, err
	}
	tr := &http2.Transport{AllowHTTP: t.TLS == nil}
	cc, err := tr.NewClientConn(conn)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, cc, nil
}

// Dials the target over TCP, wrapping TLS when the target has it
func dial(ctx context.Context, t Target) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout}
	if t.TLS == nil {
		return d.DialContext(ctx, "tcp", t.Address)
	}
	cfg := t.TLS.Clone()
	if cfg.ServerName == "" {
		host, _, _ := net.SplitHostPort(t.Address)
		cfg.ServerName = host
	}
	if len(cfg.NextProtos) == 0 {
		cfg.NextProtos = []string{"h2"}
	}
	return (&tls.Dialer{NetDialer: d, Config: cfg}).DialContext(ctx, "tcp", t.Address)
}

// An HTTP/2 client that holds one connection to the target
func httpClient(t Target) *http.Client {
	tr := &http2.Transport{
		AllowHTTP: t.TLS == nil,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return dial(ctx, t)
		},
	}
	return &http.Client{Transport: tr}
}

func base(t Target) string {
	if t.TLS != nil {
		return "https://" + t.Address
	}
	return "http://" + t.Address
}

// A reader of endless zero bytes
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// Serves the sink: reads a body and answers with the bytes it took
func Sink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "the sink takes POST", http.StatusMethodNotAllowed)
		return
	}
	n, err := io.Copy(io.Discard, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "%d\n", n)
}

// Streams n bytes in chunks, for a download measurement
func Stream(ctx context.Context, n uint64, send func(*v1.StreamResponse) error) error {
	chunk := make([]byte, ChunkBytes)
	for n > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		size := uint64(len(chunk))
		if n < size {
			size = n
		}
		if err := send(&v1.StreamResponse{Chunk: chunk[:size]}); err != nil {
			return err
		}
		n -= size
	}
	return nil
}

func gbits(bps float64) string {
	switch {
	case bps >= 1e9/8:
		return fmt.Sprintf("%.1f Gb/s", bps*8/1e9)
	case bps >= 1e6/8:
		return fmt.Sprintf("%.0f Mb/s", bps*8/1e6)
	}
	return fmt.Sprintf("%.0f kb/s", bps*8/1e3)
}

func seconds(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.1f s", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%.1f ms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%d µs", d.Microseconds())
}

// Names a class for a person
func ClassName(c v1.LinkClass) string {
	return strings.ToLower(strings.TrimPrefix(c.String(), "LINK_CLASS_"))
}
