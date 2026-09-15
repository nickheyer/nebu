package probes

import (
	"context"
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
	for i, kv := range blocks {
		devices = append(devices, &v1.Device{
			Id:     "cpu-" + itoa(i),
			Kind:   v1.DeviceKind_DEVICE_KIND_CPU,
			Vendor: strings.ToLower(kv["Manufacturer"]),
			Name:   kv["Name"],
			Facts:  map[string]string{"cores": kv["NumberOfCores"], "threads": kv["NumberOfLogicalProcessors"]},
		})
	}
	return found(devices, nil, nil, rows(len(blocks)))
}
