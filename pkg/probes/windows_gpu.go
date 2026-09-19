package probes

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Probes present GPUs and registered memory. NVIDIA memory is handled by nvidia-smi.
type windowsGPU struct{}

func (windowsGPU) ID() string { return "windows-gpu" }
func (windowsGPU) Description() string {
	return "GPUs present now, from PnP and the display driver registry"
}
func (windowsGPU) Runs(os, arch string) bool {
	return on([]string{"windows"}, nil, os, arch)
}

// Matches display adapters to dedicated memory counters by LUID.
const windowsGPUScript = `$usage = @{};
try { (Get-Counter '\GPU Adapter Memory(*)\Dedicated Usage' -ErrorAction Stop).CounterSamples | ForEach-Object {
if ($_.InstanceName -match 'luid_0x([0-9a-fA-F]+)_0x([0-9a-fA-F]+)') {
$k = ([uint64]::Parse($matches[1], 'AllowHexSpecifier') -shl 32) -bor [uint64]::Parse($matches[2], 'AllowHexSpecifier');
$usage[$k] = [uint64]$usage[$k] + [uint64]$_.CookedValue } } } catch {};
Get-PnpDevice -Class Display -PresentOnly -ErrorAction SilentlyContinue | ForEach-Object {
$id = $_.InstanceId;
$drv = (Get-PnpDeviceProperty -InstanceId $id -KeyName 'DEVPKEY_Device_Driver' -ErrorAction SilentlyContinue).Data;
$luid = (Get-PnpDeviceProperty -InstanceId $id -KeyName '{60B193CB-5276-4D0F-96FC-F173ABAD3EC6} 2' -ErrorAction SilentlyContinue).Data;
$reg = if ($drv) { Get-ItemProperty "HKLM:\SYSTEM\CurrentControlSet\Control\Class\$drv" -ErrorAction SilentlyContinue };
$m = $reg.'HardwareInformation.qwMemorySize'; if ($m -is [array]) { $m = [BitConverter]::ToUInt64($m, 0) };
if (-not $m) { $m = $reg.'HardwareInformation.MemorySize'; if ($m -is [array]) { $m = [BitConverter]::ToUInt32($m, 0) } };
$used = ''; if ($null -ne $luid -and $usage.ContainsKey([uint64]$luid)) { $used = $usage[[uint64]$luid] };
[pscustomobject]@{ Name = $_.FriendlyName; Vendor = $reg.ProviderName; Device = $id; Driver = $reg.DriverVersion; Memory = $m; Used = $used } }
| Format-List`

func (p windowsGPU) Run(ctx context.Context) host.Result {
	out, res, ok := command(ctx, 20*time.Second, "powershell", "-NoProfile", "-NonInteractive", "-Command", windowsGPUScript)
	if !ok {
		return res
	}
	var devices []*v1.Device
	var pools []*v1.MemoryPool
	for i, kv := range kvBlocks(out) {
		pnp := strings.ToLower(kv["Device"])
		if strings.Contains(pnp, "ven_10de") {
			continue
		}
		id := "gpu-" + itoa(i)
		total, err := bytesIn(kv["Memory"], "B")
		if err != nil {
			return failed(fmt.Errorf("%s memory: %w", kv["Name"], err))
		}
		var free uint64
		if strings.TrimSpace(kv["Used"]) != "" {
			used, err := bytesIn(kv["Used"], "B")
			if err != nil {
				return failed(fmt.Errorf("%s used: %w", kv["Name"], err))
			}
			if total > used {
				free = total - used
			}
		}
		devices = append(devices, &v1.Device{
			Id:               id,
			Kind:             v1.DeviceKind_DEVICE_KIND_GPU,
			Vendor:           pciVendor(pnp, kv["Vendor"]),
			Name:             kv["Name"],
			MemoryTotalBytes: total,
			MemoryFreeBytes:  free,
			Facts:            map[string]string{"index": itoa(i), "driver_version": kv["Driver"], "pnp_id": kv["Device"]},
		})
		if total > 0 {
			pools = append(pools, &v1.MemoryPool{Id: id, Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: id, TotalBytes: total, FreeBytes: free})
		}
	}
	return found(devices, pools, nil, rows(len(devices)))
}

// Resolves PCI vendor codes, falling back to the driver provider.
func pciVendor(pnp, provider string) string {
	switch {
	case strings.Contains(pnp, "ven_10de"):
		return "nvidia"
	case strings.Contains(pnp, "ven_1002"):
		return "amd"
	case strings.Contains(pnp, "ven_8086"):
		return "intel"
	}
	return strings.ToLower(provider)
}
