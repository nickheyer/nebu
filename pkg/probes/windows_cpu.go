package probes

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The CPU model and counts from the processor class, one block per socket
type windowsCPU struct{}

func (windowsCPU) ID() string          { return "windows-cpu" }
func (windowsCPU) Description() string { return "CPU model and counts from the processor class" }
func (windowsCPU) Runs(os, arch string) bool {
	return on([]string{"windows"}, nil, os, arch)
}

func (p windowsCPU) Run(ctx context.Context) host.Result {
	out, res, ok := command(ctx, 20*time.Second, "powershell", "-NoProfile", "-NonInteractive", "-Command", "Get-CimInstance Win32_Processor | Select-Object Name,Manufacturer,NumberOfCores,NumberOfLogicalProcessors | Format-List")
	if !ok {
		return res
	}
	blocks := kvBlocks(out)
	if len(blocks) == 0 {
		return skipped("no processor listed")
	}
	var devices []*v1.Device
	threads := 0
	for i, kv := range blocks {
		devices = append(devices, &v1.Device{
			Id:     "cpu-" + itoa(i),
			Kind:   v1.DeviceKind_DEVICE_KIND_CPU,
			Vendor: strings.ToLower(kv["Manufacturer"]),
			Name:   kv["Name"],
			Facts:  map[string]string{"cores": kv["NumberOfCores"], "siblings": kv["NumberOfLogicalProcessors"]},
		})
		if n, err := strconv.Atoi(kv["NumberOfLogicalProcessors"]); err == nil {
			threads += n
		}
	}
	facts := map[string]string{"cpu.model": blocks[0]["Name"], "cpu.threads": itoa(threads)}
	return found(devices, nil, facts, rows(len(blocks)))
}
