package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"sync"
	"syscall"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	// Carries the id of the node a forwarded request entered the mesh at, so the conductor breaks
	// seat ties by that node's links and admits the member session that brought it
	entryHeader = "X-Nebu-Entry"
	// Seats a request may pass through: the one picked and, when it refuses or fails, the next
	seatsPerRequest = 2
)

var (
	// Returned when every seat of a replicas route has its allowed requests in flight
	errSeatsBusy = errors.New("every seat has its allowed requests in flight")
	// Returned when a replicas route has no seat ready to take a request
	errNoSeat = errors.New("no seat is ready")
)

// Connections to other members, for routes conducted elsewhere and the relay's slot moves
type Peers interface {
	// This node's id
	Self() string
	// A member's gateway base and a client carrying the session credential
	Dial(ctx context.Context, nodeID string) (base string, client *http.Client, err error)
	// How far two nodes are apart by the class of the link between them, nearer first
	LinkBetween(from, to string) int
	// Moves a relay's saved slot file to a node from another
	MoveSlotTo(ctx context.Context, formationID, toNode, fromNode, file string) error
	// Removes a relay's slot file from a node
	DropSlotOn(ctx context.Context, nodeID, formationID, file string) error
}

// Installs the mesh, so forwarded routes and relays reach other members
func (g *Gateway) SetPeers(p Peers) { g.peers = p }

// Which seat a conversation's opening messages last went to
type affinityTable struct {
	mu      sync.Mutex
	entries map[string]affinityEntry
}

type affinityEntry struct {
	seat string
	at   time.Time
}

func newAffinity() *affinityTable { return &affinityTable{entries: map[string]affinityEntry{}} }

// The seat a prefix was sent to within the window, empty when none
func (a *affinityTable) get(key string, window time.Duration) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.entries[key]
	if !ok || time.Since(e.at) > window {
		delete(a.entries, key)
		return ""
	}
	return e.seat
}

func (a *affinityTable) set(key, seat string, window time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries[key] = affinityEntry{seat: seat, at: time.Now()}
	if len(a.entries) > 4096 {
		for k, e := range a.entries {
			if time.Since(e.at) > window {
				delete(a.entries, k)
			}
		}
	}
}

// The hash of a conversation's prefix under the route's policy: the system message and the
// first user message, whichever the policy covers, empty when the policy covers nothing
func prefixKey(route string, chat *Chat, policy *v1.AffinityPolicy) string {
	if chat == nil || policy == nil || !policy.GetSystemMessage() && !policy.GetFirstUserMessage() {
		return ""
	}
	var system, user string
	for _, m := range chat.Messages {
		text := ""
		for _, p := range m.Parts {
			if p.Type == "text" {
				text += p.Text
			}
		}
		switch {
		case m.Role == "system" && system == "":
			system = text
		case m.Role == "user" && user == "":
			user = text
		}
		if system != "" && user != "" {
			break
		}
	}
	if !policy.GetSystemMessage() {
		system = ""
	}
	if !policy.GetFirstUserMessage() {
		user = ""
	}
	if system == "" && user == "" {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(route))
	h.Write([]byte(system))
	h.Write([]byte{0})
	h.Write([]byte(user))
	return hex.EncodeToString(h.Sum(nil))
}

// A seat a request may go to: its endpoint and the instance counted for it, empty for a solo
// instance whose route counter already counts it
type target struct {
	url  *url.URL
	seat string
}

// Whether the route's requests are counted on the seats that serve them rather than on one head
func servedBySeats(r *v1.Route) bool {
	return r.GetShape() == v1.Shape_SHAPE_REPLICAS || r.GetShape() == v1.Shape_SHAPE_RELAY
}

// The seats a request may go to in order, and the key its conversation is pinned by: one target
// for a route one server answers, and for replicas the seat whose cache holds the conversation's
// prefix, then the least loaded under its cap, ties to the best link from the entry node, two at
// most so a seat that refuses or fails passes the request on once
func (g *Gateway) targets(route *v1.Route, path string, body []byte, client Flavor, entry string, cap uint32) ([]target, string, error) {
	if route.GetShape() != v1.Shape_SHAPE_REPLICAS {
		u, err := url.Parse(route.GetEndpoint())
		if err != nil {
			return nil, "", fmt.Errorf("the endpoint of %s cannot be parsed: %w", route.GetName(), err)
		}
		seat := ""
		if route.GetFormationId() != "" {
			seat = route.GetInstanceId()
		}
		return []target{{url: u, seat: seat}}, "", nil
	}
	policy := route.GetAffinity()
	if policy == nil {
		return nil, "", fmt.Errorf("%s serves replicas, and its plan carries no affinity policy to pick seats by", route.GetName())
	}
	var ready []*v1.RouteSeat
	full := false
	for _, s := range route.GetSeats() {
		if s.GetState() != v1.InstanceState_INSTANCE_STATE_READY || s.GetEndpoint() == "" {
			continue
		}
		if cap > 0 && g.table.SeatInFlight(s.GetInstanceId()) >= int(cap) {
			full = true
			continue
		}
		ready = append(ready, s)
	}
	if len(ready) == 0 {
		if full {
			return nil, "", errSeatsBusy
		}
		return nil, "", errNoSeat
	}
	key := ""
	if chat, err := client.ParseRequest(path, body); err == nil {
		key = prefixKey(route.GetName(), chat, policy)
	}
	sort.SliceStable(ready, func(i, j int) bool {
		a, b := ready[i], ready[j]
		la, lb := g.table.SeatInFlight(a.GetInstanceId()), g.table.SeatInFlight(b.GetInstanceId())
		if la != lb {
			return la < lb
		}
		ra, rb := g.linkBetween(entry, a.GetNodeId()), g.linkBetween(entry, b.GetNodeId())
		if ra != rb {
			return ra < rb
		}
		return a.GetRank() < b.GetRank()
	})
	if key != "" {
		if held := g.affinity.get(key, affinityWindow(policy)); held != "" {
			for i, s := range ready {
				if s.GetInstanceId() == held && i > 0 {
					ready = append([]*v1.RouteSeat{s}, append(ready[:i:i], ready[i+1:]...)...)
					break
				}
			}
		}
	}
	var out []target
	for _, s := range ready {
		u, err := url.Parse(s.GetEndpoint())
		if err != nil {
			g.log.Warn("seat endpoint cannot be parsed, seat skipped", "model", route.GetName(), "seat", s.GetInstanceId(), "err", err)
			continue
		}
		out = append(out, target{url: u, seat: s.GetInstanceId()})
		if len(out) == seatsPerRequest {
			break
		}
	}
	if len(out) == 0 {
		return nil, "", fmt.Errorf("no seat of %s has an endpoint the gateway can parse", route.GetName())
	}
	return out, key, nil
}

// How long a prefix stays with its seat under the policy
func affinityWindow(p *v1.AffinityPolicy) time.Duration {
	return time.Duration(p.GetWindowSeconds()) * time.Second
}

// Records the seat that served a request on its trace, and pins the conversation's prefix to it
func (g *Gateway) served(t *v1.Trace, route *v1.Route, targets []target, used int, key string) {
	if used < 0 || used >= len(targets) {
		return
	}
	if seat := targets[used].seat; seat != "" {
		t.Seats = []string{seat}
		if key != "" {
			g.affinity.set(key, seat, affinityWindow(route.GetAffinity()))
		}
	}
}

// How far a node is from another by their link, both this node when unset
func (g *Gateway) linkBetween(from, to string) int {
	if g.peers == nil {
		return 0
	}
	if from == "" {
		from = g.peers.Self()
	}
	if to == "" {
		to = g.peers.Self()
	}
	return g.peers.LinkBetween(from, to)
}

// Counts a request on the seat it is with, moving the count when the request passes to another
// seat, and nothing for a route whose own counter already counts its server
type seatHold struct {
	table   *Table
	targets []target
	release func()
}

// A hold over the targets of a route, counting on seats when the route is served by them
func (g *Gateway) holder(route *v1.Route, targets []target) *seatHold {
	h := &seatHold{targets: targets}
	if servedBySeats(route) {
		h.table = g.table
	}
	return h
}

// Moves the count to the target at i
func (h *seatHold) to(i int) {
	if h == nil || h.table == nil || i < 0 || i >= len(h.targets) || h.targets[i].seat == "" {
		return
	}
	if h.release != nil {
		h.release()
	}
	h.release = h.table.HoldSeat(h.targets[i].seat)
}

// Releases the count
func (h *seatHold) done() {
	if h != nil && h.release != nil {
		h.release()
		h.release = nil
	}
}

// Whether a failed exchange may go to another seat: the connection never opened or dropped
// before an answer, so nothing reached the client
func retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.ErrUnexpectedEOF)
}

// Whether a seat's answer refuses the request rather than answering it: over its limits, or
// failing on its side, before anything of an answer was sent
func refused(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// Sends each request to the first target that takes it: a target that refuses or fails before
// answering passes the request to the next, once, and the count follows the request
type failover struct {
	targets []target
	hold    *seatHold
	next    http.RoundTripper
	log     *slog.Logger
	used    int
}

func (f *failover) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil && len(f.targets) > 1 {
		raw, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = raw
	}
	for i, tg := range f.targets {
		f.used = i
		f.hold.to(i)
		attempt := req.Clone(req.Context())
		attempt.URL.Scheme, attempt.URL.Host, attempt.Host = tg.url.Scheme, tg.url.Host, tg.url.Host
		if body != nil {
			attempt.Body = io.NopCloser(bytes.NewReader(body))
			attempt.ContentLength = int64(len(body))
		}
		resp, err := f.next.RoundTrip(attempt)
		last := i == len(f.targets)-1
		switch {
		case err != nil && !last && retryable(err):
			f.log.Warn("seat did not answer, passing the request to the next", "seat", tg.url.Host, "err", err)
			continue
		case err != nil:
			return nil, err
		case !last && refused(resp.StatusCode):
			io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
			resp.Body.Close()
			f.log.Warn("seat refused, passing the request to the next", "seat", tg.url.Host, "status", resp.StatusCode)
			continue
		}
		return resp, nil
	}
	return nil, errors.New("no seat to send to")
}

// Sends a request for a route another member conducts to that member's gateway, under the mesh
// session, and records the conductor's trace id. The entry node's id goes with it, so the
// conductor breaks seat ties by this node's links. A video job the conductor accepted is
// remembered by its id, so polling and cancelling it from here reach the conductor
func (g *Gateway) forward(w *traceWriter, r *http.Request, body []byte, name string, route *v1.Route, policy *v1.Policy, client Flavor) {
	t := w.t
	t.Forwarded, t.NodeId = true, route.GetNodeId()
	if g.peers == nil {
		g.refuse(w, client, http.StatusServiceUnavailable, "model "+name+" is served by another node and this node belongs to no mesh", "model_unavailable")
		return
	}
	base, hc, err := g.peers.Dial(r.Context(), route.GetNodeId())
	if err != nil {
		g.refuse(w, client, http.StatusBadGateway, "the node serving "+name+" cannot be reached: "+err.Error(), "server_error")
		return
	}
	target, err := url.Parse(base)
	if err != nil {
		g.refuse(w, client, http.StatusBadGateway, err.Error(), "server_error")
		return
	}
	// The conductor answers in the client's own protocol, so its answer is parsed for the trace.
	upstream := flavorOf(clientFlavor(r))
	if t.Kind != v1.TraceKind_TRACE_KIND_OTHER {
		if chat, perr := upstream.ParseRequest(r.URL.Path, body); perr == nil {
			reader := readPassthrough(t, upstream, chat)
			w.tee = reader.pw
			defer reader.close()
		}
	}
	ctx := r.Context()
	if d := policy.GetRequestTimeoutMs(); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(d)*time.Millisecond)
		defer cancel()
	}
	self := g.peers.Self()
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = r.URL.Path
			pr.Out.URL.RawPath = r.URL.RawPath
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Host = target.Host
			// The client's credential stays here; the session credential goes with the request.
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("X-Api-Key")
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Set(modelHeader, name)
			pr.Out.Header.Set(entryHeader, self)
		},
		Transport: hc.Transport,
		ModifyResponse: func(resp *http.Response) error {
			t.ConductorTrace = resp.Header.Get(traceHeader)
			resp.Header.Del(traceHeader)
			if t.Kind == v1.TraceKind_TRACE_KIND_VIDEO && r.Method == http.MethodPost && resp.StatusCode < http.StatusMultipleChoices {
				return g.jobs.remember(resp, route.GetNodeId())
			}
			return nil
		},
		FlushInterval: -1,
		ErrorHandler:  func(w http.ResponseWriter, _ *http.Request, err error) { g.upstreamError(w, t, client, name, err) },
	}
	r = r.WithContext(ctx)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	rp.ServeHTTP(w, r)
}

// Video jobs other members' gateways accepted for requests forwarded from here, by job id, so
// their polling, download, and cancel reach the member that runs them
type forwardedJobs struct {
	mu   sync.Mutex
	byID map[string]forwardedJob
}

type forwardedJob struct {
	node string
	at   time.Time
}

func newForwardedJobs() *forwardedJobs { return &forwardedJobs{byID: map[string]forwardedJob{}} }

// Reads the job id out of the conductor's answer and keeps the conductor for it, handing the
// answer back whole
func (j *forwardedJobs) remember(resp *http.Response, node string) error {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	resp.Body.Close()
	if err != nil {
		return err
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &job); err != nil {
		return fmt.Errorf("the conductor accepted a video job with an answer that is not JSON: %w", err)
	}
	if job.ID == "" {
		return errors.New("the conductor accepted a video job without naming it")
	}
	j.put(job.ID, node)
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	resp.Header.Set("Content-Length", fmt.Sprint(len(raw)))
	return nil
}

func (j *forwardedJobs) put(id, node string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for k, v := range j.byID {
		if time.Since(v.at) > videoKeep {
			delete(j.byID, k)
		}
	}
	j.byID[id] = forwardedJob{node: node, at: time.Now()}
}

// The node running a forwarded job, false when the job was not forwarded from here
func (j *forwardedJobs) node(id string) (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	v, ok := j.byID[id]
	if !ok || time.Since(v.at) > videoKeep {
		delete(j.byID, id)
		return "", false
	}
	return v.node, true
}

func (j *forwardedJobs) forget(id string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.byID, id)
}

// Sends a video job's poll, download, or cancel to the member running it
func (g *Gateway) forwardVideo(w http.ResponseWriter, r *http.Request, client Flavor, id, node string) {
	if g.peers == nil {
		client.Error(w, http.StatusServiceUnavailable, "video "+id+" runs on another node and this node belongs to no mesh", "model_unavailable")
		return
	}
	base, hc, err := g.peers.Dial(r.Context(), node)
	if err != nil {
		client.Error(w, http.StatusBadGateway, "the node running video "+id+" cannot be reached: "+err.Error(), "server_error")
		return
	}
	target, err := url.Parse(base)
	if err != nil {
		client.Error(w, http.StatusBadGateway, err.Error(), "server_error")
		return
	}
	self := g.peers.Self()
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = r.URL.Path
			pr.Out.URL.RawPath = r.URL.RawPath
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("X-Api-Key")
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Set(entryHeader, self)
		},
		Transport: hc.Transport,
		ModifyResponse: func(resp *http.Response) error {
			if r.Method == http.MethodDelete && resp.StatusCode < http.StatusMultipleChoices {
				g.jobs.forget(id)
			}
			return nil
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			g.log.Warn("gateway video forward", "video", id, "node", node, "err", err)
			client.Error(w, http.StatusBadGateway, "the node running video "+id+" did not answer: "+err.Error(), "server_error")
		},
	}
	rp.ServeHTTP(w, r)
}
