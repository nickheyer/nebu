package probes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// GPUs from the system profiler; Apple silicon has no memory of its own, it draws on the unified pool
type darwinGPU struct{}

func (darwinGPU) ID() string          { return "darwin-gpu" }
func (darwinGPU) Description() string { return "GPUs from the system profiler" }
func (darwinGPU) Runs(os, arch string) bool {
	return on([]string{"darwin"}, nil, os, arch)
}

type displaysReport struct {
	Displays []struct {
		Name   string `json:"_name"`
		Vendor string `json:"spdisplays_vendor"`
		VRAM   string `json:"spdisplays_vram"`
		Cores  string `json:"sppci_cores"`
		Metal  string `json:"spdisplays_mtlgpufamilysupport"`
		Bus    string `json:"sppci_bus"`
	} `json:"SPDisplaysDataType"`
}

func (p darwinGPU) Run(ctx context.Context) host.Result {
	out, res, ok := command(ctx, 20*time.Second, "system_profiler", "SPDisplaysDataType", "-json")
	if !ok {
		return res
	}
	var report displaysReport
	if err := json.Unmarshal(out, &report); err != nil {
		return failed(fmt.Errorf("system_profiler json: %w", err))
	}
	var devices []*v1.Device
	var pools []*v1.MemoryPool
	for i, d := range report.Displays {
		id := "gpu-" + itoa(i)
		total, err := bytesIn(d.VRAM, "")
		if err != nil {
			return failed(fmt.Errorf("%s vram: %w", d.Name, err))
		}
		devices = append(devices, &v1.Device{
			Id:               id,
			Kind:             v1.DeviceKind_DEVICE_KIND_GPU,
			Vendor:           strings.ToLower(strings.TrimPrefix(d.Vendor, "sppci_vendor_")),
			Name:             d.Name,
			MemoryTotalBytes: total,
			Facts: map[string]string{
				"index": itoa(i),
				"cores": d.Cores,
				"metal": strings.TrimPrefix(d.Metal, "spdisplays_"),
				"bus":   strings.TrimPrefix(d.Bus, "spdisplays_"),
			},
		})
		// Only a card with memory of its own is a pool; Apple silicon shares the unified pool
		if strings.TrimSpace(d.VRAM) != "" {
			pools = append(pools, &v1.MemoryPool{Id: id, Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: id, TotalBytes: total})
		}
	}
	return found(devices, pools, map[string]string{"gpu.count": itoa(len(devices))}, rows(len(devices)))
}
