package formations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/tasks"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// A seat is polled for readiness this often
	seatPoll = time.Second
	// A seat gets this long past its role's health timeout to report ready
	seatGrace = time.Minute
	// A relay's canary request gets this long to answer through the pair
	canaryTimeout = 2 * time.Minute
	// The transport a seat's log names when its collective or rpc server took an RDMA device
	transportRDMA = "rdma"
	// The relay trace mark the gateway writes when both seats answered
	relayBoth = "prefill+decode"
)

// Plans and launches a formation across the span
func (m *Manager) Run(ctx context.Context, run *v1.RunRequest) (*v1.Formation, *v1.Task, error) {
	if run.GetSlotId() != "" {
		if m.SlotView == nil {
			return nil, nil, fmt.Errorf("%w: slot %s: no slots are wired to the conductor", ErrFormation, run.GetSlotId())
		}
		claimed, err := m.SlotView.Claim(ctx, run.GetSlotId(), run)
		if err != nil {
			return nil, nil, err
		}
		run = claimed
	}
	p, err := m.plans(ctx, run)
	if err != nil {
		return nil, nil, err
	}
	return m.start(ctx, run, p, nil)
}

// Plans a run request with the planner installed
func (m *Manager) plans(ctx context.Context, run *v1.RunRequest) (*planned, error) {
	if m.planner != nil {
		return m.planner(ctx, run)
	}
	return m.plan(ctx, run)
}

// Launches a formation again after a failure or a restart: members that left the mesh drop out
// of the span, the planner runs on the mesh as it is, and ranks that rendezvous meet at the
// address and ports they met at before when the same nodes play the same roles
func (m *Manager) relaunch(ctx context.Context, prev *v1.Formation) (*v1.Formation, *v1.Task, error) {
	run := proto.Clone(prev.GetRequest()).(*v1.RunRequest)
	var kept, dropped []string
	for _, entry := range run.GetSpan() {
		nodeRef, _, _ := strings.Cut(entry, "/")
		rec, err := m.Mesh.Node(nodeRef)
		if err != nil || rec.GetState() == v1.NodeState_NODE_STATE_GONE {
			dropped = append(dropped, nodeRef)
			continue
		}
		kept = append(kept, entry)
	}
	run.Span = kept
	p, err := m.plans(ctx, run)
	if err != nil {
		if len(dropped) > 0 {
			return nil, nil, fmt.Errorf("%w (%s left the mesh and dropped out of the span)", err, strings.Join(dropped, ", "))
		}
		return nil, nil, err
	}
	if len(dropped) > 0 {
		p.plan.Detail = strings.TrimSuffix(p.plan.GetDetail(), ".") + fmt.Sprintf("; %s left the mesh and dropped out of the span", strings.Join(dropped, ", "))
	}
	return m.start(ctx, run, p, prev)
}

// Records a planned formation and starts its launch task
func (m *Manager) start(ctx context.Context, run *v1.RunRequest, p *planned, prev *v1.Formation) (*v1.Formation, *v1.Task, error) {
	plan := p.plan
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS && !run.GetForce() {
		return nil, nil, fmt.Errorf("%w: %s does not fit the span, %s. Pass force to run anyway", ErrFormation, p.name, plan.GetDetail())
	}
	if len(plan.GetSeats()) == 0 {
		return nil, nil, fmt.Errorf("%w: %s has no seats: %s", ErrFormation, p.name, plan.GetDetail())
	}
	if !instances.FromSwap(ctx) && (m.nameTaken(p.name) || m.liveInstance(p.name)) {
		return nil, nil, fmt.Errorf("%w: %q is already running", ErrFormation, p.name)
	}
	if r, ok := m.Routes.Lookup(p.name); ok && r.GetFormationId() == "" && r.GetSlotId() != "" && run.GetSlotId() == "" {
		return nil, nil, fmt.Errorf("%w: %q is a slot, run with the slot or pick another name", ErrFormation, p.name)
	}
	self := m.self()
	selfRec := m.Mesh.Record()
	f := &v1.Formation{
		Id:             db.NewID(),
		Name:           p.name,
		Conductor:      self,
		ConductorName:  selfRec.GetName(),
		Shape:          plan.GetShape(),
		Seats:          plan.GetSeats(),
		Plan:           plan,
		Request:        proto.Clone(run).(*v1.RunRequest),
		State:          v1.FormationState_FORMATION_STATE_STARTING,
		SlotId:         run.GetSlotId(),
		DesiredRunning: true,
		CreatedAt:      timestamppb.Now(),
		UpdatedAt:      timestamppb.Now(),
		RuntimeId:      p.rt.ID(),
		SourceId:       p.stored.GetSourceId(),
		Repo:           p.stored.GetRepo(),
		Group:          p.stored.GetGroup(),
		CacheKey:       cacheKeyOf(p.stored),
		Sequence:       1,
	}
	f.Request.Name = p.name
	f.Request.RuntimeId = p.rt.ID()
	f.Request.SourceId = p.stored.GetSourceId()
	for _, s := range f.Seats {
		s.State = v1.InstanceState_INSTANCE_STATE_STARTING
	}
	if prev != nil {
		f.Rendezvous = carryRendezvous(prev, f.Seats)
	}
	m.mu.Lock()
	m.list[f.GetId()] = f
	m.busy[f.GetId()] = true
	m.pruneLocked()
	m.mu.Unlock()
	m.persist(f)
	m.Events.Publish(v1.EventKind_EVENT_KIND_FORMATION, v1.EventAction_EVENT_ACTION_CREATED, f.GetId(), proto.Clone(f).(*v1.Formation))
	m.Mesh.Bump()
	labels := map[string]string{"formation": f.GetId(), "name": f.GetName(), "shape": shapeWord(f.GetShape())}
	task := m.Tasks.Start(kindFormation, "formation "+f.GetName(), labels, func(ctx context.Context, h *tasks.Handle) error {
		defer func() {
			m.mu.Lock()
			delete(m.busy, f.GetId())
			m.mu.Unlock()
		}()
		return m.launch(ctx, h, f.GetId(), p)
	})
	out := m.update(f.GetId(), func(r *v1.Formation) { r.TaskId = task.GetId() })
	return out, task, nil
}

// The key stages keep a model's tensor cache under: the model's store key and the digests of its
// files, the same on every relaunch, so the next start sends hashes first and skips what the
// stage has
func cacheKeyOf(s *v1.StoredModel) string {
	h := sha256.New()
	h.Write([]byte(store.Key(s.GetSourceId(), s.GetRepo(), s.GetGroup())))
	var digests []string
	for _, a := range s.GetArtifacts() {
		digests = append(digests, a.GetDigest())
	}
	sort.Strings(digests)
	for _, d := range digests {
		h.Write([]byte{0})
		h.Write([]byte(d))
	}
	if len(digests) == 0 {
		h.Write([]byte{0})
		h.Write([]byte(mesh.Summary(s).GetDescriptorDigest()))
	}
	return "model-" + hex.EncodeToString(h.Sum(nil))[:24]
}

// Carries the rendezvous address and ranks of a formation being relaunched onto the new seats
// when the same nodes play the same roles, so the collectives meet where they met before
func carryRendezvous(prev *v1.Formation, seats []*v1.Seat) string {
	if prev.GetRendezvous() == "" || len(prev.GetSeats()) != len(seats) {
		return ""
	}
	byNodeRole := map[string]*v1.Seat{}
	for _, s := range prev.GetSeats() {
		key := s.GetNodeId() + "/" + s.GetRole()
		if _, dup := byNodeRole[key]; dup {
			return ""
		}
		byNodeRole[key] = s
	}
	ranks := make([]uint32, len(seats))
	for i, s := range seats {
		p, ok := byNodeRole[s.GetNodeId()+"/"+s.GetRole()]
		if !ok {
			return ""
		}
		ranks[i] = p.GetRank()
	}
	for i, s := range seats {
		s.Rank = ranks[i]
	}
	return prev.GetRendezvous()
}

// Whether a live instance already answers to the name
func (m *Manager) liveInstance(name string) bool {
	in, err := m.Instances.Get(name)
	return err == nil && !instances.Terminal(in.GetState())
}

func shapeWord(s v1.Shape) string { return strings.ToLower(strings.TrimPrefix(s.String(), "SHAPE_")) }

func instanceWord(s v1.InstanceState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "INSTANCE_STATE_"))
}

// Instances started by a launch, by seat, so peers learn each other's addresses
type launched struct {
	mu    sync.Mutex
	byKey map[string]*v1.Instance
}

func seatKey(s *v1.Seat) string {
	return fmt.Sprintf("%s/%s/%d", s.GetNodeId(), s.GetRole(), s.GetRank())
}

func (l *launched) put(s *v1.Seat, in *v1.Instance) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byKey[seatKey(s)] = in
}

func (l *launched) get(s *v1.Seat) (*v1.Instance, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	in, ok := l.byKey[seatKey(s)]
	return in, ok
}

// Pulls weights to the seats that lack them, launches seats by phase, one progress step per
// seat, proves a relay pair with a canary, and routes the head. A canceled launch stops every
// seat it started.
func (m *Manager) launch(ctx context.Context, h *tasks.Handle, id string, p *planned) error {
	f, err := m.Get(id)
	if err != nil {
		return err
	}
	h.Logf("%s", f.GetPlan().GetDetail())
	for _, c := range f.GetPlan().GetCandidates() {
		if c.GetShape() != f.GetShape() || c.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
			h.Logf("rejected %s on %s: %s", shapeWord(c.GetShape()), strings.Join(c.GetNodeIds(), ","), c.GetReason())
		}
	}
	fail := func(err error) error {
		if m.base.Err() != nil {
			return err
		}
		h.Logf("formation failed: %v", err)
		m.Routes.RemoveFormation(id)
		cleanup, cancel := context.WithTimeout(m.base, stopTimeout)
		defer cancel()
		if current, err := m.Get(id); err == nil {
			m.stopSeats(cleanup, current, h)
		}
		m.update(id, func(r *v1.Formation) {
			if r.State != v1.FormationState_FORMATION_STATE_STOPPING && r.State != v1.FormationState_FORMATION_STATE_STOPPED {
				r.State = v1.FormationState_FORMATION_STATE_FAILED
				r.Error = err.Error()
				r.StoppedAt = timestamppb.Now()
			}
		})
		m.Mesh.Bump()
		return err
	}
	total := len(f.GetSeats())
	h.Progress(0, uint64(total), "weights")
	if err := m.weights(ctx, h, f, p); err != nil {
		return fail(err)
	}
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	phases := map[int][]*v1.Seat{}
	var order []int
	for _, s := range f.GetSeats() {
		ph := int(s.GetPhase())
		if _, ok := phases[ph]; !ok {
			order = append(order, ph)
		}
		phases[ph] = append(phases[ph], s)
	}
	sort.Ints(order)
	started := &launched{byKey: map[string]*v1.Instance{}}
	done := 0
	rendezvous := f.GetRendezvous()
	for _, ph := range order {
		seats := phases[ph]
		h.Progress(uint64(done), uint64(total), fmt.Sprintf("phase %d: %d seats", ph, len(seats)))
		if meet := m.rendezvousHead(p.rt, f, seats); meet != nil {
			in, err := m.startSeat(ctx, h, f, p, meet, rendezvous, started)
			if err != nil {
				return fail(fmt.Errorf("%s on %s: %w", meet.GetRole(), meet.GetNodeName(), err))
			}
			rendezvous = in.GetSeat().GetRendezvous()
			if rendezvous == "" {
				return fail(fmt.Errorf("%s on %s answered no rendezvous address for its ranks", meet.GetRole(), meet.GetNodeName()))
			}
			m.update(id, func(r *v1.Formation) { r.Rendezvous = rendezvous })
			h.Logf("ranks rendezvous at %s", rendezvous)
		}
		errs := make([]error, len(seats))
		var wg sync.WaitGroup
		var progress sync.Mutex
		for i, s := range seats {
			wg.Add(1)
			go func(i int, s *v1.Seat) {
				defer wg.Done()
				in, ok := started.get(s)
				if !ok {
					if in, errs[i] = m.startSeat(ctx, h, f, p, s, rendezvous, started); errs[i] != nil {
						return
					}
				}
				if _, errs[i] = m.awaitSeat(ctx, h, f, p, s, in); errs[i] == nil {
					progress.Lock()
					done++
					h.Progress(uint64(done), uint64(total), fmt.Sprintf("%s on %s ready", s.GetRole(), s.GetNodeName()))
					progress.Unlock()
				}
			}(i, s)
		}
		wg.Wait()
		var failed []string
		for i, s := range seats {
			if errs[i] != nil {
				failed = append(failed, fmt.Sprintf("%s on %s: %v", s.GetRole(), s.GetNodeName(), errs[i]))
			}
		}
		if len(failed) > 0 {
			return fail(errors.New(strings.Join(failed, "; ")))
		}
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
	}
	if m.needsCanary(p.rt, f) {
		if err := m.canary(ctx, h, id); err != nil {
			return fail(err)
		}
	}
	if err := m.route(ctx, id); err != nil {
		return fail(err)
	}
	moved := m.bytesMoved(ctx, h, id, p)
	f = m.update(id, func(r *v1.Formation) {
		if r.State == v1.FormationState_FORMATION_STATE_STARTING {
			r.State = v1.FormationState_FORMATION_STATE_READY
			r.ReadyAt = timestamppb.Now()
			r.Error = ""
		}
		r.BytesMoved = moved
	})
	m.Mesh.Bump()
	h.Progress(uint64(total), uint64(total), "ready at "+f.GetEndpoint())
	h.Logf("ready %s as %s across %d seats", f.GetName(), shapeWord(f.GetShape()), total)
	return nil
}

// The seat whose node allocates the rendezvous address for a phase of ranks: the head among
// them, rank 0, nil for a phase that does not rendezvous
func (m *Manager) rendezvousHead(rt runtimes.Runtime, f *v1.Formation, seats []*v1.Seat) *v1.Seat {
	var head, first *v1.Seat
	for _, s := range seats {
		role, err := runtimes.RoleOf(rt, &v1.SeatSpec{Shape: f.GetShape(), Role: s.GetRole()})
		if err != nil || !role.Rendezvous {
			continue
		}
		if role.Head && (head == nil || s.GetRank() < head.GetRank()) {
			head = s
		}
		if first == nil || s.GetRank() < first.GetRank() {
			first = s
		}
	}
	if head != nil {
		return head
	}
	return first
}

// Whether the shape's head role wants one completion through the relay pair before ready
func (m *Manager) needsCanary(rt runtimes.Runtime, f *v1.Formation) bool {
	head := m.headSeat(f)
	if head == nil {
		return false
	}
	role, err := runtimes.RoleOf(rt, &v1.SeatSpec{Shape: f.GetShape(), Role: head.GetRole()})
	return err == nil && role.Health.Kind == runtimes.HealthCanary
}

// Sends one short completion through the relay pair by a route of its own before the formation
// is ready, and takes the answer as proof only when the gateway's trace says both seats served
func (m *Manager) canary(ctx context.Context, h *tasks.Handle, id string) error {
	if m.Gateway == nil {
		return fmt.Errorf("%w: this node has no gateway to send the canary through", ErrFormation)
	}
	if m.Traces == nil {
		return fmt.Errorf("%w: this node records no traces, so the canary cannot be proven", ErrFormation)
	}
	f, err := m.Get(id)
	if err != nil {
		return err
	}
	in, err := m.headInstance(ctx, f)
	if err != nil {
		return err
	}
	probe := proto.Clone(f).(*v1.Formation)
	if probe.Plan == nil {
		probe.Plan = &v1.FormationPlan{}
	}
	// Every prompt takes the pair on the canary route, whatever the plan's break even.
	probe.Plan.RelayBreakEvenPrompt = 1
	name := f.GetName() + ".canary"
	m.Routes.ServeFormationAs(name, probe, in, m.Runtimes.API(f.GetRuntimeId()), routeSeats(f), nil, nil)
	defer m.Routes.Delete(name)
	h.Message("canary through the relay pair")
	body, err := json.Marshal(map[string]any{"model": name, "messages": []map[string]string{{"role": "user", "content": "Say ok."}}, "max_tokens": 1, "stream": false})
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, canaryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, m.Gateway.LocalBase()+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := m.Gateway.Client("formation canary").Do(req)
	if err != nil {
		return fmt.Errorf("canary through the relay pair: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("canary through the relay pair answered %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	traceID := resp.Header.Get(gateway.TraceHeader)
	t, ok := m.Traces.Get(traceID)
	if !ok {
		return fmt.Errorf("canary answered but the gateway kept no trace %q of it", traceID)
	}
	if t.GetRelay() != relayBoth {
		return fmt.Errorf("canary answered by the decode seat alone (%s), the relay handoff did not happen", t.GetRelay())
	}
	h.Logf("canary answered through the relay pair in %s", time.Since(start).Round(time.Millisecond))
	return nil
}

// A store reference in a run parameter, as store://source/repo#group
func parseStoreRef(ref string) (source, repo, group string, ok bool) {
	rest, isRef := strings.CutPrefix(strings.TrimSpace(ref), runtimes.StoreScheme)
	if !isRef {
		return "", "", "", false
	}
	repoPath, group, hasGroup := strings.Cut(rest, "#")
	source, repo, hasRepo := strings.Cut(repoPath, "/")
	if !hasGroup || !hasRepo || source == "" || repo == "" || group == "" {
		return "", "", "", false
	}
	return source, repo, group, true
}

// Pulls onto every seat's node the files its role reads: the model and the draft model for a
// head holding weights, and for a head holding parts every part the request's parameters name
// and every stored component the blueprint fills its slots from. Seats pull at once, one
// progress row each, a seat's files in order.
func (m *Manager) weights(ctx context.Context, h *tasks.Handle, f *v1.Formation, p *planned) error {
	type need struct {
		source, repo  string
		group, revise string
	}
	var order []*v1.Seat
	needs := map[string][]need{}
	seen := map[string]bool{}
	add := func(s *v1.Seat, source, repo, group, revision string) {
		key := seatKey(s) + "|" + store.Key(source, repo, group)
		if seen[key] {
			return
		}
		seen[key] = true
		if _, ok := needs[seatKey(s)]; !ok {
			order = append(order, s)
		}
		needs[seatKey(s)] = append(needs[seatKey(s)], need{source: source, repo: repo, group: group, revise: revision})
	}
	addStored := func(s *v1.Seat, st *v1.StoredModel) {
		add(s, st.GetSourceId(), st.GetRepo(), st.GetGroup(), st.GetRevision())
	}
	var companions []*v1.StoredModel
	for _, s := range f.GetSeats() {
		role, err := runtimes.RoleOf(p.rt, &v1.SeatSpec{Shape: f.GetShape(), Role: s.GetRole()})
		if err != nil {
			return err
		}
		switch role.Files {
		case runtimes.FilesNone:
			continue
		case runtimes.FilesParts:
			addStored(s, p.stored)
			for _, param := range p.rt.Params() {
				if param.GetType() != v1.ParamType_PARAM_TYPE_PATH {
					continue
				}
				if source, repo, group, ok := parseStoreRef(f.GetRequest().GetParams()[param.GetName()]); ok {
					add(s, source, repo, group, "")
				}
			}
			if companions == nil {
				if companions, err = m.Inspector.Companions(p.stored.GetSourceId(), p.stored.GetRepo(), p.stored.GetGroup()); err != nil {
					return err
				}
			}
			for _, c := range companions {
				if c.GetDescriptor_().GetKind() == v1.ModelKind_MODEL_KIND_COMPONENT {
					addStored(s, c)
				}
			}
		default:
			addStored(s, p.stored)
			if role.Head && f.GetShape() == v1.Shape_SHAPE_DRAFT && p.draft != nil {
				addStored(s, p.draft)
			}
		}
	}
	if len(order) == 0 {
		return nil
	}
	h.Message(fmt.Sprintf("weights on %d seats", len(order)))
	errs := make([]error, len(order))
	var wg sync.WaitGroup
	for i, s := range order {
		files := needs[seatKey(s)]
		row := fmt.Sprintf("%s%d on %s", s.GetRole(), s.GetRank(), m.nodeName(s.GetNodeId()))
		h.Row(row, 0, uint64(len(files)), "weights")
		wg.Add(1)
		go func(i int, s *v1.Seat, files []need, row string) {
			defer wg.Done()
			for j, n := range files {
				name := fmt.Sprintf("%s %s", n.repo, n.group)
				progress := func(done, total uint64, message string) {
					if total > 0 {
						message = fmt.Sprintf("%s %d%%: %s", name, done*100/total, message)
					} else {
						message = name + ": " + message
					}
					h.Row(row, uint64(j), uint64(len(files)), message)
				}
				if err := m.ensure(ctx, h, progress, s.GetNodeId(), n.source, n.repo, n.group, n.revise); err != nil {
					h.Row(row, uint64(j), uint64(len(files)), name+": "+err.Error())
					errs[i] = fmt.Errorf("%s on %s: %w", name, m.nodeName(s.GetNodeId()), err)
					return
				}
				h.Row(row, uint64(j+1), uint64(len(files)), name+" on "+m.nodeName(s.GetNodeId()))
			}
		}(i, s, files, row)
	}
	wg.Wait()
	var failed []string
	for _, err := range errs {
		if err != nil {
			failed = append(failed, err.Error())
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

func (m *Manager) nodeName(id string) string {
	if rec, err := m.Mesh.Node(id); err == nil && rec.GetName() != "" {
		return rec.GetName()
	}
	return id
}

// Makes sure a node holds a model, pulling it there when it does not, the pull's byte progress
// reported through progress
func (m *Manager) ensure(ctx context.Context, h *tasks.Handle, progress func(done, total uint64, message string), node, source, repo, group, revision string) error {
	if node == m.self() {
		if _, err := m.Store.ReadManifest(source, repo, group); err == nil {
			h.Logf("%s %s already on %s", repo, group, m.nodeName(node))
			progress(1, 1, fmt.Sprintf("%s %s already here", repo, group))
			return nil
		}
		task, err := m.Puller.Pull(ctx, &v1.PullRequest{SourceId: source, Repo: repo, Group: group, Revision: revision})
		if err != nil {
			return err
		}
		h.Logf("pulling %s %s here, task %s", repo, group, task.GetId())
		return m.Tasks.Watch(ctx, task.GetId(), func(resp *v1.WatchTaskResponse) error {
			for _, line := range resp.GetLogs() {
				h.Logf("%s: %s", m.nodeName(node), line)
			}
			t := resp.GetTask()
			if pr := t.GetProgress(); pr != nil {
				progress(pr.GetDone(), pr.GetTotal(), pr.GetMessage())
			}
			switch t.GetState() {
			case v1.TaskState_TASK_STATE_FAILED:
				return errors.New(t.GetError())
			case v1.TaskState_TASK_STATE_CANCELED:
				return errors.New("pull canceled")
			}
			return nil
		})
	}
	rec, err := m.Mesh.Node(node)
	if err != nil {
		return err
	}
	if holds(rec, source, repo, group) {
		h.Logf("%s %s already on %s", repo, group, rec.GetName())
		progress(1, 1, fmt.Sprintf("%s %s already on %s", repo, group, rec.GetName()))
		return nil
	}
	cl, err := m.Mesh.Client(ctx, node)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	resp, err := cl.Mesh.PullSeat(cctx, connect.NewRequest(&v1.PullSeatRequest{SourceId: source, Repo: repo, Revision: revision, Group: group}))
	cancel()
	if err != nil {
		return err
	}
	h.Logf("pulling %s %s onto %s, task %s there", repo, group, m.nodeName(node), resp.Msg.GetTask().GetId())
	stream, err := cl.Mesh.WatchPull(ctx, connect.NewRequest(&v1.WatchPullRequest{TaskId: resp.Msg.GetTask().GetId()}))
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
		msg := stream.Msg()
		for _, line := range msg.GetLogs() {
			h.Logf("%s: %s", m.nodeName(node), line)
		}
		if pr := msg.GetTask().GetProgress(); pr != nil {
			progress(pr.GetDone(), pr.GetTotal(), pr.GetMessage())
		}
		switch msg.GetTask().GetState() {
		case v1.TaskState_TASK_STATE_FAILED:
			return errors.New(msg.GetTask().GetError())
		case v1.TaskState_TASK_STATE_CANCELED:
			return errors.New("pull canceled on " + m.nodeName(node))
		case v1.TaskState_TASK_STATE_SUCCEEDED:
			return nil
		}
	}
	if err := stream.Err(); err != nil {
		return err
	}
	return nil
}

// Launches one seat on its node and records its instance on the seat, so peers launched after it
// learn its address
func (m *Manager) startSeat(ctx context.Context, h *tasks.Handle, f *v1.Formation, p *planned, s *v1.Seat, rendezvous string, started *launched) (*v1.Instance, error) {
	run := m.seatRequest(f, p, s, rendezvous, started)
	var in *v1.Instance
	var err error
	remote := s.GetNodeId() != m.self()
	if remote {
		var cl *mesh.Client
		cl, err = m.Mesh.Client(ctx, s.GetNodeId())
		if err == nil {
			cctx, cancel := context.WithTimeout(ctx, callTimeout)
			var resp *connect.Response[v1.RunSeatResponse]
			resp, err = cl.Mesh.RunSeat(cctx, connect.NewRequest(&v1.RunSeatRequest{Run: run}))
			cancel()
			if err == nil {
				in = resp.Msg.GetInstance()
			}
		}
	} else {
		in, _, err = m.RunSeat(ctx, m.self(), run)
	}
	if err != nil {
		m.update(f.GetId(), func(r *v1.Formation) {
			for _, rs := range r.GetSeats() {
				if sameSeat(rs, s) {
					rs.State, rs.Error = v1.InstanceState_INSTANCE_STATE_FAILED, err.Error()
				}
			}
		})
		return nil, err
	}
	started.put(s, in)
	h.Logf("%s on %s: instance %s launching", s.GetRole(), s.GetNodeName(), in.GetId())
	m.update(f.GetId(), func(r *v1.Formation) {
		for _, rs := range r.GetSeats() {
			if sameSeat(rs, s) {
				rs.InstanceId, rs.State = in.GetId(), in.GetState()
				rs.Endpoint = seatEndpoint(in, remote)
				rs.Address, rs.AuxPort = in.GetSeat().GetAddress(), in.GetSeat().GetAuxPort()
			}
		}
	})
	return in, nil
}

// Waits for a launched seat to be ready and records what its node reports: its endpoint,
// transport, exposure, and measurements, or its error and triage
func (m *Manager) awaitSeat(ctx context.Context, h *tasks.Handle, f *v1.Formation, p *planned, s *v1.Seat, in *v1.Instance) (*v1.Instance, error) {
	remote := s.GetNodeId() != m.self()
	health, err := runtimes.SeatHealth(p.rt, in.GetSeat())
	if err != nil {
		return in, err
	}
	timeout := health.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ready, err := m.waitSeat(ctx, s, in.GetId(), timeout+seatGrace)
	if err != nil {
		m.update(f.GetId(), func(r *v1.Formation) {
			for _, rs := range r.GetSeats() {
				if sameSeat(rs, s) {
					rs.State, rs.Error = v1.InstanceState_INSTANCE_STATE_FAILED, err.Error()
					if ready != nil {
						rs.State = ready.GetState()
						rs.Triage = ready.GetTriage()
						rs.Measurements = ready.GetMeasurements()
						if ready.GetError() != "" {
							rs.Error = ready.GetError()
						}
					}
				}
			}
		})
		return ready, err
	}
	m.update(f.GetId(), func(r *v1.Formation) {
		for _, rs := range r.GetSeats() {
			if sameSeat(rs, s) {
				rs.InstanceId, rs.State, rs.Error, rs.Triage = ready.GetId(), ready.GetState(), "", nil
				rs.Endpoint = seatEndpoint(ready, remote)
				rs.Transport = ready.GetTransport()
				rs.Measurements = ready.GetMeasurements()
				rs.Exposed = ready.GetSeat().GetExposed() || ready.GetTransport() == transportRDMA
				rs.Address, rs.AuxPort = ready.GetSeat().GetAddress(), ready.GetSeat().GetAuxPort()
			}
		}
	})
	h.Logf("%s on %s ready at %s", s.GetRole(), s.GetNodeName(), seatEndpoint(ready, remote))
	return ready, nil
}

func sameSeat(a, b *v1.Seat) bool {
	return a.GetNodeId() == b.GetNodeId() && a.GetRole() == b.GetRole() && a.GetRank() == b.GetRank()
}

// Where the conductor reaches a seat: its loopback endpoint on this node, its guard or exposed
// port on another
func seatEndpoint(in *v1.Instance, remote bool) string {
	seat := in.GetSeat()
	if !remote {
		return in.GetEndpoint()
	}
	scheme := "http"
	if strings.HasPrefix(in.GetEndpoint(), "tcp://") {
		scheme = "tcp"
	}
	return scheme + "://" + net.JoinHostPort(hostOf(seat.GetAddress()), strconv.Itoa(int(seat.GetPort())))
}

// The run request a seat launches with: the formation's request narrowed to one seat
func (m *Manager) seatRequest(f *v1.Formation, p *planned, s *v1.Seat, rendezvous string, started *launched) *v1.RunRequest {
	run := proto.Clone(f.GetRequest()).(*v1.RunRequest)
	run.Name = fmt.Sprintf("%s/%s%d", f.GetName(), s.GetRole(), s.GetRank())
	run.SlotId, run.Span, run.Shape = "", nil, f.GetShape()
	run.RuntimeId, run.InstallId = f.GetRuntimeId(), s.GetInstallId()
	run.SourceId, run.Repo, run.Group = f.GetSourceId(), f.GetRepo(), f.GetGroup()
	params := map[string]string{}
	for k, v := range f.GetRequest().GetParams() {
		params[k] = v
	}
	if ctxParam := p.rt.Policy().ContextParam; ctxParam != "" && f.GetPlan().GetContext() > 0 {
		params[ctxParam] = strconv.FormatUint(uint64(f.GetPlan().GetContext()), 10)
	}
	run.Params = params
	spec := &v1.SeatSpec{
		FormationId: f.GetId(),
		Shape:       f.GetShape(),
		Role:        s.GetRole(),
		Rank:        s.GetRank(),
		Count:       uint32(len(f.GetSeats())),
		Rendezvous:  rendezvous,
		LayerFrom:   s.GetLayerFrom(),
		LayerTo:     s.GetLayerTo(),
		Conductor:   m.self(),
		Name:        f.GetName(),
		Devices:     s.GetDeviceIds(),
		DeviceBytes: s.GetDeviceBytes(),
		Context:     f.GetPlan().GetContext(),
		GpuLayers:   s.GetGpuLayers(),
		Memory:      s.GetMemory(),
		CacheKey:    f.GetCacheKey(),
	}
	if f.GetShape() == v1.Shape_SHAPE_DRAFT && p.draftKey != "" {
		spec.Draft = p.draftKey
	}
	for _, other := range f.GetSeats() {
		if sameSeat(other, s) {
			continue
		}
		peer := &v1.SeatPeer{NodeId: other.GetNodeId(), Role: other.GetRole(), Rank: other.GetRank(), Devices: uint32(len(other.GetDeviceIds())), LayerFrom: other.GetLayerFrom(), LayerTo: other.GetLayerTo()}
		if in, ok := started.get(other); ok {
			peer.Address = net.JoinHostPort(hostOf(in.GetSeat().GetAddress()), strconv.Itoa(int(in.GetSeat().GetPort())))
		}
		spec.Peers = append(spec.Peers, peer)
	}
	run.Seat = spec
	return run
}

// The detail of a seat that ended: its first triage hit's summary and hint, else its error,
// else its state
func seatDetail(in *v1.Instance) string {
	if len(in.GetTriage()) > 0 {
		return in.GetTriage()[0].GetSummary() + ". " + in.GetTriage()[0].GetHint()
	}
	if in.GetError() != "" {
		return in.GetError()
	}
	return instanceWord(in.GetState())
}

// Waits for a seat's instance to be ready, reading it from this node or its node
func (m *Manager) waitSeat(ctx context.Context, s *v1.Seat, instanceID string, timeout time.Duration) (*v1.Instance, error) {
	deadline := time.Now().Add(timeout)
	var last *v1.Instance
	for {
		var in *v1.Instance
		var err error
		if s.GetNodeId() == m.self() {
			in, err = m.Instances.Get(instanceID)
		} else {
			in, err = m.remoteSeat(ctx, s.GetNodeId(), instanceID)
		}
		if err == nil {
			last = in
			switch in.GetState() {
			case v1.InstanceState_INSTANCE_STATE_READY:
				return in, nil
			case v1.InstanceState_INSTANCE_STATE_FAILED, v1.InstanceState_INSTANCE_STATE_STOPPED:
				return in, errors.New(seatDetail(in))
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return last, fmt.Errorf("not ready within %s: %w", timeout, err)
			}
			return last, fmt.Errorf("not ready within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(seatPoll):
		}
	}
}

// Reads a seat's instance from its node
func (m *Manager) remoteSeat(ctx context.Context, nodeID, instanceID string) (*v1.Instance, error) {
	cl, err := m.Mesh.Client(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := cl.Mesh.GetSeat(cctx, connect.NewRequest(&v1.GetSeatRequest{InstanceId: instanceID}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetInstance(), nil
}

// Stops one seat on its node
func (m *Manager) stopSeat(ctx context.Context, s *v1.Seat) error {
	if s.GetNodeId() == m.self() {
		_, err := m.Instances.Stop(ctx, s.GetInstanceId())
		if errors.Is(err, instances.ErrUnknownInstance) {
			return nil
		}
		return err
	}
	cl, err := m.Mesh.Client(ctx, s.GetNodeId())
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout+runtimes.DefaultStopGrace)
	defer cancel()
	_, err = cl.Mesh.StopSeat(cctx, connect.NewRequest(&v1.StopSeatRequest{InstanceId: s.GetInstanceId()}))
	if connect.CodeOf(err) == connect.CodeNotFound {
		return nil
	}
	return err
}

// The seats of a formation as the gateway sends to them
func routeSeats(f *v1.Formation) []*v1.RouteSeat {
	var seats []*v1.RouteSeat
	for _, s := range f.GetSeats() {
		seats = append(seats, &v1.RouteSeat{NodeId: s.GetNodeId(), InstanceId: s.GetInstanceId(), Endpoint: s.GetEndpoint(), Role: s.GetRole(), Rank: s.GetRank(), State: s.GetState(), Address: s.GetAddress(), AuxPort: s.GetAuxPort()})
	}
	return seats
}

// The head's instance as the gateway serves it: the record on this node when the head runs
// here, else the head's node's record with the endpoint this node reaches the head at
func (m *Manager) headInstance(ctx context.Context, f *v1.Formation) (*v1.Instance, error) {
	head := m.headSeat(f)
	if head == nil || head.GetInstanceId() == "" {
		return nil, fmt.Errorf("%w: %s has no head seat", ErrFormation, f.GetName())
	}
	if head.GetNodeId() == m.self() {
		in, err := m.Instances.Get(head.GetInstanceId())
		if err != nil {
			return nil, fmt.Errorf("head of %s: %w", f.GetName(), err)
		}
		return in, nil
	}
	in, err := m.remoteSeat(ctx, head.GetNodeId(), head.GetInstanceId())
	if err != nil {
		return nil, fmt.Errorf("head of %s on %s: %w", f.GetName(), head.GetNodeName(), err)
	}
	in = proto.Clone(in).(*v1.Instance)
	in.Endpoint = seatEndpoint(in, true)
	return in, nil
}

// Puts the formation's route on this node's gateway: the head answers it, and the seats the
// gateway sends to are listed with the endpoints this node reaches them at
func (m *Manager) route(ctx context.Context, id string) error {
	f, err := m.Get(id)
	if err != nil {
		return err
	}
	in, err := m.headInstance(ctx, f)
	if err != nil {
		return err
	}
	api := m.Runtimes.API(f.GetRuntimeId())
	names := []slots.RouteName{{Name: f.GetName()}}
	if f.GetSlotId() != "" && m.SlotView != nil {
		if list := m.SlotView.Names(f.GetSlotId()); len(list) > 0 {
			names = list
		}
	}
	var route *v1.Route
	for _, n := range names {
		route = m.Routes.ServeFormationAs(n.Name, f, in, api, routeSeats(f), n.Policy, n.Profile)
	}
	endpoint := m.Mesh.GatewayBase()
	m.update(id, func(r *v1.Formation) { r.Endpoint = endpoint })
	m.Log.Info("formation routed", "formation", f.GetName(), "route", route.GetName(), "shape", shapeWord(f.GetShape()), "endpoint", endpoint)
	return nil
}

// Follows seats: an instance on this node leaving ready, or a member's record reporting a seat
// gone, degrades the formation it serves
func (m *Manager) watch(ctx context.Context) {
	sub := m.Events.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_INSTANCE, v1.EventKind_EVENT_KIND_NODE})
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sub.Events():
			if in := ev.GetInstance(); in != nil && in.GetSeat() != nil {
				m.seatChanged(in.GetSeat().GetFormationId(), in.GetId(), in.GetState(), in.GetTransport(), in.GetError(), in.GetTriage(), in.GetMeasurements())
			}
			if node := ev.GetNode(); node != nil && node.GetId() != m.self() && ev.GetAction() != v1.EventAction_EVENT_ACTION_DELETED {
				for _, ref := range node.GetSeats() {
					m.seatChanged(ref.GetFormationId(), ref.GetInstanceId(), ref.GetState(), ref.GetTransport(), ref.GetError(), ref.GetTriage(), nil)
				}
				m.seatsMissing(node)
			}
		}
	}
}

// Takes a seat's new state, transport, error, triage, and measurements into its formation's
// record, degrading a serving formation whose seat left ready
func (m *Manager) seatChanged(formationID, instanceID string, state v1.InstanceState, transport, errText string, triage []*v1.TriageHit, measurements []*v1.Measurement) {
	m.mu.Lock()
	f, ok := m.list[formationID]
	if !ok || f.GetConductor() != m.self() {
		m.mu.Unlock()
		return
	}
	var seat *v1.Seat
	for _, s := range f.GetSeats() {
		if s.GetInstanceId() == instanceID {
			seat = s
		}
	}
	if seat == nil || seat.GetState() == state && seat.GetTransport() == transport && len(triage) == len(seat.GetTriage()) && len(measurements) == len(seat.GetMeasurements()) {
		m.mu.Unlock()
		return
	}
	fState := f.GetState()
	m.mu.Unlock()
	m.update(formationID, func(r *v1.Formation) {
		for _, s := range r.GetSeats() {
			if s.GetInstanceId() == instanceID {
				s.State, s.Transport = state, transport
				if errText != "" {
					s.Error = errText
				}
				if len(triage) > 0 {
					s.Triage = triage
				}
				if len(measurements) > 0 {
					s.Measurements = measurements
				}
				if transport == transportRDMA {
					s.Exposed = true
				}
			}
		}
	})
	m.Routes.SetSeatState(formationID, instanceID, state)
	if fState == v1.FormationState_FORMATION_STATE_READY && instances.Terminal(state) && !m.isStopping(formationID) {
		go m.degrade(formationID, fmt.Sprintf("%s on %s left ready: %s", seat.GetRole(), seat.GetNodeName(), strings.TrimSpace(errText+" "+instanceWord(state))))
	}
}

// Degrades a ready formation whose node record no longer lists a seat it should host
func (m *Manager) seatsMissing(node *v1.Node) {
	if node.GetState() != v1.NodeState_NODE_STATE_READY || node.GetProfile() == nil {
		return
	}
	listed := map[string]bool{}
	for _, ref := range node.GetSeats() {
		listed[ref.GetInstanceId()] = true
	}
	m.mu.Lock()
	var gone []*v1.Formation
	for _, f := range m.list {
		if f.GetConductor() != m.self() || f.GetState() != v1.FormationState_FORMATION_STATE_READY {
			continue
		}
		for _, s := range f.GetSeats() {
			if s.GetNodeId() == node.GetId() && s.GetInstanceId() != "" && !listed[s.GetInstanceId()] {
				gone = append(gone, f)
				break
			}
		}
	}
	m.mu.Unlock()
	for _, f := range gone {
		if !m.isStopping(f.GetId()) {
			go m.degrade(f.GetId(), fmt.Sprintf("%s no longer hosts its seat", node.GetName()))
		}
	}
}

// Takes a serving formation down when a seat leaves: the route drains, the rest of the seats
// stop, and the formation launches again when it is wanted
func (m *Manager) degrade(id, why string) {
	m.mu.Lock()
	f, ok := m.list[id]
	if !ok || f.GetState() != v1.FormationState_FORMATION_STATE_READY || m.busy[id] {
		m.mu.Unlock()
		return
	}
	m.busy[id] = true
	m.stopping[id] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.busy, id)
		delete(m.stopping, id)
		m.mu.Unlock()
	}()
	m.Log.Warn("formation degraded", "formation", f.GetName(), "why", why)
	m.update(id, func(r *v1.Formation) {
		r.State = v1.FormationState_FORMATION_STATE_DEGRADED
		r.Error = why
	})
	m.Routes.DrainFormation(id)
	current, err := m.Get(id)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(m.base, stopTimeout)
	m.stopSeats(ctx, current, nil)
	cancel()
	m.Routes.RemoveFormation(id)
	f = m.update(id, func(r *v1.Formation) {
		r.State = v1.FormationState_FORMATION_STATE_FAILED
		r.StoppedAt = timestamppb.Now()
	})
	m.Mesh.Bump()
	if !f.GetDesiredRunning() {
		return
	}
	m.Log.Info("formation relaunches", "formation", f.GetName(), "in", relaunchDelay)
	select {
	case <-m.base.Done():
		return
	case <-time.After(relaunchDelay):
	}
	if _, _, err := m.relaunch(m.base, f); err != nil {
		m.Log.Warn("formation relaunch failed", "formation", f.GetName(), "err", err)
		m.update(id, func(r *v1.Formation) { r.Error = why + "; relaunch failed: " + err.Error() })
	}
}

// The host of a mesh address
func hostOf(address string) string {
	if h, _, err := net.SplitHostPort(address); err == nil {
		return h
	}
	return strings.Trim(address, "[]")
}

// A free port on a host
func freePortOn(host string) (int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// Bytes the head streamed to the seats that hold no files of their own: what each seat's guard
// listener counted, or for a seat exposed on the mesh address, what the head's log says it placed
// on that seat. A seat neither counts is named in the task log.
func (m *Manager) bytesMoved(ctx context.Context, h *tasks.Handle, id string, p *planned) uint64 {
	f, err := m.Get(id)
	if err != nil {
		return 0
	}
	var total uint64
	var headLines []string
	if head := m.headSeat(f); head != nil && head.GetNodeId() == m.self() && head.GetInstanceId() != "" {
		m.Instances.Logs(ctx, head.GetInstanceId(), false, 0, func(lines []string) error {
			headLines = append(headLines, lines...)
			return nil
		})
	}
	for _, s := range f.GetSeats() {
		role, err := runtimes.RoleOf(p.rt, &v1.SeatSpec{Shape: f.GetShape(), Role: s.GetRole()})
		if err != nil || role.Files != runtimes.FilesNone || s.GetInstanceId() == "" {
			continue
		}
		var in *v1.Instance
		if s.GetNodeId() == m.self() {
			in, err = m.Instances.Refresh(s.GetInstanceId())
		} else {
			in, err = m.remoteSeat(ctx, s.GetNodeId(), s.GetInstanceId())
		}
		if err != nil {
			h.Logf("bytes moved to %s on %s not read: %v", s.GetRole(), s.GetNodeName(), err)
			continue
		}
		counted := false
		for _, ms := range in.GetMeasurements() {
			if ms.GetKey() == instances.GuardReceivedKey {
				total += ms.GetBytes()
				counted = true
				h.Logf("%s on %s took %s through its guard listener", s.GetRole(), s.GetNodeName(), human(ms.GetBytes()))
			}
		}
		if counted {
			continue
		}
		address := strings.TrimPrefix(strings.TrimPrefix(s.GetEndpoint(), "tcp://"), "http://")
		if placed, ok := headStreamed(headLines, address); ok {
			total += placed
			h.Logf("%s on %s is exposed, the head's log says it placed %s there", s.GetRole(), s.GetNodeName(), human(placed))
			continue
		}
		h.Logf("%s on %s is exposed and the head's log names no buffer on %s, so nothing was counted for it", s.GetRole(), s.GetNodeName(), address)
	}
	return total
}

// Bytes the head's log says it placed on an rpc backend at an address, summed over the model
// buffer lines naming it
func headStreamed(lines []string, address string) (uint64, bool) {
	const phrase = " model buffer size = "
	var total uint64
	found := false
	for _, line := range lines {
		i := strings.Index(line, phrase)
		if i < 0 || !strings.Contains(line[:i], "RPC["+address+"]") {
			continue
		}
		n, ok := parseBytes(line[i+len(phrase):])
		if !ok {
			continue
		}
		total += n
		found = true
	}
	return total, found
}

// A byte count as a runtime logs it: a number and a unit
func parseBytes(s string) (uint64, bool) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0, false
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	unit := 1.0
	if len(fields) > 1 {
		switch strings.ToLower(strings.TrimRight(fields[1], ",;")) {
		case "b", "bytes":
		case "kib", "kb":
			unit = 1 << 10
		case "mib", "mb":
			unit = 1 << 20
		case "gib", "gb":
			unit = 1 << 30
		case "tib", "tb":
			unit = 1 << 40
		default:
			return 0, false
		}
	}
	return uint64(value * unit), true
}

func human(b uint64) string {
	const unit = 1024.0
	v := float64(b)
	for _, suffix := range []string{"B", "KiB", "MiB", "GiB", "TiB"} {
		if v < unit || suffix == "TiB" {
			return fmt.Sprintf("%.1f %s", v, suffix)
		}
		v /= unit
	}
	return fmt.Sprintf("%d B", b)
}
