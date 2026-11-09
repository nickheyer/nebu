package probes

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
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
	result := parseNvidiaSMI(out)
	if result.Status != v1.ProbeStatus_PROBE_STATUS_OK {
		return result
	}
	apps, res, ok := command(ctx, 0, "nvidia-smi", "--query-compute-apps=pid,process_name,used_memory,gpu_uuid", "--format=csv,noheader,nounits")
	if !ok {
		result.Detail += ", processes unlisted: " + res.Detail
		return result
	}
	procs, err := parseNvidiaApps(apps, result.Devices)
	if err != nil {
		return failed(err)
	}
	result.Processes = procs
	return result
}

// One process per CSV row on a probed device, named by its executable, unreadable pid or memory rows left out
func parseNvidiaApps(out []byte, devices []*v1.Device) ([]*v1.ProcessUse, error) {
	r := csv.NewReader(bytes.NewReader(out))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, d := range devices {
		known[d.GetId()] = true
	}
	var procs []*v1.ProcessUse
	for _, rec := range records {
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		if len(rec) < 4 {
			return nil, fmt.Errorf("nvidia-smi printed %d columns of process use, wanted 4: %q", len(rec), rec)
		}
		for i := range rec {
			rec[i] = strings.TrimSpace(rec[i])
		}
		last := len(rec) - 1
		pid, err := strconv.Atoi(rec[0])
		if err != nil || !known[rec[last]] {
			continue
		}
		used, err := bytesIn(rec[last-1], "MiB")
		if err != nil {
			continue
		}
		procs = append(procs, &v1.ProcessUse{DeviceId: rec[last], Pid: int32(pid), Bytes: used, Command: processName(strings.Join(rec[1:last-1], ","))})
	}
	return procs, nil
}

// Base name of the executable, the first word of a Linux command line, blank for nvidia-smi's bracketed placeholders
func processName(cmdline string) string {
	if cmdline == "" || strings.HasPrefix(cmdline, "[") {
		return ""
	}
	path := cmdline
	if !strings.Contains(path, `\`) {
		path, _, _ = strings.Cut(path, " ")
	}
	return path[strings.LastIndexAny(path, `/\`)+1:]
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
	var driverVersion string
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
		driverVersion = driver
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
	detail := ""
	if driverVersion != "" {
		detail = "driver " + driverVersion
	}
	return found(devices, pools, nil, detail)
}
