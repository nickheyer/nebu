// Package host probes the machine into a profile of devices, memory pools, storage, and facts.
package host

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Probe of devices, memory pools, and host facts. Missing tools are skipped. Failed probes are
// recorded without failing the profile.
type Probe interface {
	ID() string
	Description() string
	// Whether the probe belongs on this operating system and architecture, GOOS and GOARCH names
	Runs(os, arch string) bool
	Run(ctx context.Context) Result
}

// What one probe found
type Result struct {
	Devices []*v1.Device
	Pools   []*v1.MemoryPool
	Facts   map[string]string
	Status  v1.ProbeStatus
	Detail  string
}

// Runs probes and caches the resulting profile
type Prober struct {
	// Called with a copy after every fresh probe, outside the lock
	OnProbe func(*v1.HostProfile)
	probes  []Probe
	paths   []string
	ttl     time.Duration
	mu      sync.Mutex
	cached  *v1.HostProfile
}

// Keeps the probes that run on this OS and architecture and remembers the storage paths to measure
func New(probes []Probe, paths []string, ttl time.Duration) *Prober {
	p := &Prober{paths: paths, ttl: ttl}
	for _, pr := range probes {
		if pr.Runs(runtime.GOOS, runtime.GOARCH) {
			p.probes = append(p.probes, pr)
		}
	}
	return p
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
			"os":   runtime.GOOS,
			"arch": runtime.GOARCH,
		},
		ProbedAt: timestamppb.Now(),
	}
	if home, err := os.UserHomeDir(); err == nil {
		profile.Home = home
	}
	results := make([]Result, len(p.probes))
	var wg sync.WaitGroup
	for i, pr := range p.probes {
		wg.Add(1)
		go func(i int, pr Probe) {
			defer wg.Done()
			results[i] = pr.Run(ctx)
		}(i, pr)
	}
	wg.Wait()
	for i, pr := range p.probes {
		res := results[i]
		profile.Probes = append(profile.Probes, &v1.ProbeResult{ProbeId: pr.ID(), Status: res.Status, Detail: res.Detail})
		if res.Status != v1.ProbeStatus_PROBE_STATUS_OK {
			continue
		}
		profile.Devices = append(profile.Devices, res.Devices...)
		profile.Pools = append(profile.Pools, res.Pools...)
		for k, v := range res.Facts {
			profile.Facts[k] = v
		}
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

// The devices of one vendor
func Vendor(p *v1.HostProfile, vendor string) []*v1.Device {
	var out []*v1.Device
	for _, d := range p.GetDevices() {
		if d.GetVendor() == vendor {
			out = append(out, d)
		}
	}
	return out
}

// The devices of one kind
func Kind(p *v1.HostProfile, kind v1.DeviceKind) []*v1.Device {
	var out []*v1.Device
	for _, d := range p.GetDevices() {
		if d.GetKind() == kind {
			out = append(out, d)
		}
	}
	return out
}

// Whether any device is of the vendor
func HasVendor(p *v1.HostProfile, vendor string) bool { return len(Vendor(p, vendor)) > 0 }

// Whether any device is a GPU
func HasGPU(p *v1.HostProfile) bool { return len(Kind(p, v1.DeviceKind_DEVICE_KIND_GPU)) > 0 }

// Whether the profile is of the operating system and architecture, either empty meaning any
func Is(p *v1.HostProfile, os, arch string) bool {
	return (os == "" || p.GetOs() == os) && (arch == "" || p.GetArch() == arch)
}
