package probes

import (
	"context"
	"strings"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The CPU model and counts from sysctl
type darwinCPU struct{}

func (darwinCPU) ID() string          { return "darwin-cpu" }
func (darwinCPU) Description() string { return "CPU model and counts from sysctl" }
func (darwinCPU) Runs(os, arch string) bool {
	return on([]string{"darwin"}, nil, os, arch)
}

func (p darwinCPU) Run(ctx context.Context) host.Result {
	out, res, ok := command(ctx, 0, "sysctl", "machdep.cpu.brand_string", "hw.physicalcpu", "hw.logicalcpu")
	if !ok {
		return res
	}
	blocks := kvBlocks(out)
	if len(blocks) == 0 {
		return skipped("sysctl printed nothing")
	}
	kv := blocks[0]
	brand := kv["machdep.cpu.brand_string"]
	device := &v1.Device{
		Id:     "cpu-0",
		Kind:   v1.DeviceKind_DEVICE_KIND_CPU,
		Vendor: cpuVendor(brand),
		Name:   brand,
		Facts:  map[string]string{"cores": kv["hw.physicalcpu"], "siblings": kv["hw.logicalcpu"]},
	}
	facts := map[string]string{"cpu.model": brand, "cpu.threads": kv["hw.logicalcpu"]}
	return found([]*v1.Device{device}, nil, facts, rows(1))
}

// The vendor a brand string names: Apple silicon, Intel, AMD, else the first word
func cpuVendor(brand string) string {
	b := strings.ToLower(brand)
	switch {
	case strings.HasPrefix(b, "apple"):
		return "apple"
	case strings.Contains(b, "intel"):
		return "intel"
	case strings.Contains(b, "amd"):
		return "amd"
	}
	if fields := strings.Fields(b); len(fields) > 0 {
		return fields[0]
	}
	return ""
}
