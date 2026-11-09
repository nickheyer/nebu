package gateway

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/mesh"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/telemetry"
)

// Sends a request whose route another node serves to that node's gateway under the mesh session
func (x *exchange) forward(ctx context.Context, in *requestcontrol.Inbound) {
	g := x.g
	node := x.route.GetNodeId()
	x.t.Forwarded, x.t.NodeId = true, node
	req := &framework.Request{ID: x.id, FairnessID: cmp.Or(strings.TrimSpace(in.Headers.Get(requestcontrol.HeaderFairnessID)), in.Identity, "anonymous"), Headers: map[string]string{}}
	if p, err := strconv.Atoi(strings.TrimSpace(in.Headers.Get(requestcontrol.HeaderObjective))); err == nil {
		req.Objective.Priority = p
	}
	if err := g.preRequest(ctx, in, req); err != nil {
		x.hold.release()
		x.fail(err)
		return
	}
	defer x.hold.release()
	if g.peers == nil {
		x.refuse(chat.Failure{Status: http.StatusServiceUnavailable, Code: chat.CodeModelUnavailable, Message: "model " + x.model + " is served by another node and this node belongs to no mesh", RetryAfter: g.cfg.Retry.Unavailable})
		return
	}
	resp, err := x.toNode(ctx, node, req.FairnessID, x.body)
	if err != nil {
		x.refuse(upstreamFailure(x.model, fmt.Errorf("the node serving %s: %w", x.model, err)))
		return
	}
	defer resp.Body.Close()
	x.t.ConductorTrace = resp.Header.Get(TraceHeader)
	x.t.Endpoint = resp.Header.Get(requestcontrol.HeaderServed)
	x.resp.FirstByteAt = g.clock.Now()
	x.w.Header().Set(requestcontrol.HeaderRequestID, x.requestID())
	if x.traced {
		x.w.Header().Set(TraceHeader, x.t.GetId())
	}
	if x.kind == chat.Video && x.r.Method == http.MethodPost && resp.StatusCode < http.StatusMultipleChoices {
		if err := g.jobs.remember(resp, node, x.who.owner); err != nil {
			x.refuse(chat.Failure{Status: http.StatusBadGateway, Code: chat.CodeUpstream, Message: err.Error()})
			return
		}
	}
	var creq *chat.Request
	if x.kind != chat.None && x.kind != chat.Image && x.kind != chat.Video {
		creq, _ = x.chatRequest()
	}
	x.relayAnswer(ctx, resp, chat.RuntimeOf(x.api), creq)
}

// Opens the request on a member's gateway with the hop count, entry node, owner and fairness id it carries on
func (x *exchange) toNode(ctx context.Context, node, fairness string, body []byte) (*http.Response, error) {
	g := x.g
	base, hc, err := g.peers.Dial(ctx, node)
	if err != nil {
		return nil, err
	}
	target, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	target.Path, target.RawPath, target.RawQuery = x.r.URL.Path, x.r.URL.RawPath, x.r.URL.RawQuery
	req, err := http.NewRequestWithContext(ctx, x.r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = x.r.Header.Clone()
	for _, k := range []string{"Authorization", "X-Api-Key", "Cookie", "Content-Length", "Connection", "Accept-Encoding"} {
		req.Header.Del(k)
	}
	req.Header.Set(requestcontrol.HeaderModelName, servedName(x.route))
	req.Header.Set(EntryHeader, cmp.Or(x.entry, g.peers.Self()))
	req.Header.Set(OwnerHeader, x.who.owner)
	req.Header.Set(requestcontrol.HeaderFairnessID, fairness)
	req.Header.Set(mesh.HopHeader, strconv.Itoa(x.hops))
	req.Header.Set(requestcontrol.HeaderRequestID, x.id)
	telemetry.Inject(ctx, req.Header)
	return hc.Transport.RoundTrip(req)
}

// Video jobs other members accepted for requests forwarded from here, so later calls reach them
type forwardedJobs struct {
	mu   sync.Mutex
	byID map[string]forwardedJob
}

type forwardedJob struct {
	node  string
	owner string
	at    time.Time
}

func newForwardedJobs() *forwardedJobs { return &forwardedJobs{byID: map[string]forwardedJob{}} }

// Reads the job id out of the conductor's answer and keeps the conductor for it, the answer handed back whole
func (j *forwardedJobs) remember(resp *http.Response, node, owner string) error {
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
	j.mu.Lock()
	for k, v := range j.byID {
		if time.Since(v.at) > videoKeep {
			delete(j.byID, k)
		}
	}
	j.byID[job.ID] = forwardedJob{node: node, owner: owner, at: time.Now()}
	j.mu.Unlock()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	resp.Header.Set("Content-Length", strconv.Itoa(len(raw)))
	return nil
}

// The node running a forwarded job the viewer may reach, false when it was not forwarded from here
func (j *forwardedJobs) node(id string, v Viewer) (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	f, ok := j.byID[id]
	if !ok || time.Since(f.at) > videoKeep {
		delete(j.byID, id)
		return "", false
	}
	if !v.sees(f.owner) {
		return "", false
	}
	return f.node, true
}

func (j *forwardedJobs) forget(id string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.byID, id)
}

// Sends a video job's poll, download or cancel to the member running it
func (g *Gateway) forwardVideo(w http.ResponseWriter, r *http.Request, client chat.Client, id, node string) {
	if g.peers == nil {
		client.Error(w, chat.Failure{Status: http.StatusServiceUnavailable, Code: chat.CodeModelUnavailable, Message: "video " + id + " runs on another node and this node belongs to no mesh"})
		return
	}
	base, hc, err := g.peers.Dial(r.Context(), node)
	if err != nil {
		client.Error(w, chat.Failure{Status: http.StatusBadGateway, Code: chat.CodeServer, Message: "the node running video " + id + " cannot be reached: " + err.Error()})
		return
	}
	target, err := url.Parse(base)
	if err != nil {
		client.Error(w, chat.Failure{Status: http.StatusBadGateway, Code: chat.CodeServer, Message: err.Error()})
		return
	}
	target.Path, target.RawPath, target.RawQuery = r.URL.Path, r.URL.RawPath, r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), nil)
	if err != nil {
		client.Error(w, chat.Failure{Status: http.StatusBadGateway, Code: chat.CodeServer, Message: err.Error()})
		return
	}
	req.Header.Set(EntryHeader, g.peers.Self())
	req.Header.Set(OwnerHeader, identityOf(r).owner)
	resp, err := hc.Transport.RoundTrip(req)
	if err != nil {
		client.Error(w, chat.Failure{Status: http.StatusBadGateway, Code: chat.CodeServer, Message: "the node running video " + id + " did not answer: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	if r.Method == http.MethodDelete && resp.StatusCode < http.StatusMultipleChoices {
		g.jobs.forget(id)
	}
	copyAnswerHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// The model name the route's first rule writes into requests, the route name when its target names none
func servedName(rt *v1.Route) string {
	for _, rule := range rt.GetSpec().GetRules() {
		for _, t := range rule.GetTargets() {
			if t.GetModel() != "" {
				return t.GetModel()
			}
		}
	}
	return rt.GetName()
}
