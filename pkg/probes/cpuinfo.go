package probes

import (
	"context"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The CPU model, flags, and thread count from procfs, one block per logical processor
type cpuinfo struct{}

func (cpuinfo) ID() string          { return "cpuinfo" }
func (cpuinfo) Description() string { return "CPU model and flags from procfs" }
func (cpuinfo) Runs(os, arch string) bool {
	return on([]string{"linux"}, nil, os, arch)
}

func (p cpuinfo) Run(context.Context) host.Result {
	data, res, ok := file("/proc/cpuinfo")
	if !ok {
		return res
	}
	blocks := kvBlocks(data)
	if len(blocks) == 0 {
		return skipped("/proc/cpuinfo lists no processors")
	}
	first := blocks[0]
	// The first logical processor stands for the package; every block of a package repeats its model
	device := &v1.Device{
		Id:     "cpu-" + first["physical id"],
		Kind:   v1.DeviceKind_DEVICE_KIND_CPU,
		Vendor: first["vendor_id"],
		Name:   first["model name"],
		Facts:  map[string]string{"cores": first["cpu cores"], "siblings": first["siblings"], "flags": first["flags"]},
	}
	facts := map[string]string{
		"cpu.model":   first["model name"],
		"cpu.flags":   first["flags"],
		"cpu.threads": itoa(len(blocks)),
	}
	return found([]*v1.Device{device}, nil, facts, rows(len(blocks)))
}
