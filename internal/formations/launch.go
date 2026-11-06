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
	"maps"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/pools"
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
	// Words the canary prompt runs past the relay break even, and the room it keeps under the context
	canaryMargin = 32
)

// Plans and launches a formation across the span
func (m *Manager) Run(ctx context.Context, run *v1.RunRequest) (*v1.Formation, *v1.Task, error) {
	// A seat request carries its block and would conduct a formation named for the seat, so it is refused
	if seat := run.GetSeat(); seat != nil {
		return nil, nil, fmt.Errorf("%w: %s is a %s seat of the formation %s, not a formation of its own; run %s", ErrFormation, run.GetName(), seat.GetRole(), seat.GetName(), seat.GetName())
	}
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

// Launches a formation again over the mesh as it is, dropping departed members and reusing matching seats' rendezvous
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
	p, err := m.planPinned(ctx, run, seatPins(prev))
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

// The install each node's seats ran on, what a relaunch holds every node to
func seatPins(prev *v1.Formation) map[string]string {
	pins := map[string]string{}
	for _, s := range prev.GetSeats() {
		if s.GetInstallId() != "" {
			pins[s.GetNodeId()] = s.GetInstallId()
		}
	}
	return pins
}

// Records a planned formation and starts its launch task
func (m *Manager) start(ctx context.Context, run *v1.RunRequest, p *planned, prev *v1.Formation) (*v1.Formation, *v1.Task, error) {
	plan := p.plan
	if len(plan.GetSeats()) == 0 {
		return nil, nil, fmt.Errorf("%w: %s has no seats: %s", ErrFormation, p.name, plan.GetDetail())
	}
	if !instances.FromSwap(ctx) && (m.nameTaken(p.name) || m.liveInstance(p.name)) {
		return nil, nil, fmt.Errorf("%w: %q is already running", ErrFormation, p.name)
	}
	if run.GetSlotId() == "" && m.SlotView != nil && m.SlotView.Owns(p.name) {
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
		Generation:     1,
	}
	f.Conditions = instances.SetCondition(f.Conditions, CondPlanned, true, ReasonPlanned, plan.GetDetail(), 1)
	f.Conditions = instances.SetCondition(f.Conditions, CondLaunched, false, ReasonLaunching, "", 1)
	f.Conditions = instances.SetCondition(f.Conditions, CondReady, false, ReasonLaunching, "", 1)
	f.Conditions = instances.SetCondition(f.Conditions, CondDegraded, false, ReasonLaunching, "", 1)
	f.Request.Name = p.name
	f.Request.RuntimeId = p.rt.ID()
	f.Request.SourceId = p.stored.GetSourceId()
	for _, s := range f.Seats {
		s.State = v1.InstanceState_INSTANCE_STATE_STARTING
	}
	if prev != nil {
		f.Rendezvous = carryRendezvous(prev, f.Seats)
		f.Relaunches = nextRelaunches(prev)
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

// A model's tensor cache key from its store key and file digests, stable across relaunches so stages skip held tensors
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

// Carries the rendezvous address and ranks onto relaunched seats when the same nodes play the same roles
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

// Pulls weights, launches seats by phase, proves a relay pair, and publishes the head, a cancel stopping every seat started
func (m *Manager) launch(ctx context.Context, h *tasks.Handle, id string, p *planned) error {
	f, err := m.Get(id)
	if err != nil {
		return err
	}
	h.Logf("%s", f.GetPlan().GetDetail())
	for _, c := range f.GetPlan().GetCandidates() {
		switch {
		case c.GetShape() == f.GetShape() && c.GetVerdict() == v1.FitVerdict_FIT_VERDICT_FITS:
		case c.GetVerdict() == v1.FitVerdict_FIT_VERDICT_FITS:
			h.Logf("not chosen: %s on %s, %s", shapeWord(c.GetShape()), strings.Join(c.GetNodeIds(), ","), c.GetReason())
		default:
			h.Logf("cannot run: %s on %s, %s", shapeWord(c.GetShape()), strings.Join(c.GetNodeIds(), ","), c.GetReason())
		}
	}
	fail := func(err error) error {
		if m.base.Err() != nil {
			return err
		}
		h.Logf("formation failed: %v", err)
		cleanup, cancel := context.WithTimeout(m.base, stopTimeout)
		defer cancel()
		m.gatherSeatLogs(cleanup, h, id, p)
		if current, err := m.Get(id); err == nil {
			m.drain(cleanup, current)
			m.stopSeats(cleanup, current, h)
		}
		m.withdraw(cleanup, id)
		m.update(id, func(r *v1.Formation) {
			if r.State != v1.FormationState_FORMATION_STATE_STOPPING && r.State != v1.FormationState_FORMATION_STATE_STOPPED {
				r.State = v1.FormationState_FORMATION_STATE_FAILED
				r.Error = err.Error()
				r.StoppedAt = timestamppb.Now()
				r.Conditions = instances.SetCondition(r.Conditions, CondLaunched, false, ReasonLaunchFailed, r.Error, r.GetGeneration())
				r.Conditions = instances.SetCondition(r.Conditions, CondReady, false, ReasonLaunchFailed, r.Error, r.GetGeneration())
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
	m.update(id, func(r *v1.Formation) {
		r.Conditions = instances.SetCondition(r.Conditions, CondLaunched, true, ReasonSeatsReady, fmt.Sprintf("%d seats ready", total), r.GetGeneration())
	})
	if err := m.publishSeats(ctx, id, nil); err != nil {
		return fail(err)
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
			markReady(r)
		}
		r.BytesMoved = moved
	})
	m.servedWhole(id)
	m.Mesh.Bump()
	h.Progress(uint64(total), uint64(total), "ready at "+f.GetEndpoint())
	h.Logf("ready %s as %s across %d seats", f.GetName(), shapeWord(f.GetShape()), total)
	return nil
}

// The seat whose node allocates a phase's rendezvous address, the head among ranks or rank 0, nil when none
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

// Sends one completion past the relay break even through a canary route, passing when the trace shows both seats served
func (m *Manager) canary(ctx context.Context, h *tasks.Handle, id string) error {
	if m.Gateway == nil {
		return fmt.Errorf("%w: this node has no gateway to send the canary through", ErrFormation)
	}
	f, err := m.Get(id)
	if err != nil {
		return err
	}
	breakEven, window := f.GetPlan().GetRelayBreakEvenPrompt(), f.GetPlan().GetContext()
	words := breakEven + canaryMargin
	if window > 0 && words+canaryMargin >= window {
		return fmt.Errorf("%w: the plan's relay break even of %d tokens leaves no room under its %d token context, so no request takes the pair", ErrFormation, breakEven, window)
	}
	owner := ownerOf(id)
	name := f.GetName() + ".canary"
	if _, err := m.Registrar.NameRoute(ctx, owner, name, pools.RouteSpec{Shape: f.GetShape(), Seats: pools.SeatIDs(f)}); err != nil {
		return fmt.Errorf("canary route %s: %w", name, err)
	}
	defer m.Registrar.DropRoute(context.WithoutCancel(ctx), owner, name)
	h.Message("canary through the relay pair")
	prompt := strings.TrimSpace(strings.Repeat("ok ", int(words))) + " Say ok."
	body, err := json.Marshal(map[string]any{"model": name, "messages": []map[string]string{{"role": "user", "content": prompt}}, "max_tokens": 1, "stream": false})
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
	t, ok := m.Gateway.Trace(resp)
	if !ok {
		return fmt.Errorf("canary answered but the gateway kept no trace of it")
	}
	if t.GetRelay() != relayBoth {
		return fmt.Errorf("canary answered by the decode seat alone (%s), the relay handoff did not happen", t.GetRelay())
	}
	h.Logf("canary answered through the relay pair in %s", time.Since(start).Round(time.Millisecond))
	return nil
}

// A store reference in a run parameter, the store scheme then source, repo and group
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

// Pulls onto each seat's node the files its role reads, model, draft, parts and components, seats in parallel
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
				if source, repo, group, ok := parseStoreRef(p.params[param.GetName()]); ok {
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
				if err := m.ensure(ctx, h, progress, s.GetNodeId(), n.source, n.repo, n.group, n.revise, nil, ""); err != nil {
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

// Makes sure a node holds a model, pulling it there and reporting the pull's byte progress
func (m *Manager) ensure(ctx context.Context, h *tasks.Handle, progress func(done, total uint64, message string), node, source, repo, group, revision string, paths []string, slot string) error {
	if node == m.self() {
		if _, err := m.Store.ReadManifest(source, repo, group); err == nil && len(paths) == 0 {
			h.Logf("%s %s already on %s", repo, group, m.nodeName(node))
			progress(1, 1, fmt.Sprintf("%s %s already here", repo, group))
			return nil
		}
		task, err := m.Puller.Pull(ctx, &v1.PullRequest{SourceId: source, Repo: repo, Group: group, Revision: revision, Paths: paths})
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
	if len(paths) == 0 && holds(rec, source, repo, group) {
		h.Logf("%s %s already on %s", repo, group, rec.GetName())
		progress(1, 1, fmt.Sprintf("%s %s already on %s", repo, group, rec.GetName()))
		return nil
	}
	cl, err := m.Mesh.Client(ctx, node)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	resp, err := cl.Mesh.PullSeat(cctx, connect.NewRequest(&v1.PullSeatRequest{SourceId: source, Repo: repo, Revision: revision, Group: group, Paths: paths, Slot: slot}))
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

// Launches one seat on its node and records its instance, so peers launched later learn its address
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
				rs.InstanceId, rs.State, rs.Conditions = in.GetId(), in.GetState(), seatConditions(in.GetConditions())
				rs.Endpoint = seatEndpoint(in, remote)
				rs.Address, rs.AuxPort = in.GetSeat().GetAddress(), in.GetSeat().GetAuxPort()
			}
		}
	})
	return in, nil
}

// Waits for a launched seat to be ready, recording its endpoint, transport, exposure and measurements, or its error and triage
func (m *Manager) awaitSeat(ctx context.Context, h *tasks.Handle, f *v1.Formation, p *planned, s *v1.Seat, in *v1.Instance) (*v1.Instance, error) {
	remote := s.GetNodeId() != m.self()
	health, err := runtimes.SeatHealth(p.rt, in.GetSeat())
	if err != nil {
		return in, err
	}
	slowAfter := health.SlowAfter
	if slowAfter <= 0 {
		slowAfter = 5 * time.Minute
	}
	ready, err := m.waitSeat(ctx, s, in.GetId(), slowAfter+seatGrace, func(waited time.Duration) {
		h.Logf("seat %s on %s is not ready after %s, waiting for as long as it lives", in.GetName(), s.GetNodeName(), waited)
	})
	if err != nil {
		m.update(f.GetId(), func(r *v1.Formation) {
			for _, rs := range r.GetSeats() {
				if sameSeat(rs, s) {
					rs.State, rs.Error = v1.InstanceState_INSTANCE_STATE_FAILED, err.Error()
					if ready != nil {
						rs.State, rs.Conditions = ready.GetState(), seatConditions(ready.GetConditions())
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
				rs.Conditions = seatConditions(ready.GetConditions())
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

// A copy of the conditions a seat's node reports for its instance
func seatConditions(list []*v1.Condition) []*v1.Condition {
	out := make([]*v1.Condition, 0, len(list))
	for _, c := range list {
		out = append(out, proto.Clone(c).(*v1.Condition))
	}
	return out
}

// Copies of a seat's rank records
func seatRanks(list []*v1.InstanceRank) []*v1.InstanceRank {
	out := make([]*v1.InstanceRank, 0, len(list))
	for _, r := range list {
		out = append(out, proto.Clone(r).(*v1.InstanceRank))
	}
	return out
}

// Marks each seat rank ready while the seat is ready and the rank healthy, one rank by its condition
func (m *Manager) seatReadiness(ctx context.Context, shape v1.Shape, seatRank uint32, instanceID string, state v1.InstanceState, conditions []*v1.Condition, ranks []*v1.InstanceRank) {
	ready := state == v1.InstanceState_INSTANCE_STATE_READY
	if len(ranks) == 0 {
		healthy := instances.ConditionOf(conditions, instances.CondHealthy)
		m.Registrar.SetReady(ctx, instanceID, ready && (healthy == nil || healthy.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE))
		return
	}
	spec := &v1.SeatSpec{Shape: shape, Rank: seatRank}
	for _, r := range ranks {
		m.Registrar.SetRankReady(ctx, instanceID, instances.PublishedRank(spec, len(ranks), int(r.GetRank())), ready && r.GetHealthy())
	}
}

func sameSeat(a, b *v1.Seat) bool {
	return a.GetNodeId() == b.GetNodeId() && a.GetRole() == b.GetRole() && a.GetRank() == b.GetRank()
}

// Where the conductor reaches a seat, its loopback endpoint here or its guard or exposed port on another node
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

// The formation's request narrowed to one seat, slot params beneath the request's, extra words in its block
func (m *Manager) seatRequest(f *v1.Formation, p *planned, s *v1.Seat, rendezvous string, started *launched) *v1.RunRequest {
	run := proto.Clone(f.GetRequest()).(*v1.RunRequest)
	run.Name = fmt.Sprintf("%s/%s%d", f.GetName(), s.GetRole(), s.GetRank())
	run.SlotId, run.Span, run.Shape, run.Args = "", nil, f.GetShape(), ""
	run.RuntimeId, run.InstallId = f.GetRuntimeId(), s.GetInstallId()
	run.SourceId, run.Repo, run.Group = f.GetSourceId(), f.GetRepo(), f.GetGroup()
	run.Params = seatParams(f, p)
	spec := &v1.SeatSpec{
		FormationId:  f.GetId(),
		Shape:        f.GetShape(),
		Role:         s.GetRole(),
		Rank:         s.GetRank(),
		Count:        uint32(len(f.GetSeats())),
		Rendezvous:   rendezvous,
		LayerFrom:    s.GetLayerFrom(),
		LayerTo:      s.GetLayerTo(),
		Conductor:    m.self(),
		Name:         f.GetName(),
		Devices:      s.GetDeviceIds(),
		DeviceBytes:  s.GetDeviceBytes(),
		DeviceLayers: s.GetDeviceLayers(),
		Context:      f.GetPlan().GetContext(),
		GpuLayers:    s.GetGpuLayers(),
		Memory:       s.GetMemory(),
		CacheKey:     f.GetCacheKey(),
		Args:         p.args,
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

// Params every seat launches with, the slot's beneath the request's, at the context the plan holds
func seatParams(f *v1.Formation, p *planned) map[string]string {
	params := maps.Clone(p.params)
	if params == nil {
		params = map[string]string{}
	}
	if ctxParam := p.rt.Policy().ContextParam; ctxParam != "" && f.GetPlan().GetContext() > 0 {
		params[ctxParam] = strconv.FormatUint(uint64(f.GetPlan().GetContext()), 10)
	}
	return params
}

// Why a seat ended, its first triage hit's summary and hint, else its error, else its state
func seatDetail(in *v1.Instance) string {
	if len(in.GetTriage()) > 0 {
		return in.GetTriage()[0].GetSummary() + ". " + in.GetTriage()[0].GetHint()
	}
	if in.GetError() != "" {
		return in.GetError()
	}
	return instanceWord(in.GetState())
}

// Waits for a seat's instance to be ready from its node, warning after each slowAfter it is not
func (m *Manager) waitSeat(ctx context.Context, s *v1.Seat, instanceID string, slowAfter time.Duration, slow func(time.Duration)) (*v1.Instance, error) {
	start := time.Now()
	warn := time.NewTicker(slowAfter)
	defer warn.Stop()
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
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-warn.C:
			slow(time.Since(start).Round(time.Second))
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

// Names the formation's route for a formation outside a slot and records the gateway base members reach it at
func (m *Manager) route(ctx context.Context, id string) error {
	f, err := m.Get(id)
	if err != nil {
		return err
	}
	if f.GetSlotId() == "" {
		if _, err := m.Registrar.NameRoute(ctx, ownerOf(id), f.GetName(), routeSpec(f)); err != nil {
			return fmt.Errorf("route %s: %w", f.GetName(), err)
		}
	}
	endpoint := m.Mesh.GatewayBase()
	m.update(id, func(r *v1.Formation) { r.Endpoint = endpoint })
	m.Log.Info("formation routed", "formation", f.GetName(), "slot", f.GetSlotId(), "shape", shapeWord(f.GetShape()), "endpoint", endpoint)
	return nil
}

// Follows seats, degrading a formation when a local instance leaves ready or a member's record reports its seat gone
func (m *Manager) watch(ctx context.Context) {
	sub := m.Events.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_INSTANCE, v1.EventKind_EVENT_KIND_NODE})
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sub.Events():
			if in := ev.GetInstance(); in != nil && in.GetSeat() != nil {
				m.seatChanged(in.GetSeat().GetFormationId(), in.GetId(), in.GetState(), in.GetTransport(), in.GetError(), in.GetTriage(), in.GetMeasurements(), in.GetConditions(), in.GetRanks())
			}
			if node := ev.GetNode(); node != nil && node.GetId() != m.self() && ev.GetAction() != v1.EventAction_EVENT_ACTION_DELETED {
				for _, ref := range node.GetSeats() {
					m.seatChanged(ref.GetFormationId(), ref.GetInstanceId(), ref.GetState(), ref.GetTransport(), ref.GetError(), ref.GetTriage(), nil, ref.GetConditions(), ref.GetRanks())
				}
				m.seatsMissing(node)
			}
		}
	}
}

// Takes a seat's state, transport, error, triage, measurements and conditions into its record, degrading a serving formation it left
func (m *Manager) seatChanged(formationID, instanceID string, state v1.InstanceState, transport, errText string, triage []*v1.TriageHit, measurements []*v1.Measurement, conditions []*v1.Condition, ranks []*v1.InstanceRank) {
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
	sameConditions := slices.EqualFunc(conditions, seat.GetConditions(), func(a, b *v1.Condition) bool { return proto.Equal(a, b) })
	sameRanks := slices.EqualFunc(ranks, seat.GetRanks(), func(a, b *v1.InstanceRank) bool { return proto.Equal(a, b) })
	if seat == nil || seat.GetState() == state && seat.GetTransport() == transport && len(triage) == len(seat.GetTriage()) && len(measurements) == len(seat.GetMeasurements()) && sameConditions && sameRanks {
		m.mu.Unlock()
		return
	}
	// A lagging node record never revives an instance already ended
	if instances.Terminal(seat.GetState()) && !instances.Terminal(state) {
		m.mu.Unlock()
		return
	}
	fState := f.GetState()
	m.mu.Unlock()
	m.update(formationID, func(r *v1.Formation) {
		for _, s := range r.GetSeats() {
			if s.GetInstanceId() == instanceID {
				s.State, s.Transport, s.Conditions, s.Ranks = state, transport, seatConditions(conditions), seatRanks(ranks)
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
	ctx, cancel := context.WithTimeout(m.base, callTimeout)
	m.seatReadiness(ctx, f.GetShape(), seat.GetRank(), instanceID, state, conditions, ranks)
	cancel()
	if serving(fState) && instances.Terminal(state) && !m.isStopping(formationID) {
		if f, err := m.Get(formationID); err == nil {
			m.seatLeft(f, seat, fmt.Sprintf("%s on %s left ready: %s", seat.GetRole(), seat.GetNodeName(), strings.TrimSpace(errText+" "+instanceWord(state))))
		}
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
	type left struct {
		f    *v1.Formation
		seat *v1.Seat
	}
	m.mu.Lock()
	var gone []left
	for _, f := range m.list {
		if f.GetConductor() != m.self() || !serving(f.GetState()) {
			continue
		}
		for _, s := range f.GetSeats() {
			if s.GetNodeId() == node.GetId() && s.GetInstanceId() != "" && !listed[s.GetInstanceId()] {
				gone = append(gone, left{proto.Clone(f).(*v1.Formation), proto.Clone(s).(*v1.Seat)})
			}
		}
	}
	m.mu.Unlock()
	for _, g := range gone {
		if !m.isStopping(g.f.GetId()) {
			m.seatLeft(g.f, g.seat, fmt.Sprintf("%s no longer hosts its seat", node.GetName()))
		}
	}
}

// Takes a serving formation down when a seat leaves, draining and stopping every seat, then launches it again when wanted
func (m *Manager) degrade(id, why string) {
	m.cancelReseats(id)
	m.mu.Lock()
	f, ok := m.list[id]
	if !ok || !serving(f.GetState()) || m.busy[id] {
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
		r.Conditions = instances.SetCondition(r.Conditions, CondDegraded, true, ReasonFormationDown, why, r.GetGeneration())
		r.Conditions = instances.SetCondition(r.Conditions, CondReady, false, ReasonFormationDown, why, r.GetGeneration())
	})
	current, err := m.Get(id)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(m.base, stopTimeout)
	m.drain(ctx, current)
	m.stopSeats(ctx, current, nil)
	m.withdraw(ctx, id)
	cancel()
	f = m.update(id, func(r *v1.Formation) {
		r.State = v1.FormationState_FORMATION_STATE_FAILED
		r.StoppedAt = timestamppb.Now()
		r.Conditions = instances.SetCondition(r.Conditions, CondLaunched, false, ReasonFormationDown, why, r.GetGeneration())
	})
	m.Mesh.Bump()
	if !f.GetDesiredRunning() {
		return
	}
	if next := nextRelaunches(f); next > maxRelaunches {
		m.Log.Warn("formation degraded repeatedly, not relaunched", "formation", f.GetName(), "relaunches", f.GetRelaunches())
		m.update(id, func(r *v1.Formation) {
			r.Error = fmt.Sprintf("%s; degraded %d times in a row within %s of ready, so it stays stopped; run it again to retry", why, f.GetRelaunches()+1, relaunchStable)
			r.DesiredRunning = false
		})
		m.Mesh.Bump()
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

// The relaunch count after prev, one more when prev never served a stretch, one when it did
func nextRelaunches(prev *v1.Formation) uint32 {
	if prev == nil {
		return 0
	}
	if prev.GetReadyAt() != nil && prev.GetStoppedAt() != nil && prev.GetStoppedAt().AsTime().Sub(prev.GetReadyAt().AsTime()) >= relaunchStable {
		return 1
	}
	return prev.GetRelaunches() + 1
}

// The host of a mesh address
func hostOf(address string) string {
	if h, _, err := net.SplitHostPort(address); err == nil {
		return h
	}
	return strings.Trim(address, "[]")
}

// A free port on a host, the first in the mesh port range when one is set, else any
func (m *Manager) freePortOn(host string) (int, error) {
	if from, to, ok := m.Mesh.Ports(); ok {
		for port := to; port >= from; port-- {
			ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
			if err != nil {
				continue
			}
			ln.Close()
			return port, nil
		}
		return 0, fmt.Errorf("no free port on %s in the mesh port range %d-%d", host, from, to)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// Bytes the head streamed to fileless seats, from their guard counters or the head's log, naming any seat neither counts
func (m *Manager) bytesMoved(ctx context.Context, h *tasks.Handle, id string, p *planned) uint64 {
	f, err := m.Get(id)
	if err != nil {
		return 0
	}
	var total uint64
	var headLines []string
	if head := m.headSeat(f); head != nil && head.GetInstanceId() != "" {
		lines, err := m.seatLines(ctx, head)
		if err != nil {
			h.Logf("the head's log on %s not read: %v", head.GetNodeName(), err)
		}
		headLines = lines
	}
	var fileless []*v1.Seat
	for _, s := range f.GetSeats() {
		role, err := runtimes.RoleOf(p.rt, &v1.SeatSpec{Shape: f.GetShape(), Role: s.GetRole()})
		if err == nil && role.Files == runtimes.FilesNone && s.GetInstanceId() != "" {
			fileless = append(fileless, s)
		}
	}
	records := make([]*v1.Instance, len(fileless))
	errs := make([]error, len(fileless))
	var wg sync.WaitGroup
	for i, s := range fileless {
		wg.Add(1)
		go func(i int, s *v1.Seat) {
			defer wg.Done()
			if s.GetNodeId() == m.self() {
				records[i], errs[i] = m.Instances.Refresh(s.GetInstanceId())
				return
			}
			records[i], errs[i] = m.remoteSeat(ctx, s.GetNodeId(), s.GetInstanceId())
		}(i, s)
	}
	wg.Wait()
	for i, s := range fileless {
		if errs[i] != nil {
			h.Logf("bytes moved to %s on %s not read: %v", s.GetRole(), s.GetNodeName(), errs[i])
			continue
		}
		counted := false
		for _, ms := range records[i].GetMeasurements() {
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

// Every line a seat's instance has logged, read on this node or streamed from the seat's node
func (m *Manager) seatLines(ctx context.Context, s *v1.Seat) ([]string, error) {
	var out []string
	collect := func(lines []string) error {
		out = append(out, lines...)
		return nil
	}
	if s.GetNodeId() == m.self() {
		return out, m.Instances.Logs(ctx, s.GetInstanceId(), false, 0, collect)
	}
	cl, err := m.Mesh.Client(ctx, s.GetNodeId())
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	stream, err := cl.Mesh.SeatLogs(cctx, connect.NewRequest(&v1.SeatLogsRequest{InstanceId: s.GetInstanceId(), Follow: false, Tail: 0}))
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	for stream.Receive() {
		collect(stream.Msg().GetLines())
	}
	return out, stream.Err()
}

// Reads each launched seat's log on failure, recording its allocations and logging each failed allocation on the task
func (m *Manager) gatherSeatLogs(ctx context.Context, h *tasks.Handle, id string, p *planned) {
	f, err := m.Get(id)
	if err != nil {
		return
	}
	for _, s := range f.GetSeats() {
		if s.GetInstanceId() == "" {
			continue
		}
		lines, err := m.seatLines(ctx, s)
		if err != nil {
			h.Logf("the log of %s on %s not read: %v", s.GetRole(), s.GetNodeName(), err)
			continue
		}
		measured := p.rt.Measure(lines)
		if len(measured) == 0 {
			continue
		}
		m.update(id, func(r *v1.Formation) {
			for _, rs := range r.GetSeats() {
				if sameSeat(rs, s) {
					rs.Measurements = mergeMeasurements(rs.Measurements, measured)
				}
			}
		})
		for _, ms := range measured {
			if strings.HasSuffix(ms.GetKey(), ".failed") {
				h.Logf("%s on %s failed to allocate %s: %s", s.GetRole(), s.GetNodeName(), human(ms.GetBytes()), strings.TrimSpace(ms.GetLine()))
			}
		}
	}
}

// Merges measurements by key, the fresh ones replacing the old and old-only keys staying
func mergeMeasurements(old, fresh []*v1.Measurement) []*v1.Measurement {
	if len(fresh) == 0 {
		return old
	}
	seen := map[string]bool{}
	for _, ms := range fresh {
		seen[ms.GetKey()] = true
	}
	out := append([]*v1.Measurement(nil), fresh...)
	for _, ms := range old {
		if !seen[ms.GetKey()] {
			out = append(out, ms)
		}
	}
	return out
}

// Bytes the head's log says it placed on an rpc backend at an address, summed over its buffer lines
func headStreamed(lines []string, address string) (uint64, bool) {
	const phrase = " model buffer size = "
	var total uint64
	found := false
	for _, line := range lines {
		i := strings.Index(line, phrase)
		if i < 0 {
			continue
		}
		fields := strings.Fields(line[:i])
		if len(fields) == 0 || !rpcBufferAt(fields[len(fields)-1], address) {
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

// Whether a ggml buffer name is an rpc buffer at an address, with or without a device index
func rpcBufferAt(name, address string) bool {
	index, ok := strings.CutPrefix(name, "RPC")
	if !ok {
		return false
	}
	index, ok = strings.CutSuffix(index, "["+address+"]")
	if !ok {
		return false
	}
	for _, r := range index {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// A byte count as a runtime logs it, a number and a unit
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
