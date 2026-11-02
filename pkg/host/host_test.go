package host

import (
	"context"
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
