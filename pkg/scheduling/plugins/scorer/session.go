package scorer

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// Scores one for the endpoint a session header last routed to within the window, zero for every other
type SessionAffinityScorer struct {
	plugins.Named
	header string
	window time.Duration
	limit  int
	clock  registry.Clock

	mu       sync.Mutex
	sessions map[string]*list.Element
	lru      *list.List
}

type session struct {
	id       string
	endpoint datalayer.EndpointID
	at       time.Time
}

// A scorer remembering at most limit sessions, each for window after its last request
func NewSessionAffinityScorer(header string, window time.Duration, limit int, clock registry.Clock) *SessionAffinityScorer {
	return &SessionAffinityScorer{
		Named:    plugins.NewNamed(SessionAffinityScorerType),
		header:   header,
		window:   window,
		limit:    limit,
		clock:    clock,
		sessions: map[string]*list.Element{},
		lru:      list.New(),
	}
}

func (s *SessionAffinityScorer) Score(_ context.Context, _ *framework.CycleState, req *framework.Request, eps []datalayer.Endpoint) map[datalayer.EndpointID]float64 {
	out := make(map[datalayer.EndpointID]float64, len(eps))
	for _, ep := range eps {
		out[ep.ID()] = 0
	}
	sid := s.sessionID(req)
	if sid == "" {
		return out
	}
	now := s.clock.Now()
	s.mu.Lock()
	var last datalayer.EndpointID
	if e, ok := s.sessions[sid]; ok {
		if v := e.Value.(*session); now.Sub(v.at) <= s.window {
			last = v.endpoint
		} else {
			s.lru.Remove(e)
			delete(s.sessions, sid)
		}
	}
	s.mu.Unlock()
	if _, ok := out[last]; ok {
		out[last] = 1
	}
	return out
}

// Remembers the primary endpoint for the request's session as soon as the pick is made
func (s *SessionAffinityScorer) PreRequest(_ context.Context, req *framework.Request, res *framework.Result) {
	if eps := res.Endpoints(); len(eps) > 0 {
		s.record(s.sessionID(req), eps[0].ID())
	}
}

// Remembers the endpoint that actually served the session once it answered without error
func (s *SessionAffinityScorer) ResponseComplete(_ context.Context, req *framework.Request, resp *framework.Response) {
	if resp != nil && resp.Endpoint != nil && resp.Err == nil {
		s.record(s.sessionID(req), resp.Endpoint.ID())
	}
}

// Moves a session to the front with its endpoint, evicting expired and least recent sessions past the limit
func (s *SessionAffinityScorer) record(sid string, ep datalayer.EndpointID) {
	if sid == "" {
		return
	}
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.sessions[sid]; ok {
		v := e.Value.(*session)
		v.endpoint, v.at = ep, now
		s.lru.MoveToFront(e)
	} else {
		s.sessions[sid] = s.lru.PushFront(&session{id: sid, endpoint: ep, at: now})
	}
	for e := s.lru.Back(); e != nil; e = s.lru.Back() {
		v := e.Value.(*session)
		if s.lru.Len() <= s.limit && now.Sub(v.at) <= s.window {
			break
		}
		s.lru.Remove(e)
		delete(s.sessions, v.id)
	}
}

// Sessions held, for tests and debugging
func (s *SessionAffinityScorer) Sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lru.Len()
}

func (s *SessionAffinityScorer) sessionID(req *framework.Request) string {
	if v, ok := req.Headers[s.header]; ok {
		return v
	}
	for k, v := range req.Headers {
		if strings.EqualFold(k, s.header) {
			return v
		}
	}
	return ""
}
