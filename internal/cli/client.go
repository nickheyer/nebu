package cli

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/daemon"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/eval"
	"github.com/nickheyer/nebu/pkg/logger"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	localBase     = "http://nebu.local"
	dialTimeout   = 300 * time.Millisecond
	plainInterval = 2 * time.Second
	lineWidth     = 100
)

// Connect clients for every service
type clients struct {
	host      nebuv1connect.HostServiceClient
	settings  nebuv1connect.SettingsServiceClient
	sources   nebuv1connect.SourceServiceClient
	runtimes  nebuv1connect.RuntimeServiceClient
	estimate  nebuv1connect.EstimateServiceClient
	store     nebuv1connect.StoreServiceClient
	tasks     nebuv1connect.TaskServiceClient
	instances nebuv1connect.InstanceServiceClient
	builds    nebuv1connect.BuildServiceClient
	slots     nebuv1connect.SlotServiceClient
	gateway   nebuv1connect.GatewayServiceClient
	monitor   nebuv1connect.MonitorServiceClient
	events    nebuv1connect.EventServiceClient
}

// Adds the bearer token to every request
type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token != "" && req.Header.Get("Authorization") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(req)
}

// Serves requests through an in process handler
type handlerTransport struct {
	handler http.Handler
}

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	pr, pw := io.Pipe()
	w := &responseWriter{header: http.Header{}, pw: pw, ready: make(chan struct{})}
	go func() {
		defer pw.Close()
		defer w.WriteHeader(http.StatusOK)
		t.handler.ServeHTTP(w, req)
	}()
	select {
	case <-w.ready:
	case <-req.Context().Done():
		pr.Close()
		return nil, req.Context().Err()
	}
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", w.status, http.StatusText(w.status)),
		StatusCode: w.status,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     w.snapshot,
		Body:       pr,
		Request:    req,
	}, nil
}

// Streams handler output into a pipe
type responseWriter struct {
	header   http.Header
	snapshot http.Header
	pw       *io.PipeWriter
	status   int
	ready    chan struct{}
	once     sync.Once
}

func (w *responseWriter) Header() http.Header { return w.header }

func (w *responseWriter) WriteHeader(code int) {
	w.once.Do(func() {
		w.status = code
		w.snapshot = w.header.Clone()
		close(w.ready)
	})
}

func (w *responseWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pw.Write(p)
}

func (w *responseWriter) Flush() { w.WriteHeader(http.StatusOK) }

// Builds clients against the daemon or an in process handler
func (e *env) clients() (*clients, error) {
	if e.cl != nil {
		return e.cl, nil
	}
	base := localBase
	httpClient := &http.Client{}
	if addr := e.resolveAddr(); addr != "" {
		base = e.base(addr)
		httpClient.Transport = e.transport(base)
	} else {
		log, closer, err := logger.New(quiet(e.cfg.GetLogging()))
		if err != nil {
			return nil, err
		}
		e.closers = append(e.closers, closer)
		d, err := daemon.New(e.cfg, log)
		if err != nil {
			return nil, err
		}
		e.daemon = d
		httpClient.Transport = handlerTransport{handler: d.Handler()}
	}
	httpClient.Transport = authTransport{base: httpClient.Transport, token: e.cfg.GetAuth().GetToken()}
	e.cl = &clients{
		host:      nebuv1connect.NewHostServiceClient(httpClient, base),
		settings:  nebuv1connect.NewSettingsServiceClient(httpClient, base),
		sources:   nebuv1connect.NewSourceServiceClient(httpClient, base),
		runtimes:  nebuv1connect.NewRuntimeServiceClient(httpClient, base),
		estimate:  nebuv1connect.NewEstimateServiceClient(httpClient, base),
		store:     nebuv1connect.NewStoreServiceClient(httpClient, base),
		tasks:     nebuv1connect.NewTaskServiceClient(httpClient, base),
		instances: nebuv1connect.NewInstanceServiceClient(httpClient, base),
		builds:    nebuv1connect.NewBuildServiceClient(httpClient, base),
		slots:     nebuv1connect.NewSlotServiceClient(httpClient, base),
		gateway:   nebuv1connect.NewGatewayServiceClient(httpClient, base),
		monitor:   nebuv1connect.NewMonitorServiceClient(httpClient, base),
		events:    nebuv1connect.NewEventServiceClient(httpClient, base),
	}
	return e.cl, nil
}

// Raises an in process daemon to warn level unless debugging
func quiet(cfg *v1.Logging) *v1.Logging {
	out := proto.Clone(cfg).(*v1.Logging)
	if !strings.EqualFold(out.GetLevel(), "debug") {
		out.Level = "warn"
	}
	return out
}

// Fails commands whose state lives only in a running daemon
func (e *env) requireDaemon() error {
	if e.resolveAddr() == "" {
		return fmt.Errorf("this command needs a running daemon, start nebu serve or pass --addr")
	}
	return nil
}

// The daemon address, configured or found listening, empty when this process would be the daemon
func (e *env) resolveAddr() string {
	if e.resolved {
		return e.addr
	}
	e.resolved = true
	addr := e.cfg.GetAddr()
	if listen := dialable(e.cfg.GetListen(), ""); addr == "" && reachable(listen) {
		addr = listen
		e.log.Debug("using running daemon", "addr", addr)
	}
	e.addr = addr
	return addr
}

// Prefixes a bare address with the scheme the daemon serves
func (e *env) base(addr string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	if e.cfg.GetTls().GetCertFile() != "" {
		return "https://" + addr
	}
	return "http://" + addr
}

// Swaps an unspecified host for one this machine can dial
func dialable(addr, host string) string {
	h, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(h); h != "" && (ip == nil || !ip.IsUnspecified()) {
		return addr
	}
	if host == "" {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}

// Reports whether something accepts connections at addr
func reachable(addr string) bool {
	if addr == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// The transport a daemon at base is dialed through, trusting its own certificate as is
func (e *env) transport(base string) http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ForceAttemptHTTP2 = true
	own := e.ownCert()
	// An address dialed by IP sends no server name, so the name checked is the one dialed
	dialed := ""
	if u, err := url.Parse(base); err == nil {
		dialed = u.Hostname()
	}
	t.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true,
		// The daemon's configured certificate needs no authority or name, anything else does
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("tls: the server presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			if own != nil && bytes.Equal(leaf.Raw, own.Raw) {
				return nil
			}
			opts := x509.VerifyOptions{DNSName: cmp.Or(cs.ServerName, dialed), Intermediates: x509.NewCertPool()}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := leaf.Verify(opts)
			return err
		},
	}
	return t
}

// The certificate tls.cert_file holds, nil when unset or unreadable
func (e *env) ownCert() *x509.Certificate {
	file := e.cfg.GetTls().GetCertFile()
	if file == "" {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		e.log.Debug("tls.cert_file unreadable, verifying the daemon against system roots", "file", file, "err", err)
		return nil
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	return cert
}

// Where this machine reaches the gateway, the daemon saying whether it has a listener of its own
func (e *env) gatewayBase(ctx context.Context) string {
	api := e.resolveAddr()
	if api == "" {
		api = dialable(e.cfg.GetListen(), "")
	}
	host := ""
	if u, err := url.Parse(e.base(api)); err == nil {
		host = u.Hostname()
	}
	if st := e.gatewayStatus(ctx); st != nil {
		for _, l := range st.GetListeners() {
			if l.GetShared() {
				continue
			}
			scheme := "http"
			if st.GetTls() {
				scheme = "https"
			}
			return scheme + "://" + dialable(l.GetAddr(), host)
		}
	}
	return e.base(api)
}

// What the gateway reports, nil when the daemon cannot say
func (e *env) gatewayStatus(ctx context.Context) *v1.GatewayStatus {
	resp, err := e.cl.gateway.GetGatewayStatus(ctx, connect.NewRequest(&v1.GetGatewayStatusRequest{}))
	if err != nil {
		return nil
	}
	return resp.Msg.GetStatus()
}

// Resolves an empty source id to the first configured source
func (e *env) defaultSource(ctx context.Context, id string) (string, error) {
	if id != "" {
		return id, nil
	}
	resp, err := e.cl.sources.ListSources(ctx, connect.NewRequest(&v1.ListSourcesRequest{}))
	if err != nil {
		return "", err
	}
	if len(resp.Msg.GetSources()) == 0 {
		return "", fmt.Errorf("no sources configured")
	}
	return resp.Msg.GetSources()[0].GetSource().GetId(), nil
}

// Finds one source by id among the daemon's list
func (e *env) findSource(ctx context.Context, id string) (*v1.SourceStatus, error) {
	resp, err := e.cl.sources.ListSources(ctx, connect.NewRequest(&v1.ListSourcesRequest{}))
	if err != nil {
		return nil, err
	}
	for _, st := range resp.Msg.GetSources() {
		if st.GetSource().GetId() == id {
			return st, nil
		}
	}
	return nil, fmt.Errorf("unknown source %q, see nebu sources", id)
}

// Resolves an empty group to the only stored group
func (e *env) onlyGroup(ctx context.Context, source, repo, group string) (string, error) {
	if group != "" {
		return group, nil
	}
	resp, err := e.cl.store.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{}))
	if err != nil {
		return "", err
	}
	var names []string
	for _, m := range resp.Msg.GetModels() {
		if m.GetSourceId() == source && m.GetRepo() == repo {
			names = append(names, m.GetGroup())
		}
	}
	switch len(names) {
	case 0:
		return "", fmt.Errorf("%s is not stored", repo)
	case 1:
		return names[0], nil
	}
	return "", fmt.Errorf("%s has groups %s, pass --group", repo, strings.Join(names, ", "))
}

// Resolves a repo and its group through the stored models, the request naming both
func (e *env) storedModel(ctx context.Context, source, repo, group string) (string, string, error) {
	sourceID, err := e.defaultSource(ctx, source)
	if err != nil {
		return "", "", err
	}
	groupName, err := e.onlyGroup(ctx, sourceID, repo, group)
	return sourceID, groupName, err
}

// Finds one profile by id or name among the daemon's list
func (e *env) findProfile(ctx context.Context, runtimeID, ref string) (*v1.Profile, error) {
	resp, err := e.cl.runtimes.ListProfiles(ctx, connect.NewRequest(&v1.ListProfilesRequest{RuntimeId: runtimeID}))
	if err != nil {
		return nil, err
	}
	var found *v1.Profile
	for _, p := range resp.Msg.GetProfiles() {
		if p.GetId() == ref {
			return p, nil
		}
		if strings.EqualFold(p.GetName(), ref) {
			if found != nil {
				return nil, fmt.Errorf("%q names a profile of both %s and %s, pass its id or --runtime", ref, found.GetRuntimeId(), p.GetRuntimeId())
			}
			found = p
		}
	}
	if found == nil {
		return nil, fmt.Errorf("unknown profile %q, see nebu profiles", ref)
	}
	return found, nil
}

// Follows a task to the end, progress and logs on a terminal, the final snapshot alone as JSON
func (e *env) follow(ctx context.Context, id string) (*v1.Task, error) {
	stream, err := e.cl.tasks.WatchTask(ctx, connect.NewRequest(&v1.WatchTaskRequest{Id: id}))
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	var r *renderer
	if !e.json {
		r = &renderer{out: e.out, tty: isTerminal(e.out)}
	}
	var last *v1.Task
	for stream.Receive() {
		msg := stream.Msg()
		last = msg.GetTask()
		if r == nil {
			continue
		}
		for _, line := range msg.GetLogs() {
			r.log(line)
		}
		r.progress(last)
	}
	r.finish()
	if err := stream.Err(); err != nil {
		return last, err
	}
	if last == nil {
		return nil, fmt.Errorf("task %s ended without a snapshot", id)
	}
	switch last.GetState() {
	case v1.TaskState_TASK_STATE_FAILED:
		return last, fmt.Errorf("%s failed: %s", last.GetTitle(), last.GetError())
	case v1.TaskState_TASK_STATE_CANCELED:
		return last, fmt.Errorf("%s canceled", last.GetTitle())
	}
	return last, nil
}

// Draws a single updating status line on terminals, nil safe
type renderer struct {
	out      io.Writer
	tty      bool
	lastDone uint64
	lastAt   time.Time
	rate     float64
	lastLine time.Time
	dirty    bool
}

func (r *renderer) log(line string) {
	r.clear()
	fmt.Fprintln(r.out, line)
}

func (r *renderer) progress(t *v1.Task) {
	now := time.Now()
	p := t.GetProgress()
	if !r.lastAt.IsZero() && p.GetDone() >= r.lastDone {
		elapsed := now.Sub(r.lastAt).Seconds()
		if elapsed > 0 {
			instant := float64(p.GetDone()-r.lastDone) / elapsed
			if r.rate == 0 {
				r.rate = instant
			} else {
				r.rate = 0.7*r.rate + 0.3*instant
			}
		}
	}
	r.lastDone, r.lastAt = p.GetDone(), now
	line := status(t, r.rate)
	if r.tty {
		fmt.Fprintf(r.out, "\r%-*s", lineWidth, truncate(line, lineWidth))
		r.dirty = true
		return
	}
	if terminalState(t.GetState()) || now.Sub(r.lastLine) >= plainInterval {
		fmt.Fprintln(r.out, line)
		r.lastLine = now
	}
}

func (r *renderer) clear() {
	if r.tty && r.dirty {
		fmt.Fprintf(r.out, "\r%-*s\r", lineWidth, "")
		r.dirty = false
	}
}

func (r *renderer) finish() {
	if r != nil && r.tty && r.dirty {
		fmt.Fprintln(r.out)
		r.dirty = false
	}
}

func status(t *v1.Task, rate float64) string {
	p := t.GetProgress()
	parts := []string{t.GetTitle(), loud(t.GetState())}
	if p.GetTotal() > 0 {
		pct := float64(p.GetDone()) * 100 / float64(p.GetTotal())
		if p.GetTotal() < 1000 {
			parts = append(parts, fmt.Sprintf("%d/%d", p.GetDone(), p.GetTotal()))
		} else {
			parts = append(parts, fmt.Sprintf("%s/%s %.0f%%", estimate.Human(p.GetDone()), estimate.Human(p.GetTotal()), pct))
		}
	}
	if rate > 0 && !terminalState(t.GetState()) {
		parts = append(parts, estimate.Human(uint64(rate))+"/s")
	}
	if p.GetMessage() != "" {
		parts = append(parts, p.GetMessage())
	}
	return strings.Join(parts, "  ")
}

func terminalState(s v1.TaskState) bool {
	switch s {
	case v1.TaskState_TASK_STATE_SUCCEEDED, v1.TaskState_TASK_STATE_FAILED, v1.TaskState_TASK_STATE_CANCELED:
		return true
	}
	return false
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// An enum's short name in capitals, how states read in tables
func loud(e protoreflect.Enum) string { return strings.ToUpper(eval.EnumShort(e)) }
