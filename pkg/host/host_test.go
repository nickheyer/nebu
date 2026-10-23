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
