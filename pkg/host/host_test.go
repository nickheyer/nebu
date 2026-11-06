package host

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

type fakeProbe struct {
	id  string
	res Result
}

func (p fakeProbe) ID() string          { return p.id }
func (p fakeProbe) Description() string { return p.id }
func (fakeProbe) Runs(string, string) bool {
	return true
}
func (p fakeProbe) Run(context.Context) Result { return p.res }

// Two probes seeing one GPU leave one device, the first probe's numbers with the second's facts
func TestProbesMergeDevicesByID(t *testing.T) {
	first := fakeProbe{id: "tool", res: Result{Status: v1.ProbeStatus_PROBE_STATUS_OK,
		Devices: []*v1.Device{{Id: "amd-1", Name: "Radeon", MemoryTotalBytes: 100, MemoryFreeBytes: 90, Facts: map[string]string{"index": "0"}}},
		Pools:   []*v1.MemoryPool{{Id: "amd-1", DeviceId: "amd-1", TotalBytes: 100, FreeBytes: 90}}}}
	second := fakeProbe{id: "sysfs", res: Result{Status: v1.ProbeStatus_PROBE_STATUS_OK,
		Devices: []*v1.Device{{Id: "amd-1", Name: "Navi", MemoryTotalBytes: 100, MemoryFreeBytes: 50, Facts: map[string]string{"index": "7", "driver": "amdgpu"}}, {Id: "intel-1", Name: "Arc"}},
		Pools:   []*v1.MemoryPool{{Id: "amd-1", DeviceId: "amd-1", TotalBytes: 100, FreeBytes: 50}}}}
	p := New([]Probe{first, second}, nil, time.Minute)
	profile, err := p.Profile(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.GetDevices()) != 2 || len(profile.GetPools()) != 1 {
		t.Fatalf("devices %v pools %v", profile.GetDevices(), profile.GetPools())
	}
	d := profile.GetDevices()[0]
	if d.GetName() != "Radeon" || d.GetMemoryFreeBytes() != 90 || d.GetFacts()["index"] != "0" || d.GetFacts()["driver"] != "amdgpu" {
		t.Fatalf("merged device %v", d)
	}
	if profile.GetPools()[0].GetFreeBytes() != 90 || profile.GetDevices()[1].GetId() != "intel-1" {
		t.Fatalf("pool %v second device %v", profile.GetPools()[0], profile.GetDevices()[1])
	}
}

// Processes merge by device and pid, the decorator stamps every fresh profile before callers and the cache see it
func TestProcessesMergeAndTheDecoratorStampsFreshProfiles(t *testing.T) {
	first := fakeProbe{id: "smi", res: Result{Status: v1.ProbeStatus_PROBE_STATUS_OK,
		Devices:   []*v1.Device{{Id: "gpu", MemoryTotalBytes: 100, MemoryFreeBytes: 40}},
		Pools:     []*v1.MemoryPool{{Id: "gpu", DeviceId: "gpu", TotalBytes: 100, FreeBytes: 40}},
		Processes: []*v1.ProcessUse{{DeviceId: "gpu", Pid: 7, Bytes: 50}, {DeviceId: "gpu", Pid: 9, Bytes: 10}}}}
	second := fakeProbe{id: "fdinfo", res: Result{Status: v1.ProbeStatus_PROBE_STATUS_OK,
		Processes: []*v1.ProcessUse{{DeviceId: "gpu", Pid: 7, Bytes: 1}, {DeviceId: "other", Pid: 7, Bytes: 5}}}}
	p := New([]Probe{first, second}, nil, time.Minute)
	p.Decorate = func(profile *v1.HostProfile) {
		for _, pu := range profile.GetProcesses() {
			if pu.GetPid() == 7 {
				pu.InstanceId, pu.InstanceName = "in1", "model"
			}
		}
	}
	var published *v1.HostProfile
	p.OnProbe = func(profile *v1.HostProfile) { published = profile }
	profile, err := p.Profile(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.GetProcesses()) != 3 || profile.GetProcesses()[0].GetBytes() != 50 {
		t.Fatalf("processes %v", profile.GetProcesses())
	}
	if InstanceBytes(profile, "in1") != 55 || InstanceBytes(profile, "in2") != 0 || InstanceBytes(profile, "") != 0 {
		t.Fatalf("instance bytes %d", InstanceBytes(profile, "in1"))
	}
	if published == nil || InstanceBytes(published, "in1") != 55 || published.GetProcesses()[0].GetInstanceName() != "model" {
		t.Fatalf("published %v", published)
	}
	cached, err := p.Profile(context.Background(), false)
	if err != nil || InstanceBytes(cached, "in1") != 55 {
		t.Fatalf("cached %v %v", cached, err)
	}
}

// A probe that counts its runs and blocks until released
type gateProbe struct {
	runs    *atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (gateProbe) ID() string               { return "gate" }
func (gateProbe) Description() string      { return "gate" }
func (gateProbe) Runs(string, string) bool { return true }
func (g gateProbe) Run(context.Context) Result {
	g.runs.Add(1)
	g.started <- struct{}{}
	<-g.release
	return Result{Status: v1.ProbeStatus_PROBE_STATUS_OK, Devices: []*v1.Device{{Id: "gpu"}}}
}

// Concurrent refreshes share one probe, and a cached read never waits behind it
func TestConcurrentProfilesShareOneProbe(t *testing.T) {
	g := gateProbe{runs: &atomic.Int32{}, started: make(chan struct{}, 8), release: make(chan struct{})}
	p := New([]Probe{g}, nil, time.Hour)
	first := make(chan error, 1)
	go func() {
		_, err := p.Profile(context.Background(), true)
		first <- err
	}()
	<-g.started
	close(g.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	g.release = make(chan struct{})
	p.probes = []Probe{g}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	var entered sync.WaitGroup
	entered.Add(8)
	for range 8 {
		wg.Go(func() {
			entered.Done()
			profile, err := p.Profile(context.Background(), true)
			if err == nil && len(profile.GetDevices()) != 1 {
				err = fmt.Errorf("devices %v", profile.GetDevices())
			}
			errs <- err
		})
	}
	<-g.started
	cached := make(chan error, 1)
	go func() {
		_, err := p.Profile(context.Background(), false)
		cached <- err
	}()
	select {
	case err := <-cached:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cached read waited behind the probe")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Profile(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("a caller whose context ends leaves the shared probe: %v", err)
	}
	entered.Wait()
	time.Sleep(50 * time.Millisecond)
	close(g.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := g.runs.Load(); n != 2 {
		t.Fatalf("probe ran %d times, want one for the first read and one shared by eight refreshes", n)
	}
}

// A GPU with its own pool names it, the CPU names the host pool, a GPU drawing on host memory names none, and every device names a unified pool
func TestLinkPoolsNamesOnlyPoolsDevicesOwn(t *testing.T) {
	gpu := &v1.Device{Id: "gpu", Kind: v1.DeviceKind_DEVICE_KIND_GPU}
	igpu := &v1.Device{Id: "igpu", Kind: v1.DeviceKind_DEVICE_KIND_GPU}
	cpu := &v1.Device{Id: "cpu", Kind: v1.DeviceKind_DEVICE_KIND_CPU}
	profile := &v1.HostProfile{Devices: []*v1.Device{gpu, igpu, cpu}, Pools: []*v1.MemoryPool{
		{Id: "gpu", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "gpu"},
		{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST},
	}}
	linkPools(profile)
	if gpu.GetPoolId() != "gpu" || igpu.GetPoolId() != "" || cpu.GetPoolId() != "host" {
		t.Fatalf("pools %q %q %q", gpu.GetPoolId(), igpu.GetPoolId(), cpu.GetPoolId())
	}
	for _, d := range profile.GetDevices() {
		if (d.GetPoolId() != "") != OwnMemory(profile, d) {
			t.Fatalf("%s pool %q disagrees with own memory %v", d.GetId(), d.GetPoolId(), OwnMemory(profile, d))
		}
	}
	profile.Pools = append(profile.Pools, &v1.MemoryPool{Id: "unified", Kind: v1.PoolKind_POOL_KIND_UNIFIED})
	linkPools(profile)
	if gpu.GetPoolId() != "gpu" || igpu.GetPoolId() != "unified" || cpu.GetPoolId() != "unified" {
		t.Fatalf("pools with unified %q %q %q", gpu.GetPoolId(), igpu.GetPoolId(), cpu.GetPoolId())
	}
	linkPools(&v1.HostProfile{Devices: []*v1.Device{cpu}})
	if cpu.GetPoolId() != "" {
		t.Fatalf("pool without pools %q", cpu.GetPoolId())
	}
}

func TestOwnMemoryAndDevice(t *testing.T) {
	gpu := &v1.Device{Id: "gpu0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Name: "big"}
	igpu := &v1.Device{Id: "intel-0000:00:02.0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Name: "AlderLake-S GT1", Facts: map[string]string{"memory": "shared"}}
	reported := &v1.Device{Id: "nv0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, MemoryTotalBytes: 1 << 30}
	p := &v1.HostProfile{Devices: []*v1.Device{gpu, igpu, reported}, Pools: []*v1.MemoryPool{
		{Id: "gpu0", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "gpu0", TotalBytes: 1},
		{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 1},
	}}
	if !OwnMemory(p, gpu) || OwnMemory(p, igpu) || !OwnMemory(p, reported) || !OwnMemory(nil, reported) || OwnMemory(nil, gpu) {
		t.Fatal("a device pool or a reported total is memory of the device's own, a host pool is not")
	}
	cpu := &v1.Device{Id: "cpu0", Kind: v1.DeviceKind_DEVICE_KIND_CPU, Name: "cpu"}
	if !OwnMemory(p, cpu) || OwnMemory(nil, cpu) {
		t.Fatal("the host pool is a CPU's own memory")
	}
	unified := &v1.HostProfile{Pools: []*v1.MemoryPool{{Id: "unified", Kind: v1.PoolKind_POOL_KIND_UNIFIED, TotalBytes: 1}}}
	if !OwnMemory(unified, gpu) {
		t.Fatal("a unified pool is shared with every device")
	}
	if Device(p, "gpu0") != gpu || Device(p, "nope") != nil || Device(nil, "gpu0") != nil {
		t.Fatal("device lookup by id")
	}
}
