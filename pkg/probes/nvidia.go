package probes

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strings"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// NVIDIA GPUs through nvidia-smi, which reports memory in MiB
type nvidiaSMI struct{}

func (nvidiaSMI) ID() string          { return "nvidia-smi" }
func (nvidiaSMI) Description() string { return "NVIDIA GPUs through nvidia-smi" }
func (nvidiaSMI) Runs(string, string) bool {
	return true
}

func (p nvidiaSMI) Run(ctx context.Context) host.Result {
	out, res, ok := command(ctx, 0, "nvidia-smi", "--query-gpu=index,name,uuid,memory.total,memory.used,memory.free,compute_cap,driver_version,pci.bus_id", "--format=csv,noheader,nounits")
	if !ok {
		return res
	}
	return parseNvidiaSMI(out)
}

// One device and one pool per CSV row, the driver version and compute capability facts of the device
func parseNvidiaSMI(out []byte) host.Result {
	r := csv.NewReader(bytes.NewReader(out))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return failed(err)
	}
	var devices []*v1.Device
	var pools []*v1.MemoryPool
	for _, rec := range records {
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		if len(rec) < 9 {
			return failed(fmt.Errorf("nvidia-smi printed %d columns, wanted 9: %q", len(rec), rec))
		}
		for i := range rec {
			rec[i] = strings.TrimSpace(rec[i])
		}
		index, name, uuid, total, free, compute, driver, bus := rec[0], rec[1], rec[2], rec[3], rec[5], rec[6], rec[7], rec[8]
		totalBytes, err := bytesIn(total, "MiB")
		if err != nil {
			return failed(err)
		}
		freeBytes, err := bytesIn(free, "MiB")
		if err != nil {
			return failed(err)
		}
		devices = append(devices, &v1.Device{
			Id:               uuid,
			Kind:             v1.DeviceKind_DEVICE_KIND_GPU,
			Vendor:           "nvidia",
			Name:             name,
			MemoryTotalBytes: totalBytes,
			MemoryFreeBytes:  freeBytes,
			Facts:            map[string]string{"index": index, "compute_capability": compute, "driver_version": driver, "pci_bus_id": bus},
		})
		pools = append(pools, &v1.MemoryPool{Id: uuid, Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: uuid, TotalBytes: totalBytes, FreeBytes: freeBytes})
	}
	return found(devices, pools, nil, rows(len(devices)))
}
