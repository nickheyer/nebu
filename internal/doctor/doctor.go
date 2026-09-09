// Package doctor probes the host and checks every dependency as a task.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
)

// The task kind a check runs under
const Kind = "probe"

// Severity of one check
type status int

const (
	ok status = iota
	warn
	fail
)

func (s status) String() string {
	switch s {
	case warn:
		return "warn"
	case fail:
		return "fail"
	}
	return "ok"
}

// Probes the host again and checks probes, devices, storage, the store, runtimes, recipes, and sources
type Doctor struct {
	Host     *host.Prober
	Runtimes *runtimes.Registry
	Sources  *sources.Registry
	Store    *store.Store
	Installs *installs.Manager
	Tasks    *tasks.Manager
	MinFree  uint64

	mu      sync.Mutex
	running *v1.Task
}

// Starts the check as a task, one line per check in its log, failing when any check fails
//
// A check already running is returned instead of started twice.
func (d *Doctor) Start(ctx context.Context) (*v1.Task, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running != nil {
		if t, _, err := d.Tasks.Get(d.running.GetId()); err == nil && !terminal(t.GetState()) {
			return t, nil
		}
	}
	task := d.Tasks.Start(Kind, "Probe host", nil, d.run)
	d.running = task
	return task, nil
}

func terminal(s v1.TaskState) bool {
	return s == v1.TaskState_TASK_STATE_SUCCEEDED || s == v1.TaskState_TASK_STATE_FAILED || s == v1.TaskState_TASK_STATE_CANCELED
}

// One check with its outcome and what to do about it
type check struct {
	id      string
	status  status
	summary string
	hint    string
}

func (d *Doctor) run(ctx context.Context, h *tasks.Handle) error {
	h.Progress(0, 0, "probing")
	profile, err := d.Host.Profile(ctx, true)
	if err != nil {
		return err
	}
	var checks []check
	add := func(id string, st status, summary, hint string) {
		checks = append(checks, check{id: id, status: st, summary: summary, hint: hint})
	}
	for _, p := range profile.GetProbes() {
		switch p.GetStatus() {
		case v1.ProbeStatus_PROBE_STATUS_FAILED:
			add("probe."+p.GetProbeId(), warn, p.GetDetail(), "fix the tool the probe runs")
		case v1.ProbeStatus_PROBE_STATUS_SKIPPED:
			add("probe."+p.GetProbeId(), ok, "skipped, "+p.GetDetail(), "")
		default:
			add("probe."+p.GetProbeId(), ok, p.GetDetail(), "")
		}
	}
	var gpus []string
	for _, dev := range profile.GetDevices() {
		if dev.GetKind() == v1.DeviceKind_DEVICE_KIND_GPU {
			gpus = append(gpus, fmt.Sprintf("%s %s", dev.GetName(), estimate.Human(dev.GetMemoryTotalBytes())))
		}
	}
	if len(gpus) == 0 {
		add("devices.gpu", warn, "no GPU probed", "install the vendor management tool so a probe can see the device")
	} else {
		add("devices.gpu", ok, strings.Join(gpus, ", "), "")
	}
	hostPool := false
	for _, pl := range profile.GetPools() {
		if pl.GetKind() == v1.PoolKind_POOL_KIND_HOST || pl.GetKind() == v1.PoolKind_POOL_KIND_UNIFIED {
			hostPool = true
			add("pools."+pl.GetId(), ok, estimate.Human(pl.GetTotalBytes())+" total", "")
		}
	}
	if !hostPool {
		add("pools.host", warn, "no host memory pool probed", "host offload cannot be planned without a memory probe")
	}
	for _, st := range profile.GetStorage() {
		summary := fmt.Sprintf("%s free of %s for %s", estimate.Human(st.GetFreeBytes()), estimate.Human(st.GetTotalBytes()), strings.Join(st.GetUses(), ", "))
		if st.GetFreeBytes() < d.MinFree {
			add("storage."+st.GetPath(), warn, summary, fmt.Sprintf("below %s, models will not fit", estimate.Human(d.MinFree)))
		} else {
			add("storage."+st.GetPath(), ok, summary, "")
		}
	}
	if st, err := d.Store.Status(); err != nil {
		add("store", fail, err.Error(), "check permissions on the store directory")
	} else {
		summary := fmt.Sprintf("%d models, %d blobs, %s", st.GetModels(), st.GetBlobs(), estimate.Human(st.GetBlobBytes()))
		if st.GetPartials() > 0 {
			add("store", warn, fmt.Sprintf("%s, %d partial downloads holding %s", summary, st.GetPartials(), estimate.Human(st.GetPartialBytes())), "pull again to resume or run nebu store gc --partials")
		} else {
			add("store", ok, summary, "")
		}
	}
	for _, rt := range d.Runtimes.List() {
		id := "runtime." + rt.ID()
		compatible, unmet := runtimes.Compatible(rt, profile)
		if !compatible {
			add(id, warn, "needs "+strings.Join(unmet, ", "), "")
			continue
		}
		list, err := d.Installs.List(ctx, rt.ID())
		switch {
		case err != nil:
			add(id, fail, err.Error(), "check permissions on the data directory")
		case len(list) == 0:
			add(id, warn, "compatible, not installed", "nebu runtimes install "+rt.ID())
		default:
			add(id, ok, fmt.Sprintf("%d installs, newest %s %s", len(list), list[0].GetVersion(), list[0].GetPath()), "")
		}
	}
	recipes, err := d.Installs.ListRecipes(ctx, "")
	if err != nil {
		add("recipes", fail, err.Error(), "check the host profile")
	}
	for _, rs := range recipes {
		id := "recipe." + rs.GetRecipe().GetId()
		where := "variant " + rs.GetVariant()
		if rs.GetVariant() == "" {
			where = "no variant selected"
		}
		if len(rs.GetUnmet()) > 0 {
			add(id, warn, where+", "+strings.Join(rs.GetUnmet(), "; "), "install the tools or build in a container with nebu build --sandbox oci --image IMAGE")
			continue
		}
		detail := where + " on the host"
		if rs.GetSandbox() == v1.SandboxKind_SANDBOX_KIND_OCI {
			detail = where + " through " + rs.GetSandboxCli()
		}
		add(id, ok, detail, "")
	}
	h.Progress(0, 0, "asking every source")
	// Every source is asked at once, the check waiting for the slowest
	cfgs := d.Sources.List()
	errs := make([]error, len(cfgs))
	var wg sync.WaitGroup
	for i, cfg := range cfgs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src, err := d.Sources.Get(cfg.GetId())
			if err == nil {
				_, err = src.Search(ctx, &v1.SearchRequest{Limit: 1})
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for i, cfg := range cfgs {
		if errs[i] != nil {
			add("source."+cfg.GetId(), fail, errs[i].Error(), "check network, endpoint, and token")
		} else {
			add("source."+cfg.GetId(), ok, "reachable", "")
		}
	}
	counts := map[status]int{}
	for _, c := range checks {
		counts[c.status]++
		line := fmt.Sprintf("%-4s %s  %s", c.status, c.id, c.summary)
		if c.hint != "" {
			line += "  (" + c.hint + ")"
		}
		h.Logf("%s", line)
	}
	summary := fmt.Sprintf("%d ok", counts[ok])
	if counts[warn] > 0 {
		summary += fmt.Sprintf(", %d warnings", counts[warn])
	}
	if counts[fail] > 0 {
		summary += fmt.Sprintf(", %d failed", counts[fail])
	}
	h.Progress(uint64(len(checks)), uint64(len(checks)), summary)
	if counts[fail] > 0 {
		return errors.New(summary)
	}
	return nil
}
