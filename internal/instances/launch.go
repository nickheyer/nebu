package instances

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/launch"
	"github.com/nickheyer/nebu/pkg/proc"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultPrepareTimeout = time.Hour
	// Preparation log capacity, matching the launcher ring
	logCapacity = 5000
	// Launches on fresh reservations one attempt makes when another process took a port first
	maxPortTries = 5
	// Gap between reads of a diffusion runtime's capabilities while they are not listed yet
	modesPoll = time.Second
)

// Resolves the inputs for the record's generation unless the given ones match, then launches the process to ready
func (m *Manager) launchOnce(ctx context.Context, h *tasks.Handle, in *instance, p *prepared) error {
	rec := in.snapshot()
	gen := rec.GetGeneration()
	if p == nil || p.generation != gen {
		h.Message("planning")
		var err error
		if p, err = m.prepare(ctx, rec.GetRequest(), rec.GetId()); err != nil {
			in.update(func(r *v1.Instance) {
				r.Conditions = SetCondition(r.Conditions, CondScheduled, false, ReasonPlanFailed, err.Error(), gen)
			})
			return err
		}
		p.generation = gen
		in.update(func(r *v1.Instance) {
			r.Plan, r.InstallId = p.plan, p.install.GetId()
			r.Conditions = SetCondition(r.Conditions, CondScheduled, true, ReasonPlanned, planDetail(p), gen)
		})
	}
	in.mu.Lock()
	in.prepared = p
	if in.next == p {
		in.next = nil
	}
	in.mu.Unlock()
	if p.warning != "" {
		h.Logf("plan: %s", p.warning)
	}
	in.update(func(r *v1.Instance) {
		r.State = v1.InstanceState_INSTANCE_STATE_STARTING
		r.Conditions = SetCondition(r.Conditions, CondLaunched, false, ReasonLaunching, "", gen)
		r.Conditions = SetCondition(r.Conditions, CondReady, false, ReasonStarting, "", gen)
		r.Conditions = SetCondition(r.Conditions, CondDraining, false, ReasonStarting, "", gen)
	})
	if p.seat != nil {
		return m.launchSeat(ctx, h, in, p)
	}
	return m.launchSolo(ctx, h, in, p)
}

// Params as the runtime records them, each value spelled as text, for the port span and protocol before rendering
func stringParams(p estimate.Params) map[string]string {
	out := make(map[string]string, len(p))
	for k, v := range p {
		switch x := v.(type) {
		case nil:
		case string:
			out[k] = x
		case bool:
			out[k] = strconv.FormatBool(x)
		default:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

// Renders the command a run would launch with as a launch renders it, the port zero until a launch takes one
func (m *Manager) Preview(ctx context.Context, req *v1.RunRequest) ([]string, map[string]string, error) {
	p, err := m.prepare(ctx, req, "")
	if err != nil {
		return nil, nil, err
	}
	if p.seat != nil {
		return nil, nil, fmt.Errorf("%w: a seat's command is rendered by its conductor at launch", runtimes.ErrParam)
	}
	input := p.input()
	input.Artifacts, input.Host = m.artifacts(p.stored, p.rt), bindHost
	if p.rt.Prepares(p.stored.GetFormatId()) && input.Artifacts["prepared_dir"] != "" {
		launchArtifacts := maps.Clone(input.Artifacts)
		launchArtifacts["weights_dir"] = input.Artifacts["prepared_dir"]
		input.Artifacts = launchArtifacts
	}
	cmds, err := renderRanks(input, p.rt.Ranks(stringParams(p.params)), p.rt.Launch, func(in runtimes.Launch, cmd *runtimes.Command) error {
		extra, err := runtimes.RenderArgs(runtimes.JoinArgs(p.res.args(), p.req.GetArgs()), runtimes.ScopeOf(in, cmd))
		cmd.Args = append(cmd.Args, extra...)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	rendered := cmds[0]
	return auth.RedactEnv(append([]string{rendered.Command}, rendered.Args...), rendered.Env), rendered.Params, nil
}

// Renders a solo run, launches it on an ephemeral or reserved port, waits for health, and relaunches on fresh ports when one was taken
func (m *Manager) launchSolo(ctx context.Context, h *tasks.Handle, in *instance, p *prepared) error {
	stored, rt, install := p.stored, p.rt, p.install
	key := store.Key(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup())
	unlock := m.Store.Lock(key)
	touched, err := m.Store.Touch(stored.GetSourceId(), stored.GetRepo(), stored.GetGroup())
	unlock()
	if err != nil {
		return fmt.Errorf("%s %s is no longer stored, pull it again: %w", stored.GetRepo(), stored.GetGroup(), err)
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_MODEL, v1.EventAction_EVENT_ACTION_UPDATED, key, touched)
	artifacts := m.artifacts(stored, rt)
	input := p.input()
	input.Artifacts, input.Host = artifacts, bindHost
	if rt.Prepares(stored.GetFormatId()) {
		if artifacts["prepared_dir"] == "" {
			return fmt.Errorf("%s needs a prepared tree for %s and the store has none", rt.ID(), stored.GetGroup())
		}
		prep, err := rt.Prepare(input)
		if err != nil {
			return err
		}
		if err := m.runPrepare(ctx, h, in, prep, install.GetDir(), artifacts["prepared_dir"], rt.PrepareTimeout()); err != nil {
			return err
		}
		launchArtifacts := maps.Clone(artifacts)
		launchArtifacts["weights_dir"] = artifacts["prepared_dir"]
		input.Artifacts = launchArtifacts
	}
	in.setHealth(runtimes.RoleHealth{Kind: runtimes.HealthHTTP, Path: rt.Health().Path, Interval: rt.Health().Interval, SlowAfter: rt.Health().SlowAfter})
	ranks := rt.Ranks(stringParams(p.params))
	span := rt.PortSpan(input)
	// One process binding one port asks the kernel for it and nebu reads the port back, so no reservation can be raced
	ephemeral := ranks == 1 && span == 1
	image := m.snapshotKey(p)
	if p.snapshot != nil {
		handle, restored, err := m.restoreSnapshot(ctx, h, in, p, image)
		if err != nil {
			return err
		}
		if restored {
			return m.becomeReady(ctx, h, in, p, handle, image, true)
		}
	}
	var handle launch.Handle
	for try := 1; ; try++ {
		port := 0
		if !ephemeral {
			if port, err = m.reserve(in, bindHost, span); err != nil {
				return err
			}
		}
		input.Port = port
		cmds, err := renderRanks(input, ranks, rt.Launch, func(in runtimes.Launch, cmd *runtimes.Command) error {
			extra, err := runtimes.RenderArgs(runtimes.JoinArgs(p.res.args(), p.req.GetArgs()), runtimes.ScopeOf(in, cmd))
			cmd.Args = append(cmd.Args, extra...)
			return err
		})
		if err != nil {
			m.release(in)
			return err
		}
		rendered := cmds[0]
		inventory, err := rt.Inventory(input, rendered.Params)
		if err != nil {
			m.release(in)
			return err
		}
		in.update(func(r *v1.Instance) {
			r.Params = rendered.Params
			r.Command = auth.RedactEnv(append([]string{rendered.Command}, rendered.Args...), rendered.Env)
			r.Endpoint = fmt.Sprintf("http://%s", hostPort(bindHost, rendered.Port))
			r.Ranks = rankRecords(cmds, bindHost, nil)
			r.InstallId = install.GetId()
			r.Inventory = inventory
		})
		m.yield(in)
		if handle, err = m.startRanks(ctx, h, in, cmds, install.GetDir()); err != nil {
			m.release(in)
			return err
		}
		if ephemeral {
			err = m.waitEphemeral(ctx, h, in, handle)
		} else {
			err = m.waitReady(ctx, h, in, handle)
		}
		if err == nil {
			break
		}
		if cancelled(ctx, err) {
			return errStopped
		}
		if !ephemeral && exitedNow(handle) && runtimes.PortTaken(rt, span, port, linesOf(handle)) {
			m.release(in)
			if try < maxPortTries {
				h.Logf("a port from %d was taken before %s bound it, launching again on another reservation, try %d of %d", port, rt.ID(), try+1, maxPortTries)
				continue
			}
			return fmt.Errorf("%s on %d reservations in a row: %w", rt.PortRule().Summary, maxPortTries, err)
		}
		m.release(in)
		err = m.launchError(in, err)
		logHits(h, in)
		return err
	}
	m.confirm(in)
	return m.becomeReady(ctx, h, in, p, handle, image, false)
}

// The ports a process tree listens on, through the reader a test installs or the kernel's socket table
func (m *Manager) listening(pid int) ([]int, error) {
	if m.Listening != nil {
		return m.Listening(pid)
	}
	return proc.ListeningPorts(pid)
}

// Waits for the process to bind a port and answer health on it, recording the port it chose as the endpoint
func (m *Manager) waitEphemeral(ctx context.Context, h *tasks.Handle, in *instance, handle launch.Handle) error {
	health := in.healthOf()
	rec := in.snapshot()
	client := &http.Client{Timeout: health.Interval * 2}
	slowAfter := health.SlowAfter
	if slowAfter <= 0 {
		slowAfter = 10 * time.Minute
	}
	h.Message("waiting for " + rec.GetName() + " to bind a port and answer " + health.Path)
	start := time.Now()
	warn := time.NewTicker(slowAfter)
	defer warn.Stop()
	tick := time.NewTicker(health.Interval)
	defer tick.Stop()
	for {
		ports, err := m.listening(handle.Pid())
		if err != nil && !exitedNow(handle) {
			return fmt.Errorf("reading the ports of pid %d: %w", handle.Pid(), err)
		}
		for _, port := range ports {
			endpoint := fmt.Sprintf("http://%s", hostPort(bindHost, port))
			if launch.Healthy(client, endpoint+health.Path) {
				in.update(func(r *v1.Instance) { r.Endpoint = endpoint })
				h.Logf("%s bound port %d", rec.GetName(), port)
				return nil
			}
		}
		select {
		case <-ctx.Done():
			h.Logf("stop requested before the runtime was ready")
			in.stopProcess(handle, watchExit(handle), ReasonStopRequested)
			return errStopped
		case <-handle.Done():
			return fmt.Errorf("process exited before answering health on a port it bound: %v", exitDetail(handle.Err()))
		case <-warn.C:
			waited := time.Since(start).Round(time.Second)
			h.Logf("%s answers no health on %v after %s, waiting for as long as the process lives", rec.GetName(), ports, waited)
			m.Log.Warn("instance slow to become healthy", "name", rec.GetName(), "waited", waited, "ports", ports)
		case <-tick.C:
		}
	}
}

// An exit error as text, status 0 for none
func exitDetail(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

// Whether the process has exited
func exitedNow(h launch.Handle) bool {
	select {
	case <-h.Done():
		return true
	default:
		return false
	}
}

// Measures, records the runtime's own picks, reads the template or modes, snapshots, publishes, and marks the instance ready
func (m *Manager) becomeReady(ctx context.Context, h *tasks.Handle, in *instance, p *prepared, handle launch.Handle, image string, restored bool) error {
	rec := in.snapshot()
	handle.Sync()
	lines := handle.Log().Tail(0)
	measurements := in.rt.Measure(lines)
	picked := m.chosen(h.Logf, in, lines)
	m.replan(ctx, h.Logf, in, p, picked)
	if len(picked) > 0 {
		if err := m.reinventory(in, p); err != nil {
			return m.abandon(h, in, handle, err)
		}
	}
	// The runtime's allocated cache teaches the architecture's cache factor, then the plan is drawn again with it
	if m.learnCache(ctx, h.Logf, in, p.descriptor, measurements) {
		m.replan(ctx, h.Logf, in, p, picked)
	}
	if after, perr := m.Host.Profile(ctx, true); perr == nil {
		used := host.InstanceBytes(after, rec.GetId())
		if used == 0 {
			used = uint64(max(deviceFree(p.profile)-deviceFree(after), 0))
		}
		if used > 0 {
			measurements = append(measurements, &v1.Measurement{Key: estimate.DeviceUsedKey, Bytes: used})
			if plan := in.snapshot().GetPlan(); plan != nil && m.Calibration != nil {
				base := max(float64(estimate.PlannedDevice(plan))-plan.GetOverheadDelta(), 0)
				if cerr := m.Calibration.Record(ctx, in.rt.ID(), p.descriptor.GetArchitecture(), used, uint64(base)); cerr != nil {
					m.Log.Warn("calibration write failed", "err", cerr)
				}
			}
		}
	}
	tpl, err := m.readTemplate(ctx, h.Logf, in)
	if err != nil {
		return m.abandon(h, in, handle, err)
	}
	if p.snapshot != nil && !restored {
		if err := m.takeSnapshot(ctx, h, in, p, handle, image); cancelled(ctx, err) {
			h.Logf("stop requested while snapshotting")
			in.stopProcess(handle, watchExit(handle), ReasonStopRequested)
			return errStopped
		}
	}
	if err := m.publish(ctx, in); err != nil {
		return m.abandon(h, in, handle, err)
	}
	m.ready(h, in, p.generation, measurements, tpl)
	return nil
}

// Stops a process that answered health but could not be made ready, returning why
func (m *Manager) abandon(h *tasks.Handle, in *instance, handle launch.Handle, err error) error {
	h.Logf("not ready: %v", err)
	in.stopProcess(handle, watchExit(handle), ReasonLaunchFailed)
	in.recordDrainCrash()
	return err
}

// Marks the instance ready at a generation and records measurements and the chat template
func (m *Manager) ready(h *tasks.Handle, in *instance, gen uint64, measurements []*v1.Measurement, tpl *v1.ChatTemplate) {
	now := time.Now()
	in.mu.Lock()
	in.failures, in.readyAt, in.healthWritten = 0, now, now
	in.healthFails = make([]int, len(rankRefs(in.rec)))
	in.mu.Unlock()
	var rec *v1.Instance
	in.update(func(r *v1.Instance) {
		r.State = v1.InstanceState_INSTANCE_STATE_READY
		r.ReadyAt, r.LastHealthAt = timestamppb.New(now), timestamppb.New(now)
		r.Healthy, r.ObservedGeneration = true, gen
		for _, rr := range r.GetRanks() {
			rr.Healthy = true
		}
		r.Error, r.Triage, r.StoppedAt = "", nil, nil
		r.Measurements = mergeMeasurements(r.Measurements, measurements)
		r.Template = tpl
		r.Conditions = SetCondition(r.Conditions, CondLaunched, true, ReasonRunning, pidsText(r), gen)
		r.Conditions = SetCondition(r.Conditions, CondReady, true, ReasonPublished, endpointsText(r), gen)
		r.Conditions = SetCondition(r.Conditions, CondHealthy, true, ReasonProbeOK, "", gen)
		r.Conditions = SetCondition(r.Conditions, CondDraining, false, ReasonServing, "", gen)
		rec = proto.Clone(r).(*v1.Instance)
	})
	for _, ms := range measurements {
		h.Logf("%s %s", ms.GetKey(), estimate.Human(ms.GetBytes()))
	}
	h.Progress(1, 1, "ready at "+endpointsText(rec))
	h.Logf("ready %s at %s", rec.GetName(), endpointsText(rec))
}

// Runs the runtime's preparation command, its output becoming the instance log until the runtime starts
func (m *Manager) runPrepare(ctx context.Context, h *tasks.Handle, in *instance, prep *runtimes.Command, dir, prepDir string, timeout time.Duration) error {
	marker := filepath.Join(prepDir, ".prepared")
	if _, err := os.Stat(marker); err == nil {
		h.Logf("prepared earlier at %s", prepDir)
		return nil
	}
	if err := os.RemoveAll(prepDir); err != nil {
		return err
	}
	if err := os.MkdirAll(prepDir, 0o755); err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = defaultPrepareTimeout
	}
	h.Progress(0, 0, "preparing")
	h.Logf("prepare: %s %s", prep.Command, strings.Join(auth.RedactEnv(prep.Args, prep.Env), " "))
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(pctx, prep.Command, prep.Args...)
	cmd.Dir = dir
	cmd.Env = proc.Environ()
	for k, v := range prep.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if err := os.MkdirAll(filepath.Dir(m.logPath(in.id())), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(m.logPath(in.id()), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	log := launch.NewLog(logCapacity)
	in.mu.Lock()
	in.log = log
	in.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		done <- launch.Lines(pr, func(line string) {
			fmt.Fprintln(logFile, line)
			log.Write(line)
			h.Logf("%s", line)
		})
		// A read error leaves the converter blocked on a full pipe, so drain whatever it still writes
		io.Copy(io.Discard, pr)
	}()
	// Cancelling takes the whole converter tree, workers included
	err = proc.Run(cmd)
	pw.Close()
	if rerr := <-done; rerr != nil && err == nil {
		err = fmt.Errorf("reading the prepare output: %w", rerr)
	}
	log.Close()
	if err != nil {
		os.RemoveAll(prepDir)
		err = m.launchError(in, fmt.Errorf("prepare: %w", err))
		logHits(h, in)
		return err
	}
	return os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// Polls every rank's health while the processes live, warning when slow, and stops them when the attempt is cancelled
func (m *Manager) waitReady(ctx context.Context, h *tasks.Handle, in *instance, handle launch.Handle) error {
	health := in.rt.Health()
	rec := in.snapshot()
	var err error
	for _, ref := range rankRefs(rec) {
		url := ref.endpoint + health.Path
		h.Message("waiting for " + url)
		err = launch.WaitHealthy(ctx, handle, url, health.Interval, health.SlowAfter, func(waited time.Duration) {
			h.Logf("%s is not healthy at %s after %s, waiting for as long as the process lives", rec.GetName(), url, waited)
			m.Log.Warn("instance slow to become healthy", "name", rec.GetName(), "waited", waited, "url", url)
		})
		if err != nil {
			break
		}
	}
	if err != nil && ctx.Err() != nil {
		h.Logf("stop requested before the runtime was ready")
		in.stopProcess(handle, watchExit(handle), ReasonStopRequested)
		return errStopped
	}
	return err
}

// Records the cache the runtime reports against the plan's cache before its factor, true when one was read
func (m *Manager) learnCache(ctx context.Context, logf func(string, ...any), in *instance, d *v1.Descriptor, measurements []*v1.Measurement) bool {
	plan := in.snapshot().GetPlan()
	measured := cacheMeasured(measurements)
	if m.Calibration == nil || plan == nil || plan.GetCacheBytes() == 0 || measured == 0 {
		return false
	}
	base := float64(plan.GetCacheBytes())
	if r := plan.GetCacheRatio(); r > 0 {
		base /= r
	}
	if err := m.Calibration.RecordCache(ctx, in.rt.ID(), d.GetArchitecture(), measured, uint64(base)); err != nil {
		m.Log.Warn("cache calibration write failed", "err", err)
		return false
	}
	logf("cache %s allocated against %s planned, %s plans now scale their cache by %.2f", estimate.Human(measured), estimate.Human(uint64(base)), d.GetArchitecture(), m.Calibration.Ratio(in.rt.ID(), d.GetArchitecture()))
	return true
}

// The cache bytes a runtime's log reported on every side
func cacheMeasured(measurements []*v1.Measurement) uint64 {
	var total uint64
	for _, ms := range measurements {
		if strings.HasSuffix(ms.GetKey(), ".cache") {
			total += ms.GetBytes()
		}
	}
	return total
}

// Records the auto params the runtime picked itself, read from its log, as the instance's params
func (m *Manager) chosen(logf func(string, ...any), in *instance, lines []string) map[string]string {
	policy := in.rt.Policy()
	if policy == nil {
		return nil
	}
	rec := in.snapshot()
	picked := map[string]string{}
	for name, read := range policy.Deferred {
		if _, set := rec.GetParams()[name]; set {
			continue
		}
		v, ok := read(lines)
		if !ok {
			logf("%s left %s to the runtime and its log names no value for it", rec.GetName(), name)
			continue
		}
		picked[name] = v
		logf("%s %s, chosen by %s", name, v, rec.GetRuntimeId())
	}
	if len(picked) == 0 {
		return nil
	}
	in.update(func(r *v1.Instance) {
		if r.Params == nil {
			r.Params = map[string]string{}
		}
		maps.Copy(r.Params, picked)
	})
	return picked
}

// Reports the inventory again at the params and plan the record holds after the runtime chose its own values
func (m *Manager) reinventory(in *instance, p *prepared) error {
	rec := in.snapshot()
	input := p.input()
	input.Plan = rec.GetPlan()
	inventory, err := in.rt.Inventory(input, rec.GetParams())
	if err != nil {
		return fmt.Errorf("inventory at the values %s chose: %w", rec.GetRuntimeId(), err)
	}
	in.update(func(r *v1.Instance) { r.Inventory = inventory })
	return nil
}

// Plans the run again at the values the runtime chose, so the record holds the context it runs
func (m *Manager) replan(ctx context.Context, logf func(string, ...any), in *instance, p *prepared, picked map[string]string) {
	if p.plan == nil || p.rt.Policy() == nil {
		return
	}
	plan, warning, err := m.Inspector.Plan(ctx, p.rt, p.descriptor, p.planned, runtimes.Merge(p.layered, picked), true, p.res.placement(), p.stored.GetRepo(), p.stored, p.companions)
	if err != nil {
		logf("plan at the chosen values failed: %v", err)
		return
	}
	if warning != "" {
		logf("plan: %s", warning)
	}
	in.update(func(r *v1.Instance) { r.Plan = plan })
}

// Reads the chat template from the model's files, or for a diffusion runtime the generation modes it serves
func (m *Manager) readTemplate(ctx context.Context, logf func(string, ...any), in *instance) (*v1.ChatTemplate, error) {
	rec := in.snapshot()
	if m.Runtimes.API(rec.GetRuntimeId()) == v1.ApiFlavor_API_FLAVOR_SDCPP {
		return nil, m.readModes(ctx, logf, in)
	}
	var tpl *v1.ChatTemplate
	if m.Store == nil {
		tpl = &v1.ChatTemplate{Error: "no store holds the model's files"}
	} else if stored, err := m.Store.ReadManifest(rec.GetSourceId(), rec.GetRepo(), rec.GetGroup()); err != nil {
		tpl = &v1.ChatTemplate{Error: "manifest: " + err.Error()}
	} else {
		tpl = chattemplate.Inspect(stored)
	}
	switch {
	case tpl.GetError() != "":
		logf("chat template: %s. Tool definitions pass through as the client sends them", tpl.GetError())
	case tpl.GetTools() == v1.ToolSupport_TOOL_SUPPORT_NATIVE:
		logf("chat template %s renders tools", tpl.GetSource())
	default:
		logf("chat template %s renders no tools. The gateway writes them into the prompt and parses the calls", tpl.GetSource())
	}
	return tpl, nil
}

// Reads the generation modes a diffusion server lists, waiting for the listing up to its timeout
func (m *Manager) readModes(ctx context.Context, logf func(string, ...any), in *instance) error {
	rec := in.snapshot()
	ctx, cancel := context.WithTimeout(ctx, modesTimeout)
	defer cancel()
	for {
		modes, err := diffusionModes(ctx, rec.GetEndpoint())
		if err == nil {
			in.mu.Lock()
			in.modes = modes
			in.mu.Unlock()
			logf("generates %s", strings.Join(modes, ", "))
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("capabilities of %s: %w", rec.GetName(), err)
		case <-time.After(modesPoll):
		}
	}
}

// Merges measurements by key, preserving old-only keys
func mergeMeasurements(old, fresh []*v1.Measurement) []*v1.Measurement {
	if len(fresh) == 0 {
		return old
	}
	seen := map[string]bool{}
	for _, m := range fresh {
		seen[m.GetKey()] = true
	}
	out := append([]*v1.Measurement(nil), fresh...)
	for _, m := range old {
		if !seen[m.GetKey()] {
			out = append(out, m)
		}
	}
	return out
}

// Whether pid runs the recorded command, a redacted word matching whatever the secret was
func running(pid int, command []string) bool {
	if len(command) == 0 {
		return false
	}
	joined := strings.ReplaceAll(strings.Join(command, " "), `"`, "")
	pieces := strings.Split(joined, auth.Redacted)
	for i, piece := range pieces {
		pieces[i] = regexp.QuoteMeta(piece)
	}
	pattern, err := regexp.Compile("(?s)" + strings.Join(pieces, ".*?") + "$")
	if err != nil {
		return false
	}
	return proc.RunningMatch(pid, pattern.MatchString)
}

// Whether every rank of a record runs its recorded command, and the pids of the ranks that do
func liveRanks(rec *v1.Instance) (bool, []int) {
	refs := rankRefs(rec)
	var live []int
	for _, ref := range refs {
		if running(int(ref.pid), ref.command) {
			live = append(live, int(ref.pid))
		}
	}
	return len(live) == len(refs), live
}

// Stops every process at once, waiting for all of them
func terminateAll(ctx context.Context, pids []int, grace time.Duration) {
	var wg sync.WaitGroup
	for _, pid := range pids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			proc.Terminate(ctx, pid, grace)
		}()
	}
	wg.Wait()
}

// Follows every rank of a live launch a previous daemon started, ranks past one supervised as one group
func (m *Manager) adoptRanks(rec *v1.Instance, grace time.Duration) (launch.Handle, error) {
	refs := rankRefs(rec)
	handles := make([]launch.Handle, 0, len(refs))
	for _, ref := range refs {
		h, err := launch.Adopt(int(ref.pid), m.rankLogPath(rec.GetId(), ref.rank))
		if err != nil {
			if len(refs) > 1 {
				return nil, fmt.Errorf("rank %d pid %d: %w", ref.rank, ref.pid, err)
			}
			return nil, err
		}
		handles = append(handles, h)
	}
	if len(handles) == 1 {
		return handles[0], nil
	}
	return launch.NewGroup(handles, grace), nil
}

// Whether every rank of a record answers its health path now
func everyRankHealthy(rec *v1.Instance, path string) bool {
	client := &http.Client{Timeout: probeTimeout}
	for _, ref := range rankRefs(rec) {
		if !launch.Healthy(client, ref.endpoint+path) {
			return false
		}
	}
	return true
}

// Whether a record left by a previous daemon is launched again in place
func relaunches(rec *v1.Instance) bool {
	return rec.GetDesiredRunning() && rec.GetState() != v1.InstanceState_INSTANCE_STATE_FAILED && rec.GetSeat() == nil && rec.GetRequest() != nil
}

// Loads records, adopts live runtimes, finishes stops a previous daemon began, and relaunches wanted instances in place
func (m *Manager) Recover(ctx context.Context) error {
	loaded, err := m.load(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.list = loaded
	m.pruneLocked()
	m.mu.Unlock()
	var relaunch []*instance
	// The newest record of a name and slot relaunches, older ones of it lose their intent
	newest := map[string]*instance{}
	for _, in := range loaded {
		rec := in.snapshot()
		every, live := liveRanks(rec)
		alive := !Terminal(rec.GetState()) && every
		if !Terminal(rec.GetState()) && !every && len(live) > 0 {
			m.Log.Warn("stopping the ranks left of an instance that lost a rank", "name", rec.GetName(), "pids", live)
			terminateAll(ctx, live, in.grace())
		}
		switch {
		case in.rt == nil:
			if alive {
				m.Log.Warn("stopping instance whose runtime manifest is gone", "name", rec.GetName(), "runtime", rec.GetRuntimeId(), "pids", live)
				terminateAll(ctx, live, runtimes.DefaultStopGrace)
			}
			if !Terminal(rec.GetState()) || relaunches(rec) {
				in.update(func(r *v1.Instance) {
					r.State, r.StoppedAt = v1.InstanceState_INSTANCE_STATE_FAILED, timestamppb.Now()
					r.Error = fmt.Sprintf("runtime %s is no longer registered", rec.GetRuntimeId())
				})
			}
		case alive && rec.GetDesiredRunning() && rec.GetState() != v1.InstanceState_INSTANCE_STATE_STOPPING:
			m.adopt(ctx, in)
		case alive:
			m.Log.Info("finishing the stop a previous daemon began", "name", rec.GetName(), "pids", live)
			terminateAll(ctx, live, in.grace())
			in.update(func(r *v1.Instance) {
				r.State, r.Error, r.StoppedAt = v1.InstanceState_INSTANCE_STATE_STOPPED, db.RestartNote, timestamppb.Now()
			})
		case rec.GetState() == v1.InstanceState_INSTANCE_STATE_IDLE && rec.GetDesiredRunning():
			if err := m.register(ctx, in, nil); err != nil {
				m.Log.Warn("idle instance route not named", "name", rec.GetName(), "err", err)
			}
			in.startLoop(nil, false)
		case relaunches(rec):
			key := rec.GetName() + "\x00" + rec.GetSlotId()
			if older := newest[key]; older != nil {
				older.update(func(r *v1.Instance) { r.DesiredRunning = false })
			}
			newest[key] = in
		case !Terminal(rec.GetState()):
			in.update(func(r *v1.Instance) {
				r.State, r.Error, r.StoppedAt = v1.InstanceState_INSTANCE_STATE_STOPPED, db.RestartNote, timestamppb.Now()
			})
		}
	}
	for _, in := range newest {
		if in.snapshot().GetDesiredRunning() {
			relaunch = append(relaunch, in)
		}
	}
	sort.Slice(relaunch, func(i, j int) bool {
		return relaunch[i].snapshot().GetCreatedAt().AsTime().Before(relaunch[j].snapshot().GetCreatedAt().AsTime())
	})
	var wanted []*instance
	for _, in := range relaunch {
		rec := in.snapshot()
		if m.conflict(rec.GetName(), rec.GetSlotId(), rec.GetId()) {
			m.Log.Warn("not relaunched, another live instance took the name", "name", rec.GetName())
			continue
		}
		in.revive()
		if err := m.register(ctx, in, nil); err != nil {
			m.Log.Warn("relaunch route not named", "name", rec.GetName(), "err", err)
		}
		wanted = append(wanted, in)
	}
	if len(wanted) > 0 {
		go m.relaunchAll(ctx, wanted)
	}
	return nil
}

// Relaunches revived records one after another so each plan accounts for the launches before it
func (m *Manager) relaunchAll(ctx context.Context, list []*instance) {
	for _, in := range list {
		if ctx.Err() != nil {
			return
		}
		rec := in.snapshot()
		if Terminal(rec.GetState()) || in.stopWanted() {
			continue
		}
		m.Log.Info("relaunching", "name", rec.GetName())
		first := in.launchAttempt("relaunch "+rec.GetName(), nil)
		in.startLoop(first, false)
		select {
		case <-first.finished:
		case <-ctx.Done():
			return
		}
	}
}

// Opens a final record again for a relaunch in place
func (in *instance) revive() {
	in.mu.Lock()
	in.exited, in.exitOnce = make(chan struct{}), &sync.Once{}
	gen := in.rec.GetGeneration()
	in.mu.Unlock()
	in.update(func(r *v1.Instance) {
		r.State, r.Error, r.StoppedAt, r.ReadyAt = v1.InstanceState_INSTANCE_STATE_STARTING, "", nil, nil
		unhealthy(r)
		r.Conditions = SetCondition(r.Conditions, CondLaunched, false, ReasonLaunching, db.RestartNote, gen)
		r.Conditions = SetCondition(r.Conditions, CondReady, false, ReasonStarting, db.RestartNote, gen)
	})
}

// Adopts every rank of a live runtime, restoring a missing inventory, serving at once when healthy, else verifying in a task
func (m *Manager) adopt(ctx context.Context, in *instance) {
	rec := in.snapshot()
	handle, err := m.adoptRanks(rec, in.grace())
	if err == nil && rec.GetInventory() == nil {
		var inventory *v1.RuntimeInventory
		if inventory, err = m.recordedInventory(ctx, rec); err == nil {
			in.update(func(r *v1.Instance) { r.Inventory = inventory })
			rec = in.snapshot()
		}
	}
	if err != nil {
		_, live := liveRanks(rec)
		m.Log.Warn("adoption failed, stopping the process to launch it again", "name", rec.GetName(), "pids", live, "err", err)
		terminateAll(ctx, live, in.grace())
		in.update(func(r *v1.Instance) {
			r.State, r.Error, r.StoppedAt = v1.InstanceState_INSTANCE_STATE_STOPPED, "adoption failed: "+err.Error(), timestamppb.Now()
		})
		if relaunches(in.snapshot()) {
			in.revive()
			go m.relaunchAll(ctx, []*instance{in})
		}
		return
	}
	in.attach(handle)
	if err := m.register(ctx, in, nil); err != nil {
		m.Log.Warn("adopted instance route not named", "name", rec.GetName(), "err", err)
	}
	if rec.GetSeat() != nil {
		m.adoptSeat(ctx, in, handle)
		return
	}
	in.setHealth(runtimes.RoleHealth{Kind: runtimes.HealthHTTP, Path: in.rt.Health().Path, Interval: in.rt.Health().Interval, SlowAfter: in.rt.Health().SlowAfter})
	if rec.GetState() == v1.InstanceState_INSTANCE_STATE_READY && everyRankHealthy(rec, in.rt.Health().Path) {
		handle.Sync()
		measurements := in.rt.Measure(handle.Log().Tail(0))
		if err := m.publish(ctx, in); err != nil {
			m.Log.Warn("adopted instance not published, verifying it again", "name", rec.GetName(), "err", err)
		} else {
			now := time.Now()
			in.mu.Lock()
			in.readyAt, in.healthWritten = now, now
			in.mu.Unlock()
			gen := rec.GetGeneration()
			in.update(func(r *v1.Instance) {
				r.Measurements = mergeMeasurements(r.Measurements, measurements)
				r.Healthy, r.LastHealthAt, r.ObservedGeneration = true, timestamppb.New(now), gen
				for _, rr := range r.GetRanks() {
					rr.Healthy = true
				}
				r.Conditions = SetCondition(r.Conditions, CondReady, true, ReasonPublished, "adopted after a daemon restart", gen)
				r.Conditions = SetCondition(r.Conditions, CondHealthy, true, ReasonProbeOK, "", gen)
			})
			m.Log.Info("adopted running instance", "name", rec.GetName(), "pids", pidsText(rec), "endpoints", endpointsText(rec))
			in.startLoop(nil, true)
			return
		}
	}
	in.update(func(r *v1.Instance) {
		r.State, r.ReadyAt = v1.InstanceState_INSTANCE_STATE_STARTING, nil
	})
	m.Log.Info("adopted instance still starting", "name", rec.GetName(), "pid", rec.GetPid())
	first := in.startAttempt("adopt "+rec.GetName(), func(ctx context.Context, h *tasks.Handle) error {
		h.Logf("adopted pid %d left running by the previous daemon", rec.GetPid())
		if err := m.waitReady(ctx, h, in, handle); err != nil {
			if !errors.Is(err, errStopped) {
				err = m.launchError(in, err)
				logHits(h, in)
			}
			return err
		}
		handle.Sync()
		tpl, err := m.readTemplate(ctx, h.Logf, in)
		if err != nil {
			return m.abandon(h, in, handle, err)
		}
		if err := m.publish(ctx, in); err != nil {
			return m.abandon(h, in, handle, err)
		}
		m.ready(h, in, rec.GetGeneration(), in.rt.Measure(handle.Log().Tail(0)), tpl)
		return nil
	})
	in.startLoop(first, false)
}
