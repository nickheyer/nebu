// Package doctor turns probes, runtimes, and sources into actionable checks.
package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtime"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
)

// Runs every diagnostic
type Doctor struct {
	Host     *host.Prober
	Runtimes *runtime.Registry
	Sources  *sources.Registry
	Store    *store.Store
	Installs *installs.Manager
	MinFree  uint64
}

// Produces a report from a fresh probe
func (d *Doctor) Run(ctx context.Context) (*v1.DoctorReport, error) {
	profile, err := d.Host.Profile(ctx, true)
	if err != nil {
		return nil, err
	}
	report := &v1.DoctorReport{Profile: profile}
	add := func(id string, status v1.CheckStatus, summary, hint string) {
		report.Checks = append(report.Checks, &v1.DoctorCheck{Id: id, Status: status, Summary: summary, Hint: hint})
	}
	for _, p := range profile.GetProbes() {
		switch p.GetStatus() {
		case v1.ProbeStatus_PROBE_STATUS_FAILED:
			add("probe."+p.GetProbeId(), v1.CheckStatus_CHECK_STATUS_WARN, p.GetDetail(), "fix the tool or override the probe spec in a spec directory")
		case v1.ProbeStatus_PROBE_STATUS_SKIPPED:
			add("probe."+p.GetProbeId(), v1.CheckStatus_CHECK_STATUS_OK, "skipped, "+p.GetDetail(), "")
		default:
			add("probe."+p.GetProbeId(), v1.CheckStatus_CHECK_STATUS_OK, p.GetDetail(), "")
		}
	}
	var gpus []string
	for _, dev := range profile.GetDevices() {
		if dev.GetKind() == v1.DeviceKind_DEVICE_KIND_GPU {
			gpus = append(gpus, fmt.Sprintf("%s %s", dev.GetName(), estimate.Human(dev.GetMemoryTotalBytes())))
		}
	}
	if len(gpus) == 0 {
		add("devices.gpu", v1.CheckStatus_CHECK_STATUS_WARN, "no GPU probed", "install the vendor management tool so a probe can see the device")
	} else {
		add("devices.gpu", v1.CheckStatus_CHECK_STATUS_OK, strings.Join(gpus, ", "), "")
	}
	hostPool := false
	for _, pl := range profile.GetPools() {
		if pl.GetKind() == v1.PoolKind_POOL_KIND_HOST || pl.GetKind() == v1.PoolKind_POOL_KIND_UNIFIED {
			hostPool = true
			add("pools."+pl.GetId(), v1.CheckStatus_CHECK_STATUS_OK, estimate.Human(pl.GetTotalBytes())+" total", "")
		}
	}
	if !hostPool {
		add("pools.host", v1.CheckStatus_CHECK_STATUS_WARN, "no host memory pool probed", "host offload cannot be planned without a memory probe")
	}
	for _, st := range profile.GetStorage() {
		summary := fmt.Sprintf("%s free of %s for %s", estimate.Human(st.GetFreeBytes()), estimate.Human(st.GetTotalBytes()), strings.Join(st.GetUses(), ", "))
		if st.GetFreeBytes() < d.MinFree {
			add("storage."+st.GetPath(), v1.CheckStatus_CHECK_STATUS_WARN, summary, fmt.Sprintf("below %s, models will not fit", estimate.Human(d.MinFree)))
		} else {
			add("storage."+st.GetPath(), v1.CheckStatus_CHECK_STATUS_OK, summary, "")
		}
	}
	if st, err := d.Store.Status(); err != nil {
		add("store", v1.CheckStatus_CHECK_STATUS_FAIL, err.Error(), "check permissions on the store directory")
	} else {
		summary := fmt.Sprintf("%d models, %d blobs, %s", st.GetModels(), st.GetBlobs(), estimate.Human(st.GetBlobBytes()))
		if st.GetPartials() > 0 {
			add("store", v1.CheckStatus_CHECK_STATUS_WARN, fmt.Sprintf("%s, %d partial downloads holding %s", summary, st.GetPartials(), estimate.Human(st.GetPartialBytes())), "pull again to resume or run nebu store gc --partials")
		} else {
			add("store", v1.CheckStatus_CHECK_STATUS_OK, summary, "")
		}
	}
	for _, rt := range d.Runtimes.List() {
		id := "runtime." + rt.Manifest.GetId()
		ok, unmet := rt.Compatible(profile)
		if !ok {
			add(id, v1.CheckStatus_CHECK_STATUS_WARN, "needs "+strings.Join(unmet, ", "), "")
			continue
		}
		list, err := d.Installs.List(ctx, rt.Manifest.GetId())
		switch {
		case err != nil:
			add(id, v1.CheckStatus_CHECK_STATUS_FAIL, err.Error(), "check permissions on the data directory")
		case len(list) == 0:
			add(id, v1.CheckStatus_CHECK_STATUS_WARN, "compatible, no install", fmt.Sprintf("nebu runtimes adopt %s or nebu runtimes install %s", rt.Manifest.GetId(), rt.Manifest.GetId()))
		default:
			add(id, v1.CheckStatus_CHECK_STATUS_OK, fmt.Sprintf("%d installs, newest %s %s", len(list), list[0].GetVersion(), list[0].GetPath()), "")
		}
	}
	if recipes, err := d.Installs.ListRecipes(ctx, ""); err == nil {
		for _, rs := range recipes {
			id := "recipe." + rs.GetRecipe().GetId()
			if len(rs.GetUnmet()) > 0 {
				add(id, v1.CheckStatus_CHECK_STATUS_WARN, fmt.Sprintf("variant %s, %s", rs.GetVariant(), strings.Join(rs.GetUnmet(), "; ")), "install the tools or build in a container with nebu build --sandbox oci --image IMAGE")
				continue
			}
			detail := "variant " + rs.GetVariant() + " on the host"
			if rs.GetSandbox() == v1.SandboxKind_SANDBOX_KIND_OCI {
				detail = "variant " + rs.GetVariant() + " through " + rs.GetSandboxCli()
			}
			add(id, v1.CheckStatus_CHECK_STATUS_OK, detail, "")
		}
	}
	for _, cfg := range d.Sources.List() {
		src, err := d.Sources.Get(cfg.GetId())
		if err == nil {
			_, err = src.Search(ctx, "", nil, 1)
		}
		if err != nil {
			add("source."+cfg.GetId(), v1.CheckStatus_CHECK_STATUS_FAIL, err.Error(), "check network, endpoint, and token")
		} else {
			add("source."+cfg.GetId(), v1.CheckStatus_CHECK_STATUS_OK, "reachable", "")
		}
	}
	return report, nil
}
