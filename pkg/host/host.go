// Package host assembles a probed profile of the machine.
package host

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/eval"
	"github.com/nickheyer/nebu/pkg/host/probes"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Runs probes and caches the resulting profile
type Prober struct {
	// Called with a copy after every fresh probe, outside the lock
	OnProbe func(*v1.HostProfile)
	probes  []*probes.Probe
	paths   []string
	ttl     time.Duration
	mu      sync.Mutex
	cached  *v1.HostProfile
}

// Compiles probe specs for this OS and remembers storage paths
func New(specs []*v1.ProbeSpec, paths []string, ttl time.Duration) (*Prober, error) {
	p := &Prober{paths: paths, ttl: ttl}
	for _, s := range specs {
		if len(s.GetOs()) > 0 && !slices.Contains(s.GetOs(), runtime.GOOS) || len(s.GetArch()) > 0 && !slices.Contains(s.GetArch(), runtime.GOARCH) {
			continue
		}
		c, err := probes.Compile(s)
		if err != nil {
			return nil, err
		}
		p.probes = append(p.probes, c)
	}
	return p, nil
}

// Returns cached profile or probes again when stale or forced
func (p *Prober) Profile(ctx context.Context, refresh bool) (*v1.HostProfile, error) {
	p.mu.Lock()
	if !refresh && p.cached != nil && time.Since(p.cached.GetProbedAt().AsTime()) < p.ttl {
		out := proto.Clone(p.cached).(*v1.HostProfile)
		p.mu.Unlock()
		return out, nil
	}
	profile, err := p.probe(ctx)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	p.cached = profile
	out := proto.Clone(profile).(*v1.HostProfile)
	p.mu.Unlock()
	if p.OnProbe != nil {
		p.OnProbe(proto.Clone(profile).(*v1.HostProfile))
	}
	return out, nil
}

func (p *Prober) probe(ctx context.Context) (*v1.HostProfile, error) {
	hostname, _ := os.Hostname()
	profile := &v1.HostProfile{
		Hostname: hostname,
		Os:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Facts: map[string]string{
			"os":        runtime.GOOS,
			"arch":      runtime.GOARCH,
			"cpu.count": strconv.Itoa(runtime.NumCPU()),
		},
		ProbedAt: timestamppb.Now(),
	}
	if home, err := os.UserHomeDir(); err == nil {
		profile.Home = home
	}
	results := make([]probes.Result, len(p.probes))
	var wg sync.WaitGroup
	for i, pr := range p.probes {
		wg.Add(1)
		go func(i int, pr *probes.Probe) {
			defer wg.Done()
			results[i] = pr.Run(ctx)
		}(i, pr)
	}
	wg.Wait()
	for i, pr := range p.probes {
		res := results[i]
		pres := &v1.ProbeResult{ProbeId: pr.Spec.GetId(), Status: res.Status, Detail: res.Detail}
		if res.Status == v1.ProbeStatus_PROBE_STATUS_OK {
			em, err := pr.Emit(res.Rows)
			if err != nil {
				pres.Status = v1.ProbeStatus_PROBE_STATUS_FAILED
				pres.Detail = err.Error()
			} else {
				profile.Devices = append(profile.Devices, em.Devices...)
				profile.Pools = append(profile.Pools, em.Pools...)
				for k, v := range em.Facts {
					profile.Facts[k] = v
				}
			}
		}
		profile.Probes = append(profile.Probes, pres)
	}
	byMount := map[string]*v1.Storage{}
	for _, path := range p.paths {
		st, err := stat(path)
		if err != nil {
			continue
		}
		if existing, ok := byMount[st.GetPath()]; ok {
			existing.Uses = append(existing.Uses, path)
			continue
		}
		st.Uses = []string{path}
		byMount[st.GetPath()] = st
		profile.Storage = append(profile.Storage, st)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("probe: %w", err)
	}
	return profile, nil
}

// Reads the filesystem holding a path, its mount and free bytes
func Stat(path string) (*v1.Storage, error) {
	return stat(path)
}

// Builds the expression environment for a profile
func Env(p *v1.HostProfile) map[string]any {
	devices := make([]any, 0, len(p.GetDevices()))
	for _, d := range p.GetDevices() {
		devices = append(devices, map[string]any{
			"id":                 d.GetId(),
			"kind":               eval.EnumShort(d.GetKind()),
			"vendor":             d.GetVendor(),
			"name":               d.GetName(),
			"memory_total_bytes": float64(d.GetMemoryTotalBytes()),
			"memory_free_bytes":  float64(d.GetMemoryFreeBytes()),
			"facts":              eval.Anys(d.GetFacts()),
		})
	}
	pools := make([]any, 0, len(p.GetPools()))
	for _, pl := range p.GetPools() {
		pools = append(pools, map[string]any{
			"id":          pl.GetId(),
			"kind":        eval.EnumShort(pl.GetKind()),
			"device_id":   pl.GetDeviceId(),
			"total_bytes": float64(pl.GetTotalBytes()),
			"free_bytes":  float64(pl.GetFreeBytes()),
		})
	}
	storage := make([]any, 0, len(p.GetStorage()))
	for _, s := range p.GetStorage() {
		uses := make([]any, 0, len(s.GetUses()))
		for _, u := range s.GetUses() {
			uses = append(uses, u)
		}
		storage = append(storage, map[string]any{
			"path":        s.GetPath(),
			"filesystem":  s.GetFilesystem(),
			"total_bytes": float64(s.GetTotalBytes()),
			"free_bytes":  float64(s.GetFreeBytes()),
			"uses":        uses,
		})
	}
	return map[string]any{
		"hostname": p.GetHostname(),
		"os":       p.GetOs(),
		"arch":     p.GetArch(),
		"facts":    eval.Anys(p.GetFacts()),
		"devices":  devices,
		"pools":    pools,
		"storage":  storage,
	}
}
