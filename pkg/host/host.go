// Package host probes the machine into a profile of devices, memory pools, storage, and facts
package host

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sync"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Finds devices, memory pools, and host facts, a failed probe recorded without failing the profile
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
	// Processes holding device memory, as the vendor tool or the kernel lists them
	Processes []*v1.ProcessUse
	Facts     map[string]string
	Status    v1.ProbeStatus
	Detail    string
}

// Runs probes and caches the resulting profile
type Prober struct {
	// Called with a copy after every fresh probe, outside the lock
	OnProbe func(*v1.HostProfile)
	// Called on every fresh profile before it is cached, filling in what only the daemon knows
	Decorate func(*v1.HostProfile)
	probes   []Probe
	paths    []string
	ttl      time.Duration
	mu       sync.Mutex
	cached   *v1.HostProfile
	// Shares one running probe among every caller that needs a fresh profile
	flight singleflight.Group
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

// Returns the cached profile, or joins the one probe running outside the lock when stale or forced
func (p *Prober) Profile(ctx context.Context, refresh bool) (*v1.HostProfile, error) {
	p.mu.Lock()
	if !refresh && p.cached != nil && time.Since(p.cached.GetProbedAt().AsTime()) < p.ttl {
		out := proto.Clone(p.cached).(*v1.HostProfile)
		p.mu.Unlock()
		return out, nil
	}
	p.mu.Unlock()
	// The shared probe outlives any one caller, each probe command bounding itself by its own timeout
	result := p.flight.DoChan("probe", func() (any, error) {
		profile, err := p.probe(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		p.cached = profile
		p.mu.Unlock()
		if p.OnProbe != nil {
			p.OnProbe(proto.Clone(profile).(*v1.HostProfile))
		}
		return profile, nil
	})
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("probe: %w", ctx.Err())
	case r := <-result:
		if r.Err != nil {
			return nil, r.Err
		}
		return proto.Clone(r.Val.(*v1.HostProfile)).(*v1.HostProfile), nil
	}
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
		profile.Devices = mergeDevices(profile.Devices, res.Devices)
		profile.Pools = mergePools(profile.Pools, res.Pools)
		profile.Processes = mergeProcesses(profile.Processes, res.Processes)
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
	if p.Decorate != nil {
		p.Decorate(profile)
	}
	return profile, nil
}

// Adds devices by id, one two probes both found keeping the first probe's numbers and
// taking the facts and names it lacked from the second
func mergeDevices(have, more []*v1.Device) []*v1.Device {
	for _, d := range more {
		i := slices.IndexFunc(have, func(h *v1.Device) bool { return h.GetId() == d.GetId() })
		if i == -1 {
			have = append(have, d)
			continue
		}
		h := have[i]
		if h.Name == "" {
			h.Name = d.GetName()
		}
		if h.MemoryTotalBytes == 0 {
			h.MemoryTotalBytes, h.MemoryFreeBytes = d.GetMemoryTotalBytes(), d.GetMemoryFreeBytes()
		}
		if h.Facts == nil && len(d.GetFacts()) > 0 {
			h.Facts = map[string]string{}
		}
		for k, v := range d.GetFacts() {
			if _, ok := h.Facts[k]; !ok {
				h.Facts[k] = v
			}
		}
	}
	return have
}

// Adds pools by id, the first probe's numbers kept for one two probes both found
func mergePools(have, more []*v1.MemoryPool) []*v1.MemoryPool {
	for _, p := range more {
		if !slices.ContainsFunc(have, func(h *v1.MemoryPool) bool { return h.GetId() == p.GetId() }) {
			have = append(have, p)
		}
	}
	return have
}

// Adds processes by device and pid, the first probe's bytes kept for one two probes both listed
func mergeProcesses(have, more []*v1.ProcessUse) []*v1.ProcessUse {
	for _, p := range more {
		if !slices.ContainsFunc(have, func(h *v1.ProcessUse) bool { return h.GetDeviceId() == p.GetDeviceId() && h.GetPid() == p.GetPid() }) {
			have = append(have, p)
		}
	}
	return have
}

// Device memory the profile attributes to an instance's processes, summed over every device
func InstanceBytes(p *v1.HostProfile, instanceID string) uint64 {
	var total uint64
	if instanceID == "" {
		return 0
	}
	for _, pu := range p.GetProcesses() {
		if pu.GetInstanceId() == instanceID {
			total += pu.GetBytes()
		}
	}
	return total
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

// Whether a device holds memory of its own, reported on the device, a device or unified pool of the profile, or the host pool for a CPU
func OwnMemory(p *v1.HostProfile, d *v1.Device) bool {
	if d.GetMemoryTotalBytes() > 0 {
		return true
	}
	cpu := d.GetKind() == v1.DeviceKind_DEVICE_KIND_CPU
	for _, pl := range p.GetPools() {
		switch pl.GetKind() {
		case v1.PoolKind_POOL_KIND_UNIFIED:
			return true
		case v1.PoolKind_POOL_KIND_HOST:
			if cpu {
				return true
			}
		case v1.PoolKind_POOL_KIND_DEVICE:
			if !cpu && (pl.GetDeviceId() == d.GetId() || pl.GetId() == d.GetId()) {
				return true
			}
		}
	}
	return false
}

// The profile's device with an id, nil when it holds none
func Device(p *v1.HostProfile, id string) *v1.Device {
	for _, d := range p.GetDevices() {
		if d.GetId() == id {
			return d
		}
	}
	return nil
}

// Whether any device is of the vendor
func HasVendor(p *v1.HostProfile, vendor string) bool { return len(Vendor(p, vendor)) > 0 }

// Whether any device is a GPU
func HasGPU(p *v1.HostProfile) bool { return len(Kind(p, v1.DeviceKind_DEVICE_KIND_GPU)) > 0 }

// Whether the profile is of the operating system and architecture, either empty meaning any
func Is(p *v1.HostProfile, os, arch string) bool {
	return (os == "" || p.GetOs() == os) && (arch == "" || p.GetArch() == arch)
}
