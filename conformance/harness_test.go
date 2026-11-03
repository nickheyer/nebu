//go:build conformance

package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/stub"
)

var (
	gatewayURL = flag.String("gateway-url", "", "base URL of the gateway under test, such as https://127.0.0.1:8080")
	apiURL     = flag.String("api-url", "", "base URL of the nebu v1 Connect API of the implementation under test")
	token      = flag.String("token", "", "bearer token sent to the API and the gateway")
	tlsCA      = flag.String("tls-ca", "", "PEM certificate trusted for the gateway and API besides the system roots")
	reportDir  = flag.String("report", "", "directory the YAML and Markdown reports are written to")
	stubHost   = flag.String("stub-host", "127.0.0.1", "address the stub backends listen on, reachable from the gateway")
)

// Routing headers the protocol defines
const (
	headerServed = "X-Gateway-Destination-Endpoint-Served"
	headerSubset = "X-Gateway-Destination-Endpoint-Subset"
	headerStubID = "X-Stub-Id"
)

// The runtime the stub backends emulate, telling the gateway which metrics they export
const stubRuntime = "vllm"

// Condition types and the reasons the protocol documents
const (
	condAccepted     = "Accepted"
	condResolvedRefs = "ResolvedRefs"
	condReady        = "Ready"

	reasonInvalidPoolRef         = "InvalidPoolRef"
	reasonUnknownSchedulerConfig = "UnknownSchedulerConfig"
)

// How long status and traffic take to settle after a change
const (
	settle = 20 * time.Second
	poll   = 100 * time.Millisecond
)

var suite *harness

// Reads the flags, reaches the implementation, runs every feature and writes the report
func TestMain(m *testing.M) {
	flag.Parse()
	h, err := dial()
	if err != nil {
		fmt.Fprintln(os.Stderr, "conformance:", err)
		os.Exit(2)
	}
	suite = h
	code := m.Run()
	rep, err := NewReport(h.impl, h.started, h.results())
	if err != nil {
		fmt.Fprintln(os.Stderr, "conformance: report not written:", err)
		os.Exit(max(code, 1))
	}
	yml, md, err := rep.Write(*reportDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "conformance: report not written:", err)
		os.Exit(max(code, 1))
	}
	fmt.Printf("conformance: %s %s passed %d failed %d, report %s and %s\n", rep.Implementation.Name, rep.Implementation.Version, rep.Summary.Passed, rep.Summary.Failed, yml, md)
	os.Exit(code)
}

// Clients of one implementation, the run id naming every resource, and the results so far
type harness struct {
	gw      *http.Client
	gwURL   string
	api     nebuv1connect.GatewayServiceClient
	token   string
	host    string
	run     string
	impl    Implementation
	started time.Time
	prompts atomic.Int64

	mu   sync.Mutex
	done []FeatureResult
}

// Checks the flags, builds clients trusting the CA, and reads the implementation name and version
func dial() (*harness, error) {
	switch {
	case *gatewayURL == "":
		return nil, errors.New("-gateway-url is required")
	case *apiURL == "":
		return nil, errors.New("-api-url is required")
	case *reportDir == "":
		return nil, errors.New("-report is required")
	case *stubHost == "":
		return nil, errors.New("-stub-host must name an address")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("system roots: %w", err)
	}
	if *tlsCA != "" {
		pem, err := os.ReadFile(*tlsCA)
		if err != nil {
			return nil, fmt.Errorf("-tls-ca: %w", err)
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("-tls-ca %s holds no PEM certificate", *tlsCA)
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.ForceAttemptHTTP2 = true
	tr.MaxIdleConnsPerHost = 64
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	authed := &http.Client{Transport: bearer{base: tr, token: *token}, Timeout: 30 * time.Second}
	var id [3]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	h := &harness{
		gw:      authed,
		gwURL:   strings.TrimRight(*gatewayURL, "/"),
		api:     nebuv1connect.NewGatewayServiceClient(authed, strings.TrimRight(*apiURL, "/")),
		token:   *token,
		host:    *stubHost,
		run:     hex.EncodeToString(id[:]),
		started: time.Now(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := h.api.GetGatewayStatus(ctx, connect.NewRequest(&v1.GetGatewayStatusRequest{}))
	if err != nil {
		return nil, fmt.Errorf("gateway status: %w", err)
	}
	h.impl = Implementation{Name: st.Msg.GetStatus().GetImplementation(), Version: st.Msg.GetStatus().GetVersion()}
	if h.impl.Name == "" || h.impl.Version == "" {
		return nil, fmt.Errorf("gateway status names implementation %q version %q, both are required", h.impl.Name, h.impl.Version)
	}
	return h, nil
}

// Adds the bearer token to every request
type bearer struct {
	base  http.RoundTripper
	token string
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.token == "" {
		return b.base.RoundTrip(r)
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func (h *harness) results() []FeatureResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]FeatureResult(nil), h.done...)
}

// Records the checks of one feature and its outcome once the test and its cleanups finish
type recorder struct {
	t     *testing.T
	h     *harness
	name  Feature
	start time.Time

	mu      sync.Mutex
	checks  []Check
	routes  []string
	pools   []string
	configs []string
}

// A recorder for a feature, its result stored when the test ends
func (h *harness) feature(t *testing.T, name Feature) *recorder {
	desc := ""
	for _, f := range Features {
		if f.Name == name {
			desc = f.Description
		}
	}
	if desc == "" {
		t.Fatalf("feature %s is not part of the suite", name)
	}
	r := &recorder{t: t, h: h, name: name, start: time.Now()}
	t.Cleanup(func() {
		out := OutcomePassed
		if t.Failed() {
			out = OutcomeFailed
		}
		r.mu.Lock()
		res := FeatureResult{Name: name, Description: desc, Outcome: out, DurationMS: time.Since(r.start).Milliseconds(), Checks: r.checks}
		r.mu.Unlock()
		h.mu.Lock()
		h.done = append(h.done, res)
		h.mu.Unlock()
	})
	t.Cleanup(r.teardown)
	return r
}

// Records a check, failing the test when it did not pass
func (r *recorder) check(name string, ok bool, format string, args ...any) bool {
	detail := fmt.Sprintf(format, args...)
	r.mu.Lock()
	r.checks = append(r.checks, Check{Name: name, Passed: ok, Detail: detail})
	r.mu.Unlock()
	if !ok {
		r.t.Errorf("%s: %s", name, detail)
	}
	return ok
}

// Records a setup step, ending the test when it failed
func (r *recorder) must(name string, err error) {
	r.t.Helper()
	if err != nil {
		r.check(name, false, "%v", err)
		r.t.FailNow()
	}
}

// A unique resource name for this run
func (h *harness) name(parts ...string) string {
	return "conformance-" + h.run + "-" + strings.ToLower(strings.Join(parts, "-"))
}

// A stub model server on its own listener, counting the inference requests it receives by protocol
type backend struct {
	id   string
	addr string
	port uint32
	stub *stub.Stub

	mu     sync.Mutex
	chats  int
	protos map[string]int
}

// Starts a stub serving model on the stub host, speaking HTTP/1.1 alone or also cleartext HTTP/2
func (h *harness) backend(t *testing.T, id, model string, h2c bool) *backend {
	t.Helper()
	s, err := stub.New(stub.Config{ID: id, Model: model})
	if err != nil {
		t.Fatalf("stub %s: %v", id, err)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(h.host, "0"))
	if err != nil {
		s.Close()
		t.Fatalf("stub %s listener: %v", id, err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	b := &backend{id: id, addr: net.JoinHostPort(h.host, strconv.Itoa(port)), port: uint32(port), stub: s, protos: map[string]int{}}
	protos := new(http.Protocols)
	protos.SetHTTP1(true)
	protos.SetUnencryptedHTTP2(h2c)
	srv := &http.Server{Handler: b, Protocols: protos, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	t.Cleanup(func() {
		srv.Close()
		s.Close()
	})
	return b
}

// Counts inference requests by protocol before the stub answers them
func (b *backend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/") {
		b.mu.Lock()
		b.chats++
		b.protos[r.Proto]++
		b.mu.Unlock()
	}
	b.stub.ServeHTTP(w, r)
}

// Inference requests received so far
func (b *backend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.chats
}

// Inference requests received so far by protocol, such as HTTP/1.1 or HTTP/2.0
func (b *backend) byProto() map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]int, len(b.protos))
	for k, v := range b.protos {
		out[k] = v
	}
	return out
}

// A loopback address nothing listens on
func (h *harness) deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(h.host, "0"))
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// What the gateway answered one request with
type answer struct {
	status int
	served string
	stubID string
	body   string
}

func (a answer) String() string {
	body := a.body
	if len(body) > 160 {
		body = body[:160] + "..."
	}
	return fmt.Sprintf("status %d served %q stub %q body %q", a.status, a.served, a.stubID, body)
}

// Sends one chat completion for model with extra headers, reading the whole answer
func (h *harness) chat(ctx context.Context, model string, header http.Header, stream bool) (answer, error) {
	n := h.prompts.Add(1)
	body, err := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": fmt.Sprintf("conformance run %s prompt %d", h.run, n)}},
		"max_tokens": 4,
		"stream":     stream,
	})
	if err != nil {
		return answer{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.gwURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return answer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := h.gw.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return answer{}, fmt.Errorf("reading the answer: %w", err)
	}
	return answer{status: resp.StatusCode, served: resp.Header.Get(headerServed), stubID: resp.Header.Get(headerStubID), body: string(raw)}, nil
}

// Sends one request at a time and names the backend whose count rose by one while every other stayed put
func (h *harness) attributed(ctx context.Context, model string, header http.Header, stream bool, among []*backend) (answer, *backend, error) {
	before := make([]int, len(among))
	for i, b := range among {
		before[i] = b.count()
	}
	a, err := h.chat(ctx, model, header, stream)
	if err != nil {
		return a, nil, err
	}
	var hit *backend
	var rises []string
	total := 0
	for i, b := range among {
		if d := b.count() - before[i]; d > 0 {
			rises = append(rises, fmt.Sprintf("%s+%d", b.id, d))
			hit, total = b, total+d
		}
	}
	if total > 1 {
		return a, nil, fmt.Errorf("one request reached the stubs %v, %s", rises, a)
	}
	return a, hit, nil
}

// Sends n requests over workers at once, returning every answer
func (h *harness) burst(ctx context.Context, model string, header http.Header, n, workers int) ([]answer, error) {
	out := make([]answer, n)
	errs := make([]error, n)
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				out[i], errs[i] = h.chat(ctx, model, header, false)
			}
		})
	}
	wg.Wait()
	return out, errors.Join(errs...)
}

// Retries a request until its status is want, returning the last answer and how many tries it took
func (h *harness) until(ctx context.Context, model string, header http.Header, want int) (answer, int, error) {
	deadline := time.Now().Add(settle)
	tries := 0
	for {
		tries++
		a, err := h.chat(ctx, model, header, false)
		if err == nil && a.status == want {
			return a, tries, nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return a, tries, err
			}
			return a, tries, fmt.Errorf("no answer with status %d after %d tries over %s, last %s", want, tries, settle, a)
		}
		time.Sleep(poll)
	}
}

// Polls fetch until accept passes, returning the last detail on timeout
func eventually[T any](ctx context.Context, fetch func(context.Context) (T, error), accept func(T) (bool, string)) (T, error) {
	deadline := time.Now().Add(settle)
	for {
		v, err := fetch(ctx)
		detail := ""
		if err == nil {
			ok, d := accept(v)
			if ok {
				return v, nil
			}
			detail = d
		} else {
			detail = err.Error()
		}
		if time.Now().After(deadline) {
			return v, fmt.Errorf("not settled after %s, %s", settle, detail)
		}
		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Creates a pool removed when the test ends
func (r *recorder) putPool(ctx context.Context, id string, spec *v1.PoolSpec) (*v1.Pool, error) {
	resp, err := r.h.api.PutPool(ctx, connect.NewRequest(&v1.PutPoolRequest{Id: id, Spec: spec}))
	if err != nil {
		return nil, fmt.Errorf("put pool %s: %w", id, err)
	}
	r.own(&r.pools, id)
	return resp.Msg.GetPool(), nil
}

// Creates a route removed when the test ends
func (r *recorder) putRoute(ctx context.Context, name string, spec *v1.RouteSpec) (*v1.Route, error) {
	resp, err := r.h.api.PutRoute(ctx, connect.NewRequest(&v1.PutRouteRequest{Name: name, Spec: spec}))
	if err != nil {
		return nil, fmt.Errorf("put route %s: %w", name, err)
	}
	r.own(&r.routes, name)
	return resp.Msg.GetRoute(), nil
}

// Creates a scheduler config removed when the test ends
func (r *recorder) putConfig(ctx context.Context, name, yaml string) (*v1.SchedulerConfig, error) {
	resp, err := r.h.api.PutSchedulerConfig(ctx, connect.NewRequest(&v1.PutSchedulerConfigRequest{Config: &v1.SchedulerConfig{Name: name, Spec: &v1.SchedulerConfigSpec{Yaml: yaml}}}))
	if err != nil {
		return nil, fmt.Errorf("put scheduler config %s: %w", name, err)
	}
	r.own(&r.configs, name)
	return resp.Msg.GetConfig(), nil
}

func (r *recorder) own(list *[]string, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(*list, name) {
		*list = append(*list, name)
	}
}

// Removes routes, then pools, then scheduler configs, one already removed counting as removed
func (r *recorder) teardown() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	api := r.h.api
	r.mu.Lock()
	routes, pools, configs := r.routes, r.pools, r.configs
	r.mu.Unlock()
	gone := func(what string, err error) {
		if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
			r.check("remove "+what, false, "%v", err)
		}
	}
	for _, n := range routes {
		_, err := api.DeleteRoute(ctx, connect.NewRequest(&v1.DeleteRouteRequest{Name: n}))
		gone("route "+n, err)
	}
	for _, id := range pools {
		_, err := api.DeletePool(ctx, connect.NewRequest(&v1.DeletePoolRequest{Id: id}))
		gone("pool "+id, err)
	}
	for _, n := range configs {
		_, err := api.DeleteSchedulerConfig(ctx, connect.NewRequest(&v1.DeleteSchedulerConfigRequest{Name: n}))
		gone("scheduler config "+n, err)
	}
}

func (h *harness) deletePool(ctx context.Context, id string) error {
	_, err := h.api.DeletePool(ctx, connect.NewRequest(&v1.DeletePoolRequest{Id: id}))
	return err
}

func (h *harness) pool(ctx context.Context, id string) (*v1.Pool, error) {
	resp, err := h.api.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: id}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetPool(), nil
}

func (h *harness) route(ctx context.Context, name string) (*v1.Route, error) {
	resp, err := h.api.GetRoute(ctx, connect.NewRequest(&v1.GetRouteRequest{Name: name}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetRoute(), nil
}

// Waits until the pool reports want ready endpoints and a Ready condition that is True
func (h *harness) poolReady(ctx context.Context, id string, want int) (*v1.Pool, error) {
	return eventually(ctx, func(ctx context.Context) (*v1.Pool, error) { return h.pool(ctx, id) }, func(p *v1.Pool) (bool, string) {
		c := condition(p.GetStatus().GetConditions(), condReady)
		ok := int(p.GetStatus().GetReadyEndpoints()) == want && isTrue(c)
		return ok, fmt.Sprintf("pool %s has %d of %d endpoints ready, Ready %s", id, p.GetStatus().GetReadyEndpoints(), p.GetStatus().GetTotalEndpoints(), describe(c))
	})
}

// Waits until the route reports Accepted, ResolvedRefs and Ready all True
func (h *harness) routeReady(ctx context.Context, name string) (*v1.Route, error) {
	return eventually(ctx, func(ctx context.Context) (*v1.Route, error) { return h.route(ctx, name) }, func(r *v1.Route) (bool, string) {
		conds := r.GetStatus().GetConditions()
		a, rr, rd := condition(conds, condAccepted), condition(conds, condResolvedRefs), condition(conds, condReady)
		return isTrue(a) && isTrue(rr) && isTrue(rd), fmt.Sprintf("route %s Accepted %s ResolvedRefs %s Ready %s", name, describe(a), describe(rr), describe(rd))
	})
}

// The condition of a type, nil when absent
func condition(conds []*v1.Condition, typ string) *v1.Condition {
	for _, c := range conds {
		if c.GetType() == typ {
			return c
		}
	}
	return nil
}

func isTrue(c *v1.Condition) bool {
	return c != nil && c.GetStatus() == v1.ConditionStatus_CONDITION_STATUS_TRUE
}

func isFalse(c *v1.Condition, reason string) bool {
	return c != nil && c.GetStatus() == v1.ConditionStatus_CONDITION_STATUS_FALSE && c.GetReason() == reason
}

// A condition as status slash reason with its message, or absent
func describe(c *v1.Condition) string {
	if c == nil {
		return "absent"
	}
	return fmt.Sprintf("%s/%s (%s)", strings.TrimPrefix(c.GetStatus().String(), "CONDITION_STATUS_"), c.GetReason(), c.GetMessage())
}

// A route sending every request naming model to the weighted pool targets
func routeTo(model string, targets ...*v1.RewriteTarget) *v1.RouteSpec {
	return &v1.RouteSpec{Rules: []*v1.RewriteRule{{Match: model, Targets: targets}}}
}

// A pool spec over static vLLM endpoints at the backends, every backend port a target port
func staticPool(model string, backends ...*backend) *v1.PoolSpec {
	spec := &v1.PoolSpec{Model: model, RuntimeId: stubRuntime}
	for i, b := range backends {
		spec.TargetPorts = append(spec.TargetPorts, b.port)
		spec.StaticEndpoints = append(spec.StaticEndpoints, &v1.StaticEndpoint{Address: b.addr, Rank: uint32(i)})
	}
	return spec
}

// A subset header naming the addresses
func subset(addrs ...string) http.Header {
	return http.Header{headerSubset: {strings.Join(addrs, ",")}}
}
