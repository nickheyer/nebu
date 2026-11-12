package slots

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/pools"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Points or percent the canary may trail the occupant by when the swap names no bound
const defaultRegression = 10

// Reasons the Canary condition carries through a canary swap and after it ends
const (
	CanaryLaunching   = "Launching"
	CanaryShifting    = "Shifting"
	CanaryPromoting   = "Promoting"
	CanaryPromoted    = "Promoted"
	CanaryRollingBack = "RollingBack"
	CanaryRolledBack  = "RolledBack"
	CanaryAbandoned   = "Abandoned"
)

// Request counters per pool and target model, the gateway's in a daemon
type Outcomes interface {
	Outcome(pool datalayer.PoolID, model string) pools.Outcome
}

// Waits out canary steps, the system clock in a daemon
type Clock interface {
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func (m *Manager) clock() Clock {
	if m.Clock != nil {
		return m.Clock
	}
	return systemClock{}
}

// The shares and bounds of one canary swap
type canaryPlan struct {
	// Percent the canary takes in its first step and adds after each step it passes
	first, step uint32
	every       time.Duration
	// Points of error rate and attainment, and percent of mean latency, the canary may trail by
	slack uint32
}

// The slot's Canary condition while a canary runs and after it ends
type phase struct {
	active          bool
	reason, message string
}

// The canary a swap asks for, false for none, refusing canary options without a share or a step length
func canaryOf(req *v1.SwapRequest) (canaryPlan, bool, error) {
	bounded := req.CanaryMaxRegressionPercent != nil
	p := canaryPlan{first: req.GetCanaryPercent(), step: req.GetCanaryStepPercent(), every: time.Duration(req.GetCanarySeconds()) * time.Second, slack: defaultRegression}
	if bounded {
		p.slack = req.GetCanaryMaxRegressionPercent()
	}
	switch {
	case p.first == 0 && (p.every > 0 || p.step > 0 || bounded):
		return p, false, fmt.Errorf("%w: canary seconds, step and regression bound shape the canary a canary percent opens, set one", ErrSlot)
	case p.first == 0:
		return p, false, nil
	case p.first > 99 || p.step > 99:
		return p, false, fmt.Errorf("%w: canary percent %d and step percent %d must stay under 100", ErrSlot, p.first, p.step)
	case p.every <= 0:
		return p, false, fmt.Errorf("%w: a canary needs the seconds each step lasts", ErrSlot)
	case p.slack > 100:
		return p, false, fmt.Errorf("%w: canary regression bound %d is over 100", ErrSlot, p.slack)
	}
	return p, true, nil
}

// The canary's share in each step, growing by the step percent while it stays under 100
func (p canaryPlan) shares() []uint32 {
	out := []uint32{p.first}
	for p.step > 0 {
		next := out[len(out)-1] + p.step
		if next >= 100 {
			break
		}
		out = append(out, next)
	}
	return out
}

// Writes a canary phase into the slot's Canary condition
func (m *Manager) setPhase(id string, p phase) {
	m.update(id, func(*v1.Slot) { m.phaseLocked(id, p) })
}

// Records a canary phase, the caller holding the lock
func (m *Manager) phaseLocked(id string, p phase) {
	if m.phases == nil {
		m.phases = map[string]phase{}
	}
	m.phases[id] = p
}

// Forgets an earlier canary's phase and condition, the caller holding the lock
func (m *Manager) clearPhaseLocked(s *v1.Slot) {
	delete(m.phases, s.GetId())
	s.Conditions = slices.DeleteFunc(s.Conditions, func(c *v1.Condition) bool { return c.GetType() == CondCanary })
}

// Launches the canary beside the occupant, shifts traffic to it step by step, then promotes it or rolls back
func (m *Manager) swapCanary(ctx context.Context, h *tasks.Handle, s *v1.Slot, old occupant, run *v1.RunRequest, mesh bool, plan canaryPlan) error {
	shares := plan.shares()
	total := uint64(len(shares) + 2)
	h.Progress(0, total, "starting canary "+run.GetName())
	fresh, err := m.launchFresh(ctx, h, s, run, mesh, true)
	if err != nil {
		return m.rollBack(ctx, h, s, old, fresh, fmt.Sprintf("the canary did not start: %v", err))
	}
	was, now := m.target(ctx, old, 0), m.target(ctx, fresh, 0)
	if why := blended(was, now); why != "" {
		return m.rollBack(ctx, h, s, old, fresh, why)
	}
	base := [2]pools.Outcome{m.Outcomes.Outcome(was.Pool, was.Model), m.Outcomes.Outcome(now.Pool, now.Model)}
	for i, share := range shares {
		if err := m.splitRoutes(ctx, s, []pools.WeightedTarget{m.target(ctx, old, 100-share), m.target(ctx, fresh, share)}); err != nil {
			return m.rollBack(ctx, h, s, old, fresh, "routes not split: "+err.Error())
		}
		msg := fmt.Sprintf("step %d of %d sends %d%% of traffic to %s for %s", i+1, len(shares), share, fresh.name(), plan.every)
		m.setPhase(s.GetId(), phase{active: true, reason: CanaryShifting, message: msg})
		h.ProgressUntil(uint64(i+1), total, msg, time.Now().Add(plan.every))
		h.Logf("%s", msg)
		if err := m.hold(ctx, fresh, plan.every); err != nil {
			return m.rollBack(ctx, h, s, old, fresh, fmt.Sprintf("step %d: %v", i+1, err))
		}
		occ, can := m.Outcomes.Outcome(was.Pool, was.Model).Since(base[0]), m.Outcomes.Outcome(now.Pool, now.Model).Since(base[1])
		if why := regressed(occ, can, plan.slack); why != "" {
			return m.rollBack(ctx, h, s, old, fresh, fmt.Sprintf("step %d: %s", i+1, why))
		}
		h.Logf("step %d held up, the canary served %d requests with %d errors and the occupant %d with %d", i+1, can.Requests, can.Errors, occ.Requests, occ.Errors)
	}
	m.take(s.GetId(), run, mesh)
	return m.promote(ctx, h, s, old, fresh, len(shares), total)
}

// Why a split cannot tell the canary's requests from the occupant's, empty when their targets differ
func blended(was, now pools.WeightedTarget) string {
	if was.Pool != now.Pool || was.Model != now.Model {
		return ""
	}
	return fmt.Sprintf("the canary serves as %s in the occupant's pool %s, so no split tells them apart, give it another served_model_name", now.Model, now.Pool)
}

// Waits out one step on the clock, ending early with why once the canary stops serving or the swap ends
func (m *Manager) hold(ctx context.Context, fresh occupant, every time.Duration) error {
	done := m.clock().After(every)
	tick := time.NewTicker(canaryPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("the swap was cancelled: %w", ctx.Err())
		case <-done:
			if ok, why := m.serving(fresh); !ok {
				return fmt.Errorf("the canary stopped serving, %s", why)
			}
			return nil
		case <-tick.C:
			if ok, why := m.serving(fresh); !ok {
				return fmt.Errorf("the canary stopped serving, %s", why)
			}
		}
	}
}

// Why the canary's counts trail the occupant's past slack, empty when every measure holds up
func regressed(occ, can pools.Outcome, slack uint32) string {
	bound := float64(slack)
	switch {
	case can.Requests == 0:
		return "the canary served no requests"
	case occ.Requests == 0:
		return "the occupant served no requests to compare the canary against"
	}
	if c, o := percent(can.Errors, can.Requests), percent(occ.Errors, occ.Requests); c-o > bound {
		return fmt.Sprintf("error rate %.1f%% against the occupant's %.1f%%", c, o)
	}
	if why := paired("requests carrying an objective", can.Judged, occ.Judged); why != "" {
		return why
	}
	if can.Judged > 0 {
		if c, o := percent(can.Met, can.Judged), percent(occ.Met, occ.Judged); o-c > bound {
			return fmt.Sprintf("met %.1f%% of objectives against the occupant's %.1f%%", c, o)
		}
	}
	for _, l := range []struct {
		name        string
		canSum, occ time.Duration
		canN, occN  uint64
	}{
		{"time to first token", can.TTFT, occ.TTFT, can.TTFTs, occ.TTFTs},
		{"time per output token", can.TPOT, occ.TPOT, can.TPOTs, occ.TPOTs},
	} {
		if why := paired(l.name+" samples", l.canN, l.occN); why != "" {
			return why
		}
		if l.canN == 0 {
			continue
		}
		c, o := l.canSum/time.Duration(l.canN), l.occ/time.Duration(l.occN)
		if float64(c) > float64(o)*(1+bound/100) {
			return fmt.Sprintf("mean %s %s against the occupant's %s", l.name, c, o)
		}
	}
	return ""
}

func percent(part, whole uint64) float64 { return float64(part) * 100 / float64(whole) }

// Why one side measured something the other did not, empty when both or neither did
func paired(what string, can, occ uint64) string {
	switch {
	case can == 0 && occ > 0:
		return fmt.Sprintf("the canary served no %s while the occupant served %d", what, occ)
	case occ == 0 && can > 0:
		return fmt.Sprintf("the occupant served no %s to compare the canary's %d against", what, can)
	}
	return ""
}

// Points every name back at the occupant, stops the canary, and settles the slot on the occupant
func (m *Manager) rollBack(ctx context.Context, h *tasks.Handle, s *v1.Slot, old, fresh occupant, why string) error {
	ctx = context.WithoutCancel(ctx)
	h.Logf("rolling back, %s", why)
	m.setPhase(s.GetId(), phase{active: true, reason: CanaryRollingBack, message: why})
	if err := m.occupy(ctx, s, old); err != nil {
		h.Logf("routes not pointed back at %s: %v", old.name(), err)
	}
	m.stopFailed(h, fresh)
	m.setPhase(s.GetId(), phase{reason: CanaryRolledBack, message: why})
	m.settle(ctx, s, &old, "canary rolled back: "+why)
	return fmt.Errorf("canary rolled back: %s", why)
}

// Points every name at the canary, drains and stops the occupant, and settles the slot on the canary
func (m *Manager) promote(ctx context.Context, h *tasks.Handle, s *v1.Slot, old, fresh occupant, steps int, total uint64) error {
	msg := fmt.Sprintf("%s takes all traffic, draining %s", fresh.name(), old.name())
	m.setPhase(s.GetId(), phase{active: true, reason: CanaryPromoting, message: msg})
	h.Progress(total-1, total, msg)
	if err := m.occupy(ctx, s, fresh); err != nil {
		h.Logf("routes not pointed at %s: %v", fresh.name(), err)
	}
	retireErr := m.retire(ctx, old)
	note := ""
	if retireErr != nil {
		note = fmt.Sprintf("the old occupant %s did not stop: %v", old.name(), retireErr)
		h.Logf("%s", note)
	}
	m.setPhase(s.GetId(), phase{reason: CanaryPromoted, message: fmt.Sprintf("%s held up through step %d and took all traffic", fresh.name(), steps)})
	m.settle(ctx, s, &fresh, note)
	h.Progress(total, total, "ready")
	return retireErr
}

// Stops the canary instances and formations a daemon died during and marks the canary abandoned
func (m *Manager) abandonCanary(ctx context.Context, s *v1.Slot) {
	var stopped, kept []string
	stop := func(name string, err error) {
		if err != nil {
			kept = append(kept, fmt.Sprintf("%s (%v)", name, err))
			return
		}
		stopped = append(stopped, name)
	}
	for _, in := range m.Instances.InSlot(s.GetId()) {
		if in.GetId() == s.GetInstanceId() || in.GetSeat().GetFormationId() != "" || instances.Terminal(in.GetState()) {
			continue
		}
		_, err := m.Instances.Stop(ctx, in.GetId())
		stop(in.GetName(), err)
	}
	if m.Formations != nil {
		for _, f := range m.Formations.List(true) {
			if f.GetSlotId() != s.GetId() || f.GetId() == s.GetFormationId() {
				continue
			}
			_, err := m.Formations.Stop(ctx, f.GetId())
			stop("formation "+f.GetName(), err)
		}
	}
	msg := "the daemon restarted during the canary, the occupant keeps every name"
	if len(stopped) > 0 {
		msg += ", stopped " + strings.Join(stopped, ", ")
	}
	if len(kept) > 0 {
		msg += ", did not stop " + strings.Join(kept, ", ")
		m.Log.Warn("abandoned canary not stopped", "slot", s.GetName(), "canaries", kept)
	}
	m.setPhase(s.GetId(), phase{reason: CanaryAbandoned, message: msg})
}
