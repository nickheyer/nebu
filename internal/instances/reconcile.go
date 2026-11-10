package instances

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/launch"
	"github.com/nickheyer/nebu/pkg/proc"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/text"
	"github.com/nickheyer/nebu/pkg/triage"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Launches in a row that never reached ready before the instance fails for good
	maxFailedLaunches = 5
	// Health probes failing in a row before the instance stops taking traffic
	unhealthyAfter = 3
	// Requests in flight get this long to finish when the daemon names no drain timeout
	defaultDrainTimeout = 30 * time.Second
	drainPoll           = 100 * time.Millisecond
	// A ready instance's latest health stamp reaches the database this often
	healthRecordEvery = 30 * time.Second
	// Longest a stop waits past the runtime's grace for the process tree to go
	stopSlack = 30 * time.Second
)

var (
	// The first restart delay, doubled after every crash or failed launch up to the cap
	backoffMin = time.Second
	backoffMax = time.Minute
	// Ready this long before a crash, the next restart waits the first delay again
	backoffReset = 5 * time.Minute
)

type instance struct {
	mgr  *Manager
	mu   sync.Mutex
	rec  *v1.Instance
	rt   runtimes.Runtime
	proc launch.Handle
	log  *launch.Log
	// Guard listeners of a seat other nodes reach through one, one per data parallel rank
	guards []*forwarder
	// Closed once the record reaches a final state
	exited   chan struct{}
	exitOnce *sync.Once
	// Wakes the reconciler to read the desired state again
	poke chan struct{}
	// Closed when the reconciler returns, nil while none runs
	loopDone chan struct{}
	// Cancels the launch attempt under way
	cancelAttempt context.CancelFunc
	// The daemon is closing, so the process stops with its relaunch intent kept
	closing  bool
	closeCtx context.Context
	// A request asked an idle instance to load again
	woken bool
	// The process sleeps through its idle window instead of exiting
	asleep bool
	// Whether endpoints of the instance are published under its owner
	published bool
	// Launch inputs of the generation the process runs, and the newer generation Apply resolved
	prepared *prepared
	next     *prepared
	// Launches in a row that did not reach ready, the next restart delay, and when ready was reached
	failures int
	backoff  time.Duration
	delay    time.Duration
	readyAt  time.Time
	// Diffusion generation modes read at ready
	modes []string
	// Port spans reserved for the launch under way
	held []reservation
	// Health probes failed in a row per rank, and when the last health stamp was written
	healthFails   []int
	healthWritten time.Time
	// How the process is known healthy, the runtime's own probe or the seat role's
	health runtimes.RoleHealth
	// The crash a drain saw, recorded on the final state of a stop
	drainCrash string
}

func (m *Manager) newInstance(rec *v1.Instance, rt runtimes.Runtime) *instance {
	rec.Adapters = switchesAdapters(rt, rec)
	in := &instance{mgr: m, rec: rec, rt: rt, exited: make(chan struct{}), exitOnce: &sync.Once{}, poke: make(chan struct{}, 1), backoff: backoffMin}
	if Terminal(rec.GetState()) {
		in.exitOnce.Do(func() { close(in.exited) })
	}
	return in
}

func (in *instance) id() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.rec.GetId()
}

func (in *instance) snapshot() *v1.Instance {
	in.mu.Lock()
	defer in.mu.Unlock()
	return proto.Clone(in.rec).(*v1.Instance)
}

// Applies a change, writes and announces it when anything moved, and closes the exit channel once final
func (in *instance) update(fn func(*v1.Instance)) {
	m := in.mgr
	rec, terminal, guards, exited, once := in.apply(fn)
	if rec != nil && m.Slots != nil {
		m.Slots.OnInstance(rec)
	}
	if terminal {
		for _, g := range guards {
			g.Close()
		}
		once.Do(func() { close(exited) })
	}
}

// Changes the record under its lock, released on a panic too, returning the copy announced when anything moved
func (in *instance) apply(fn func(*v1.Instance)) (rec *v1.Instance, terminal bool, guards []*forwarder, exited chan struct{}, once *sync.Once) {
	m := in.mgr
	in.mu.Lock()
	defer in.mu.Unlock()
	before := proto.Clone(in.rec).(*v1.Instance)
	fn(in.rec)
	in.rec.Adapters = switchesAdapters(in.rt, in.rec)
	if !proto.Equal(before, in.rec) {
		if err := m.DB.PutInstance(context.Background(), in.rec); err != nil {
			m.Log.Warn("instance record write failed", "id", in.rec.GetId(), "err", err)
		}
		rec = proto.Clone(in.rec).(*v1.Instance)
		m.Events.Publish(v1.EventKind_EVENT_KIND_INSTANCE, v1.EventAction_EVENT_ACTION_UPDATED, rec.GetId(), rec)
	}
	terminal = Terminal(in.rec.GetState())
	if terminal {
		guards, in.guards = in.guards, nil
	}
	return rec, terminal, guards, in.exited, in.exitOnce
}

// Writes the record as it stands, for fields changed without an announcement
func (in *instance) flush() {
	in.mu.Lock()
	defer in.mu.Unlock()
	if err := in.mgr.DB.PutInstance(context.Background(), in.rec); err != nil {
		in.mgr.Log.Warn("instance record write failed", "id", in.rec.GetId(), "err", err)
	}
}

// Wakes the reconciler without blocking
func (in *instance) kick() {
	select {
	case in.poke <- struct{}{}:
	default:
	}
}

func (in *instance) handle() launch.Handle {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.proc
}

// Takes a new process or rank group, its ranks starting with no failed health probes
func (in *instance) attach(h launch.Handle) {
	in.mu.Lock()
	in.proc, in.log, in.healthFails = h, h.Log(), nil
	in.mu.Unlock()
}

// Records how the process is known healthy
func (in *instance) setHealth(h runtimes.RoleHealth) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.health = h
}

// How the process is known healthy, as recorded at launch or adoption
func (in *instance) healthOf() runtimes.RoleHealth {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.health
}

// Whether the health probes took a rank out of traffic
func (in *instance) rankDown(rank int) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return rank < len(in.healthFails) && in.healthFails[rank] >= unhealthyAfter
}

// Whether endpoints of the instance are published now
func (in *instance) isPublished() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.published
}

// Records whether endpoints are published and returns what it was before
func (in *instance) setPublished(v bool) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	was := in.published
	in.published = v
	return was
}

func (in *instance) modesOf() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.modes...)
}

func (in *instance) grace() time.Duration {
	if in.rt != nil && in.rt.StopGrace() > 0 {
		return in.rt.StopGrace()
	}
	return runtimes.DefaultStopGrace
}

// Whether the process should go because the run is no longer wanted or the daemon is closing
func (in *instance) stopWanted() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return !in.rec.GetDesiredRunning() || in.closing
}

func (in *instance) isClosing() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.closing
}

// The context every stop step runs under, the daemon's close deadline while it closes
func (in *instance) stopContext() (context.Context, context.CancelFunc) {
	in.mu.Lock()
	closing, cctx := in.closing, in.closeCtx
	in.mu.Unlock()
	if closing && cctx != nil {
		return context.WithCancel(cctx)
	}
	return context.WithTimeout(context.Background(), in.mgr.drainTimeout()+in.grace()+stopSlack)
}

func (m *Manager) drainTimeout() time.Duration {
	if m.DrainTimeout > 0 {
		return m.DrainTimeout
	}
	return defaultDrainTimeout
}

// Whether Apply asked for a request generation the process does not run
func (in *instance) generationChanged() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.next != nil && in.next.generation == in.rec.GetGeneration() && in.rec.GetObservedGeneration() != in.rec.GetGeneration()
}

// One launch running as a task, its error readable once finished closes
type attempt struct {
	task     *v1.Task
	finished chan struct{}
	err      error
}

// Starts a launch task running body, cancelled by a stop request or the daemon closing
func (in *instance) startAttempt(title string, body func(ctx context.Context, h *tasks.Handle) error) *attempt {
	a := &attempt{finished: make(chan struct{})}
	actx, cancel := context.WithCancel(context.Background())
	in.mu.Lock()
	in.cancelAttempt = cancel
	rec := in.rec
	labels := map[string]string{"instance": rec.GetId(), "name": rec.GetName()}
	if s := rec.GetSeat(); s != nil {
		labels["formation"], labels["role"] = s.GetFormationId(), s.GetRole()
	}
	in.mu.Unlock()
	a.task = in.mgr.Tasks.Start(kindRun, title, labels, func(tctx context.Context, h *tasks.Handle) (err error) {
		defer func() {
			a.err = err
			close(a.finished)
		}()
		ctx, stop := context.WithCancel(tctx)
		defer stop()
		unhook := context.AfterFunc(actx, stop)
		defer unhook()
		return body(ctx, h)
	})
	in.update(func(r *v1.Instance) { r.TaskId = a.task.GetId() })
	return a
}

// A launch of the instance's request, prepared inputs reused when they match the generation
func (in *instance) launchAttempt(title string, p *prepared) *attempt {
	return in.startAttempt(title, func(ctx context.Context, h *tasks.Handle) error {
		return in.mgr.launchOnce(ctx, h, in, p)
	})
}

// Waits for an attempt and drops its cancel hook
func (in *instance) await(a *attempt) error {
	<-a.finished
	in.mu.Lock()
	if in.cancelAttempt != nil {
		in.cancelAttempt()
		in.cancelAttempt = nil
	}
	in.mu.Unlock()
	return a.err
}

// Cancels the attempt under way, so its process stops before ready
func (in *instance) cancelLaunch() {
	in.mu.Lock()
	cancel := in.cancelAttempt
	in.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Starts the reconciler, serving first when the process is already ready
func (in *instance) startLoop(first *attempt, serving bool) {
	in.mu.Lock()
	in.loopDone = make(chan struct{})
	done := in.loopDone
	in.mu.Unlock()
	go func() {
		defer close(done)
		in.loop(first, serving)
	}()
}

// Drives the process toward the desired state until the record is final or the daemon closes
func (in *instance) loop(first *attempt, serving bool) {
	if serving && !in.serve() {
		return
	}
	a := first
	for {
		if a == nil {
			next, ok := in.nextAttempt()
			if !ok {
				return
			}
			a = next
		}
		err := in.await(a)
		a = nil
		if err != nil {
			if !in.launchFailed(err) {
				return
			}
			continue
		}
		if !in.serve() {
			return
		}
	}
}

// Waits out the restart delay or an idle spell and starts the next launch, false once the instance is final
func (in *instance) nextAttempt() (*attempt, bool) {
	for {
		if in.stopWanted() {
			return nil, in.finish()
		}
		rec := in.snapshot()
		in.mu.Lock()
		woken := in.woken
		in.woken = false
		delay := in.delay
		in.mu.Unlock()
		if rec.GetState() == v1.InstanceState_INSTANCE_STATE_IDLE && !woken {
			<-in.poke
			continue
		}
		if woken {
			delay = 0
		}
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
				in.mu.Lock()
				in.delay = 0
				in.mu.Unlock()
			case <-in.poke:
				timer.Stop()
				if in.generationChanged() {
					in.mu.Lock()
					in.delay = 0
					in.mu.Unlock()
				}
				continue
			}
		}
		in.mu.Lock()
		p := in.prepared
		if in.next != nil && in.next.generation == in.rec.GetGeneration() {
			p = in.next
		}
		in.mu.Unlock()
		title := "restart " + rec.GetName()
		if woken {
			title = "wake " + rec.GetName()
		}
		return in.launchAttempt(title, p), true
	}
}

// Counts a launch that never reached ready, failing the instance after too many in a row, false once final
func (in *instance) launchFailed(err error) bool {
	m := in.mgr
	m.release(in)
	if in.stopWanted() {
		return in.finish()
	}
	rec := in.snapshot()
	in.mu.Lock()
	in.failures++
	failures := in.failures
	in.mu.Unlock()
	gen := rec.GetGeneration()
	if rec.GetSeat() != nil || failures >= maxFailedLaunches {
		reason := err.Error()
		if rec.GetSeat() == nil {
			reason = fmt.Sprintf("%d launches in a row failed, the last: %v", failures, err)
		}
		m.unpublish(context.Background(), in)
		m.dropRoute(context.Background(), in)
		in.update(func(r *v1.Instance) {
			r.State, r.Error, r.StoppedAt = v1.InstanceState_INSTANCE_STATE_FAILED, reason, timestamppb.Now()
			r.Conditions = SetCondition(r.Conditions, CondLaunched, false, ReasonFailedLaunches, reason, gen)
			r.Conditions = SetCondition(r.Conditions, CondReady, false, ReasonLaunchFailed, reason, gen)
		})
		return false
	}
	in.scheduleRestart()
	in.update(func(r *v1.Instance) {
		r.State, r.Error = v1.InstanceState_INSTANCE_STATE_STARTING, err.Error()
		r.Conditions = SetCondition(r.Conditions, CondLaunched, false, ReasonLaunchFailed, err.Error(), gen)
		r.Conditions = SetCondition(r.Conditions, CondReady, false, ReasonLaunchFailed, err.Error(), gen)
	})
	m.Log.Warn("instance launch failed, launching again", "name", rec.GetName(), "failures", failures, "in", in.pendingDelay(), "err", err)
	return true
}

// Sets the next restart delay from the backoff and doubles the backoff up to its cap
func (in *instance) scheduleRestart() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.delay = in.backoff
	in.backoff = min(in.backoff*2, backoffMax)
}

func (in *instance) pendingDelay() time.Duration {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.delay
}

// Ends the reconciler on a stop, stopped or failed after a drain crash, idle kept while the daemon closes
func (in *instance) finish() bool {
	m := in.mgr
	in.mu.Lock()
	closing, crash := in.closing, in.drainCrash
	in.drainCrash = ""
	in.mu.Unlock()
	rec := in.snapshot()
	if closing && rec.GetState() == v1.InstanceState_INSTANCE_STATE_IDLE {
		return false
	}
	gen := rec.GetGeneration()
	reason := ReasonStopped
	if closing {
		reason = ReasonShutdown
	}
	launched, launchedMsg := reason, ""
	if crash != "" {
		launched, launchedMsg = ReasonDrainCrashed, crash
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopSlack)
	defer cancel()
	m.unpublish(ctx, in)
	if !closing {
		m.dropRoute(ctx, in)
	}
	in.update(func(r *v1.Instance) {
		r.State, r.StoppedAt = v1.InstanceState_INSTANCE_STATE_STOPPED, timestamppb.Now()
		unhealthy(r)
		if crash != "" {
			r.State, r.Error = v1.InstanceState_INSTANCE_STATE_FAILED, crash
		}
		r.Conditions = SetCondition(r.Conditions, CondLaunched, false, launched, launchedMsg, gen)
		r.Conditions = SetCondition(r.Conditions, CondReady, false, reason, "", gen)
		r.Conditions = SetCondition(r.Conditions, CondDraining, false, reason, "", gen)
	})
	return false
}

// Closes once the process is gone, by its pidfd and by the handle that read its output and status
func watchExit(h launch.Handle) <-chan struct{} {
	out := make(chan struct{})
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		waited := make(chan struct{})
		go func() {
			proc.WaitExit(ctx, h.Pid())
			close(waited)
		}()
		select {
		case <-waited:
		case <-h.Done():
		}
		<-h.Done()
		close(out)
	}()
	return out
}

// Watches a ready process for health, idle windows, request changes, stops and crashes, false once final
func (in *instance) serve() bool {
	m := in.mgr
	h := in.handle()
	exit := watchExit(h)
	tick := time.NewTicker(in.healthInterval())
	defer tick.Stop()
	id := in.id()
	window := time.Duration(in.snapshot().GetRequest().GetIdleUnloadSeconds()) * time.Second
	active := time.Now()
	var count uint64
	if window > 0 {
		count = m.Dispatched(id)
	}
	for {
		select {
		case <-exit:
			return in.crashed(h)
		case <-in.poke:
			switch {
			case in.stopWanted():
				in.stopProcess(h, exit, ReasonStopRequested)
				return in.finish()
			case in.generationChanged():
				in.stopProcess(h, exit, ReasonRequestChanged)
				in.recordDrainCrash()
				return true
			}
		case <-tick.C:
			in.probe()
			if window <= 0 {
				continue
			}
			if n, c := m.InFlight(id), m.Dispatched(id); n > 0 || c != count {
				active, count = time.Now(), c
			}
			if time.Since(active) < window {
				continue
			}
			// The gateway sent nothing for a whole window, so the runtime says whether anything else did
			at, err := m.Load(context.Background(), id)
			if err != nil {
				m.Log.Warn("instance kept loaded, its runtime could not be asked whether it is idle", "id", id, "err", err)
				active = time.Now()
				continue
			}
			if at.After(active) {
				active = at
			}
			if time.Since(active) < window {
				continue
			}
			switch in.unload(h, exit) {
			case unloadFinal:
				return false
			case unloadStopped:
				return true
			}
			cont, awake := in.sleepThrough(h, exit)
			if !cont {
				return false
			}
			if !awake {
				return true
			}
			active, count = time.Now(), m.Dispatched(id)
		}
	}
}

// Moves a crash a drain saw from the pending stop onto the record, the instance relaunching after it
func (in *instance) recordDrainCrash() {
	in.mu.Lock()
	crash := in.drainCrash
	in.drainCrash = ""
	in.mu.Unlock()
	if crash != "" {
		in.update(func(r *v1.Instance) { r.Error = crash })
	}
}

// The probe interval recorded at launch or adoption, the runtime's own or the seat role's
func (in *instance) healthInterval() time.Duration {
	return in.healthOf().Interval
}

// Whether each rank answers its health path, or for a hello or process seat whether its process still runs
func (in *instance) answers() []bool {
	rec := in.snapshot()
	refs := rankRefs(rec)
	health := in.healthOf()
	if health.Kind != runtimes.HealthHTTP {
		select {
		case <-in.handle().Done():
			return []bool{false}
		default:
			return []bool{true}
		}
	}
	path := health.Path
	client := &http.Client{Timeout: max(in.healthInterval()*2, probeTimeout)}
	out := make([]bool, len(refs))
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = launch.Healthy(client, ref.endpoint+path)
		}()
	}
	wg.Wait()
	return out
}

// Probes every rank, taking a rank out of traffic after three failures in a row and back after one success
func (in *instance) probe() {
	m := in.mgr
	oks := in.answers()
	now := time.Now()
	in.mu.Lock()
	refs := rankRefs(in.rec)
	if len(in.healthFails) != len(refs) {
		in.healthFails = make([]int, len(refs))
	}
	all := len(oks) == len(refs)
	for i := range refs {
		if i < len(oks) && oks[i] {
			in.healthFails[i] = 0
			continue
		}
		in.healthFails[i]++
		all = false
	}
	if all {
		in.rec.LastHealthAt = timestamppb.New(now)
	}
	fails := slices.Clone(in.healthFails)
	gen, seat, id := in.rec.GetGeneration(), in.rec.GetSeat(), in.rec.GetId()
	write := all && now.Sub(in.healthWritten) >= healthRecordEvery
	if write {
		in.healthWritten = now
	}
	in.mu.Unlock()
	var down, up []int
	for i, ref := range refs {
		ok := i < len(oks) && oks[i]
		switch {
		case !ok && ref.healthy && fails[i] >= unhealthyAfter:
			down = append(down, i)
		case ok && !ref.healthy:
			up = append(up, i)
		}
	}
	if len(down) == 0 && len(up) == 0 {
		if write {
			in.flush()
		}
		return
	}
	in.update(func(r *v1.Instance) {
		for _, i := range down {
			setRankHealthy(r, i, false)
		}
		for _, i := range up {
			setRankHealthy(r, i, true)
		}
		if r.GetHealthy() {
			r.Conditions = SetCondition(r.Conditions, CondHealthy, true, ReasonProbeOK, "", gen)
		} else {
			r.Conditions = SetCondition(r.Conditions, CondHealthy, false, ReasonProbeFailed, unhealthyText(r), gen)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	for _, i := range down {
		m.Registrar.SetRankReady(ctx, id, PublishedRank(seat, len(refs), i), false)
		m.Log.Warn("instance rank unhealthy, out of traffic", "instance", id, "rank", i, "failures", fails[i])
	}
	for _, i := range up {
		m.Registrar.SetRankReady(ctx, id, PublishedRank(seat, len(refs), i), true)
		m.Log.Info("instance rank healthy again, back in traffic", "instance", id, "rank", i)
	}
}

// Records one rank's health, the instance healthy while every rank is
func setRankHealthy(r *v1.Instance, rank int, healthy bool) {
	if len(r.GetRanks()) == 0 {
		r.Healthy = healthy
		return
	}
	r.Ranks[rank].Healthy = healthy
	r.Healthy = !slices.ContainsFunc(r.GetRanks(), func(rr *v1.InstanceRank) bool { return !rr.GetHealthy() })
}

// Records the instance and every rank as answering no health
func unhealthy(r *v1.Instance) {
	r.Healthy = false
	for _, rr := range r.GetRanks() {
		rr.Healthy = false
	}
}

// Why an instance is out of traffic, naming each rank out of it when it runs several
func unhealthyText(r *v1.Instance) string {
	if len(r.GetRanks()) == 0 {
		return fmt.Sprintf("%d health probes in a row failed", unhealthyAfter)
	}
	var out []string
	for _, rr := range r.GetRanks() {
		if !rr.GetHealthy() {
			out = append(out, strconv.Itoa(int(rr.GetRank())))
		}
	}
	return fmt.Sprintf("ranks %s failed %d health probes in a row, the other ranks serve on", strings.Join(out, ", "), unhealthyAfter)
}

// Takes the instance out of traffic and waits for its requests in flight, true when the process crashed meanwhile
func (in *instance) drain(h launch.Handle, exit <-chan struct{}) bool {
	m := in.mgr
	rec := in.snapshot()
	ctx, cancel := in.stopContext()
	defer cancel()
	in.mu.Lock()
	published := in.published
	in.mu.Unlock()
	switch {
	case rec.GetSeat() != nil:
		m.Registrar.SetReady(ctx, rec.GetId(), false)
	case published:
		m.Registrar.Drain(ctx, ownerOf(rec.GetId()))
	}
	deadline := time.NewTimer(m.drainTimeout())
	defer deadline.Stop()
	tick := time.NewTicker(drainPoll)
	defer tick.Stop()
	for m.InFlight(rec.GetId()) > 0 {
		select {
		case <-exit:
			return in.exitedDraining(h)
		case <-deadline.C:
			m.Log.Warn("requests still in flight when the instance stops", "instance", rec.GetName(), "in_flight", m.InFlight(rec.GetId()))
			return false
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
	select {
	case <-exit:
		return in.exitedDraining(h)
	default:
	}
	return false
}

// Records an exit seen while draining as a crash unless the process stopped cleanly
func (in *instance) exitedDraining(h launch.Handle) bool {
	err := h.Err()
	if err == nil {
		return false
	}
	hits, measurements := in.scanLog()
	detail := "exited while draining: " + err.Error()
	gen := in.snapshot().GetGeneration()
	in.update(func(r *v1.Instance) {
		r.Triage = hits
		r.Measurements = mergeMeasurements(r.Measurements, measurements)
		r.Conditions = SetCondition(r.Conditions, CondLaunched, false, ReasonDrainCrashed, detail, gen)
	})
	in.mu.Lock()
	in.drainCrash = detail
	in.mu.Unlock()
	in.mgr.Log.Warn("instance crashed while draining", "instance", in.id(), "err", err)
	return true
}

// Drains then stops the process, every stop path going through here, true when it crashed while draining
func (in *instance) stopProcess(h launch.Handle, exit <-chan struct{}, reason string) bool {
	m := in.mgr
	gen := in.snapshot().GetGeneration()
	in.update(func(r *v1.Instance) {
		r.State = v1.InstanceState_INSTANCE_STATE_DRAINING
		r.Conditions = SetCondition(r.Conditions, CondDraining, true, reason, "", gen)
		r.Conditions = SetCondition(r.Conditions, CondReady, false, reason, "", gen)
	})
	crashed := in.drain(h, exit)
	in.update(func(r *v1.Instance) { r.State = v1.InstanceState_INSTANCE_STATE_STOPPING })
	ctx, cancel := in.stopContext()
	defer cancel()
	if err := h.Stop(ctx, in.grace()); err != nil {
		m.Log.Warn("instance did not stop in time", "instance", in.id(), "err", err)
	}
	m.unpublish(ctx, in)
	m.release(in)
	in.update(func(r *v1.Instance) {
		unhealthy(r)
		r.Conditions = SetCondition(r.Conditions, CondDraining, false, reason, "", gen)
	})
	m.probeHost()
	return crashed
}

// Records a crash of a running process and schedules its restart, false once final
func (in *instance) crashed(h launch.Handle) bool {
	m := in.mgr
	err := h.Err()
	detail := "exited"
	if err != nil {
		detail = "exited: " + err.Error()
	}
	hits, measurements := in.scanLog()
	ctx, cancel := context.WithTimeout(context.Background(), stopSlack)
	defer cancel()
	m.unpublish(ctx, in)
	m.release(in)
	m.probeHost()
	rec := in.snapshot()
	gen := rec.GetGeneration()
	m.Log.Warn("instance exited", "name", rec.GetName(), "err", err, "triage", len(hits))
	record := func(r *v1.Instance) {
		r.Error, r.Triage = detail, hits
		unhealthy(r)
		r.Measurements = mergeMeasurements(r.Measurements, measurements)
		r.Conditions = SetCondition(r.Conditions, CondLaunched, false, ReasonCrashed, detail, gen)
		r.Conditions = SetCondition(r.Conditions, CondReady, false, ReasonCrashed, detail, gen)
		r.Conditions = SetCondition(r.Conditions, CondHealthy, false, ReasonCrashed, detail, gen)
	}
	if rec.GetSeat() != nil || in.stopWanted() && !in.isClosing() {
		m.dropRoute(ctx, in)
		in.update(func(r *v1.Instance) {
			record(r)
			r.State, r.StoppedAt = v1.InstanceState_INSTANCE_STATE_FAILED, timestamppb.Now()
		})
		return false
	}
	if in.isClosing() {
		in.update(func(r *v1.Instance) {
			record(r)
			r.State, r.StoppedAt = v1.InstanceState_INSTANCE_STATE_STOPPED, timestamppb.Now()
		})
		return false
	}
	in.mu.Lock()
	if !in.readyAt.IsZero() && time.Since(in.readyAt) >= backoffReset {
		in.backoff = backoffMin
	}
	in.asleep = false
	in.mu.Unlock()
	in.scheduleRestart()
	idle := rec.GetState() == v1.InstanceState_INSTANCE_STATE_IDLE
	in.update(func(r *v1.Instance) {
		record(r)
		r.Restarts++
		if !idle {
			r.State = v1.InstanceState_INSTANCE_STATE_STARTING
		}
	})
	return true
}

// What an idle unload left behind
type unloaded int

const (
	// The instance is final
	unloadFinal unloaded = iota
	// The process is gone and the instance waits idle for a wake
	unloadStopped
	// The process sleeps in place
	unloadAsleep
)

// Unloads an idle process, asleep in place when the runtime sleeps, else drained and stopped
func (in *instance) unload(h launch.Handle, exit <-chan struct{}) unloaded {
	m := in.mgr
	rec := in.snapshot()
	gen := rec.GetGeneration()
	sleeper := sleeperOf(in.rt, rec)
	if sleeper == nil {
		return in.unloadByStop(h, exit, "")
	}
	in.update(func(r *v1.Instance) {
		r.State = v1.InstanceState_INSTANCE_STATE_DRAINING
		r.Conditions = SetCondition(r.Conditions, CondDraining, true, ReasonIdle, "", gen)
	})
	if in.drain(h, exit) {
		in.recordDrainCrash()
		if in.crashed(h) {
			return unloadStopped
		}
		return unloadFinal
	}
	ctx, cancel := in.stopContext()
	defer cancel()
	if err := sleeper.Sleep(ctx); err != nil {
		m.Log.Warn("instance did not go to sleep, unloaded by stopping instead", "name", rec.GetName(), "err", err)
		return in.unloadByStop(h, exit, "sleep failed, unloaded by stopping: "+err.Error())
	}
	m.unpublish(ctx, in)
	in.mu.Lock()
	in.asleep = true
	in.mu.Unlock()
	in.update(func(r *v1.Instance) {
		r.State = v1.InstanceState_INSTANCE_STATE_IDLE
		r.Conditions = SetCondition(r.Conditions, CondReady, false, ReasonAsleep, "", gen)
		r.Conditions = SetCondition(r.Conditions, CondDraining, false, ReasonAsleep, "", gen)
	})
	m.Log.Info("instance asleep after its idle window", "name", rec.GetName())
	return unloadAsleep
}

// Drains and stops the process for its idle window, the instance waiting idle for a wake with why it stopped recorded
func (in *instance) unloadByStop(h launch.Handle, exit <-chan struct{}, why string) unloaded {
	m := in.mgr
	rec := in.snapshot()
	gen := rec.GetGeneration()
	in.stopProcess(h, exit, ReasonIdle)
	in.recordDrainCrash()
	in.update(func(r *v1.Instance) {
		r.State = v1.InstanceState_INSTANCE_STATE_IDLE
		if why != "" {
			r.Error = why
		}
		r.Conditions = SetCondition(r.Conditions, CondLaunched, false, ReasonIdle, why, gen)
		r.Conditions = SetCondition(r.Conditions, CondReady, false, ReasonIdle, why, gen)
	})
	m.Log.Info("instance unloaded after its idle window", "name", rec.GetName())
	if in.stopWanted() && !in.finish() {
		return unloadFinal
	}
	return unloadStopped
}

// Holds a sleeping process until a wake, stop, request change or crash, reporting finality and whether it serves again
func (in *instance) sleepThrough(h launch.Handle, exit <-chan struct{}) (cont, awake bool) {
	m := in.mgr
	for {
		select {
		case <-exit:
			return in.crashed(h), false
		case <-in.poke:
		}
		switch {
		case in.stopWanted():
			in.stopProcess(h, exit, ReasonStopRequested)
			if in.isClosing() {
				// A daemon closing keeps the instance idle, so the next one waits for a wake as well
				in.update(func(r *v1.Instance) { r.State = v1.InstanceState_INSTANCE_STATE_IDLE })
			}
			return in.finish(), false
		case in.generationChanged():
			in.stopProcess(h, exit, ReasonRequestChanged)
			in.recordDrainCrash()
			return true, false
		}
		in.mu.Lock()
		woken := in.woken
		in.woken = false
		in.mu.Unlock()
		if !woken {
			continue
		}
		rec := in.snapshot()
		gen := rec.GetGeneration()
		ctx, cancel := context.WithTimeout(context.Background(), modesTimeout)
		err := sleeperOf(in.rt, rec).Wake(ctx)
		if err == nil {
			err = m.publish(ctx, in)
		}
		cancel()
		if err != nil {
			m.Log.Warn("sleeping instance did not wake, stopping it to launch again", "name", rec.GetName(), "err", err)
			in.stopProcess(h, exit, ReasonIdle)
			in.recordDrainCrash()
			in.mu.Lock()
			in.asleep, in.woken = false, true
			in.mu.Unlock()
			msg := "wake failed: " + err.Error()
			in.update(func(r *v1.Instance) {
				r.Error = msg
				r.Conditions = SetCondition(r.Conditions, CondReady, false, ReasonIdle, msg, gen)
			})
			return true, false
		}
		in.mu.Lock()
		in.asleep = false
		in.mu.Unlock()
		in.update(func(r *v1.Instance) {
			r.State = v1.InstanceState_INSTANCE_STATE_READY
			r.Conditions = SetCondition(r.Conditions, CondReady, true, ReasonPublished, "", gen)
		})
		m.Log.Info("instance woke from sleep", "name", rec.GetName())
		return true, true
	}
}

// Reads the log for triage hits and the allocations the runtime reported
func (in *instance) scanLog() ([]*v1.TriageHit, []*v1.Measurement) {
	in.mu.Lock()
	log, h := in.log, in.proc
	in.mu.Unlock()
	if h != nil {
		h.Sync()
	}
	if log == nil || in.rt == nil {
		return nil, nil
	}
	lines := log.Tail(0)
	return triage.Scan(in.rt.Triage(), lines), in.rt.Measure(lines)
}

// Probes the host again, unless the daemon is on its way out
func (m *Manager) probeHost() {
	m.mu.Lock()
	closing := m.closing
	m.mu.Unlock()
	if m.Probe != nil && !closing {
		m.Probe()
	}
}

// Stops an instance by request, clearing relaunch intent, and waits for it to be final
func (m *Manager) Stop(ctx context.Context, id string) (*v1.Instance, error) {
	in, err := m.find(id)
	if err != nil {
		return nil, err
	}
	if Terminal(in.snapshot().GetState()) {
		return in.snapshot(), nil
	}
	in.update(func(r *v1.Instance) { r.DesiredRunning = false })
	in.mu.Lock()
	looping := in.loopDone != nil
	in.mu.Unlock()
	if !looping {
		in.finish()
		return in.snapshot(), nil
	}
	in.cancelLaunch()
	in.kick()
	select {
	case <-in.exited:
	case <-ctx.Done():
		return in.snapshot(), ctx.Err()
	}
	return in.snapshot(), nil
}

// Wakes an idle instance an owner names, the next request finding it loading
func (m *Manager) Wake(ctx context.Context, owner pools.OwnerRef) error {
	if owner.Kind != pools.OwnerInstance {
		return fmt.Errorf("%w: instances wake instance owners, not %s", runtimes.ErrParam, owner.Kind)
	}
	in, err := m.find(owner.ID)
	if err != nil {
		return err
	}
	return in.wake()
}

// Asks the reconciler to load an idle instance again
func (in *instance) wake() error {
	rec := in.snapshot()
	switch {
	case Terminal(rec.GetState()):
		return fmt.Errorf("%w: %s is %s", runtimes.ErrParam, rec.GetName(), text.Enum(rec.GetState()))
	case rec.GetState() != v1.InstanceState_INSTANCE_STATE_IDLE:
		return nil
	}
	in.mu.Lock()
	in.woken = true
	in.mu.Unlock()
	in.kick()
	return nil
}

// Stops every live process for shutdown through the drain path, keeping relaunch intent, bounded by its own deadline
func (m *Manager) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	m.mu.Lock()
	m.closing = true
	list := append([]*instance(nil), m.list...)
	m.mu.Unlock()
	var waits []chan struct{}
	for _, in := range list {
		in.mu.Lock()
		in.closing, in.closeCtx = true, ctx
		done := in.loopDone
		in.mu.Unlock()
		if done == nil {
			continue
		}
		in.cancelLaunch()
		in.kick()
		waits = append(waits, done)
	}
	for _, done := range waits {
		select {
		case <-done:
		case <-ctx.Done():
			m.Log.Warn("instances still stopping when the close deadline passed")
			return
		}
	}
}

// Records a failed launch's triage and measurements and returns the error with its first hint
func (m *Manager) launchError(in *instance, err error) error {
	hits, measurements := in.scanLog()
	in.update(func(r *v1.Instance) {
		r.Triage = hits
		r.Measurements = mergeMeasurements(r.Measurements, measurements)
	})
	if len(hits) > 0 {
		return fmt.Errorf("%s: %s: %w", hits[0].GetSummary(), hits[0].GetHint(), err)
	}
	return err
}

// Whether the error is a stop or a close cancelling the launch
func cancelled(ctx context.Context, err error) bool {
	return errors.Is(err, errStopped) || ctx.Err() != nil
}

// The log lines of a handle read so far
func linesOf(h launch.Handle) []string {
	h.Sync()
	return h.Log().Tail(0)
}

// Logs every triage hit of a failed launch on its task
func logHits(h *tasks.Handle, in *instance) {
	for _, hit := range in.snapshot().GetTriage() {
		h.Logf("triage %s: %s. %s", hit.GetId(), hit.GetSummary(), hit.GetHint())
	}
}

// Joins a command for the task log
func commandLine(words []string) string { return strings.Join(words, " ") }
