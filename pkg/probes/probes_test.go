package probes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const cpuinfoSample = `processor	: 0
vendor_id	: GenuineIntel
model name	: Intel(R) Core(TM) i9
physical id	: 0
siblings	: 16
cpu cores	: 8
flags		: fpu vme avx2

processor	: 1
vendor_id	: GenuineIntel
model name	: Intel(R) Core(TM) i9
physical id	: 0
siblings	: 16
cpu cores	: 8
flags		: fpu vme avx2
`

func TestCPUInfoFactsLiveOnTheDevice(t *testing.T) {
	res := parseCPUInfo([]byte(cpuinfoSample))
	if res.Status != v1.ProbeStatus_PROBE_STATUS_OK || len(res.Devices) != 1 || len(res.Facts) != 0 {
		t.Fatalf("result %+v", res)
	}
	d := res.Devices[0]
	if d.GetKind() != v1.DeviceKind_DEVICE_KIND_CPU || d.GetVendor() != "GenuineIntel" || d.GetName() != "Intel(R) Core(TM) i9" || d.GetId() != "cpu-0" {
		t.Fatalf("device %+v", d)
	}
	if f := d.GetFacts(); f["threads"] != "2" || f["cores"] != "8" || f["siblings"] != "16" || f["flags"] != "fpu vme avx2" {
		t.Fatalf("facts %v", f)
	}
	if res := parseCPUInfo(nil); res.Status != v1.ProbeStatus_PROBE_STATUS_SKIPPED {
		t.Fatalf("an empty listing is skipped: %+v", res)
	}
}

func TestNvidiaSMIRowsBecomeDevicesAndPools(t *testing.T) {
	out := "0, NVIDIA GeForce RTX 3080 Ti, GPU-aaaa, 12288, 1000, 11288, 8.6, 580.65.06, 00000000:01:00.0\n1, NVIDIA A100, GPU-bbbb, 40960, 0, 40960, 8.0, 580.65.06, 00000000:02:00.0\n"
	res := parseNvidiaSMI([]byte(out))
	if res.Status != v1.ProbeStatus_PROBE_STATUS_OK || len(res.Devices) != 2 || len(res.Pools) != 2 || len(res.Facts) != 0 {
		t.Fatalf("result %+v", res)
	}
	d := res.Devices[0]
	if d.GetId() != "GPU-aaaa" || d.GetVendor() != "nvidia" || d.GetMemoryTotalBytes() != 12288<<20 || d.GetMemoryFreeBytes() != 11288<<20 {
		t.Fatalf("device %+v", d)
	}
	if f := d.GetFacts(); f["driver_version"] != "580.65.06" || f["compute_capability"] != "8.6" || f["index"] != "0" || f["pci_bus_id"] != "00000000:01:00.0" {
		t.Fatalf("facts %v", f)
	}
	if p := res.Pools[1]; p.GetDeviceId() != "GPU-bbbb" || p.GetKind() != v1.PoolKind_POOL_KIND_DEVICE || p.GetTotalBytes() != 40960<<20 {
		t.Fatalf("pool %+v", p)
	}
	if res := parseNvidiaSMI([]byte("0, only, three\n")); res.Status != v1.ProbeStatus_PROBE_STATUS_FAILED {
		t.Fatalf("short rows fail: %+v", res)
	}
	if res := parseNvidiaSMI([]byte("0, n, u, lots, 0, 0, 8.6, 580, bus\n")); res.Status != v1.ProbeStatus_PROBE_STATUS_FAILED {
		t.Fatalf("a memory figure that is not a number fails: %+v", res)
	}
}

// Process rows name a probed device by uuid, executable and memory in MiB, withheld or unknown rows dropped
func TestNvidiaSMIProcessRowsBecomeProcessUse(t *testing.T) {
	devices := []*v1.Device{{Id: "GPU-aaaa"}, {Id: "GPU-bbbb"}}
	out := "1234, /usr/bin/python3 train.py --epochs 3, 6442, GPU-aaaa\n1234, [Insufficient Permissions], 100, GPU-bbbb\n[N/A], /x, 50, GPU-aaaa\n77, /y, [Insufficient Permissions], GPU-aaaa\n99, /z, 10, GPU-zzzz\n4321, C:\\Program Files\\a,b\\llama-server.exe, 7, GPU-bbbb\n\n"
	procs, err := parseNvidiaApps([]byte(out), devices)
	if err != nil || len(procs) != 3 {
		t.Fatalf("procs %v %v", procs, err)
	}
	if p := procs[0]; p.GetPid() != 1234 || p.GetDeviceId() != "GPU-aaaa" || p.GetBytes() != 6442<<20 || p.GetCommand() != "python3" {
		t.Fatalf("first %+v", p)
	}
	if p := procs[1]; p.GetPid() != 1234 || p.GetDeviceId() != "GPU-bbbb" || p.GetBytes() != 100<<20 || p.GetCommand() != "" {
		t.Fatalf("second %+v", p)
	}
	if p := procs[2]; p.GetPid() != 4321 || p.GetBytes() != 7<<20 || p.GetCommand() != "llama-server.exe" {
		t.Fatalf("third %+v", p)
	}
	if _, err := parseNvidiaApps([]byte("1234, 6442, GPU-aaaa\n"), devices); err == nil {
		t.Fatal("short rows fail")
	}
}

// Each process's open DRM nodes name the card by PCI address and its resident memory there, counted once per client
func TestDRMFdinfoNamesEachProcessMemoryOnTheCards(t *testing.T) {
	root := fakeSysfs(t)
	proc := t.TempDir()
	open := func(pid int, fd string, info string) {
		t.Helper()
		dir := filepath.Join(proc, fmt.Sprint(pid))
		if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "fdinfo"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/dev/dri/renderD128", filepath.Join(dir, "fd", fd)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fdinfo", fd), []byte(info), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	amd := "drm-driver:\tamdgpu\ndrm-client-id:\t4\ndrm-pdev:\t0000:03:00.0\ndrm-memory-vram:\t2048 KiB\ndrm-memory-gtt:\t512 KiB\ndrm-resident-vram:\t1024 KiB\n"
	open(500, "7", amd)
	open(500, "8", amd)
	open(500, "9", "drm-driver:\tamdgpu\ndrm-client-id:\t5\ndrm-pdev:\t0000:03:00.0\ndrm-memory-vram:\t3 MiB\n")
	open(600, "3", "drm-driver:\txe\ndrm-client-id:\t9\ndrm-pdev:\t0000:05:00.0\ndrm-total-vram0:\t900 KiB\ndrm-resident-vram0:\t700 KiB\ndrm-resident-system:\t100 KiB\n")
	open(700, "3", "drm-driver:\ti915\ndrm-pdev:\t0000:00:02.0\ndrm-memory-local0:\t64 KiB\n")
	if err := os.WriteFile(filepath.Join(proc, "500", "comm"), []byte("llama-server\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proc, "self", "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := drmSysfs{root: root, ids: []string{filepath.Join(root, "pci.ids")}, proc: proc}.Run(context.Background())
	if res.Status != v1.ProbeStatus_PROBE_STATUS_OK || len(res.Processes) != 2 {
		t.Fatalf("result %+v", res)
	}
	if p := res.Processes[0]; p.GetPid() != 500 || p.GetDeviceId() != "amd-1f9ebf1b8f9e4d8a" || p.GetBytes() != 1024<<10+3<<20 || p.GetCommand() != "llama-server" {
		t.Fatalf("amd process %+v", p)
	}
	if p := res.Processes[1]; p.GetPid() != 600 || p.GetDeviceId() != "intel-0000:05:00.0" || p.GetBytes() != 700<<10 || p.GetCommand() != "" {
		t.Fatalf("xe process %+v", p)
	}
}

func TestKVBlocksAndBytes(t *testing.T) {
	blocks := kvBlocks([]byte("a: 1\nb = two\n\n\nc: 3\nno separator here\n"))
	if len(blocks) != 2 || blocks[0]["a"] != "1" || blocks[0]["b"] != "two" || blocks[1]["c"] != "3" || len(blocks[1]) != 1 {
		t.Fatalf("blocks %v", blocks)
	}
	if n, err := bytesIn("16384", "kB"); err != nil || n != 16384<<10 {
		t.Fatalf("kB %d %v", n, err)
	}
	if n, err := bytesIn("2 GiB", "kB"); err != nil || n != 2<<30 {
		t.Fatalf("inline unit %d %v", n, err)
	}
	if n, err := bytesIn("  ", "kB"); err != nil || n != 0 {
		t.Fatalf("blank is zero %d %v", n, err)
	}
	if !on(nil, nil, "linux", "amd64") || on([]string{"darwin"}, nil, "linux", "amd64") || !on([]string{"darwin"}, []string{"arm64"}, "darwin", "arm64") {
		t.Fatal("on")
	}
}

func TestAllProbesAreNamed(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range All() {
		if p.ID() == "" || p.Description() == "" || seen[p.ID()] {
			t.Fatalf("probe %q", p.ID())
		}
		seen[p.ID()] = true
	}
}

// A sysfs tree with one AMD card, one Intel card under xe, one Intel integrated card, and an NVIDIA card
func fakeSysfs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(path, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	card := func(n int, bus, driver string, attrs map[string]string) {
		t.Helper()
		dev := filepath.Join(root, "bus", "pci", "devices", bus)
		for name, value := range attrs {
			write(filepath.Join(dev, name), value)
		}
		if err := os.MkdirAll(filepath.Join(root, "bus", "pci", "drivers", driver), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "..", "..", "bus", "pci", "drivers", driver), filepath.Join(dev, "driver")); err != nil {
			t.Fatal(err)
		}
		cardDir := filepath.Join(root, "class", "drm", fmt.Sprintf("card%d", n))
		if err := os.MkdirAll(cardDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "..", "..", "bus", "pci", "devices", bus), filepath.Join(cardDir, "device")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "class", "drm", fmt.Sprintf("card%d-DP-1", n)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	card(0, "0000:01:00.0", "nvidia", map[string]string{"vendor": "0x10de", "device": "0x2208"})
	card(1, "0000:03:00.0", "amdgpu", map[string]string{"vendor": "0x1002", "device": "0x744c", "subsystem_vendor": "0x1da2", "subsystem_device": "0x471e", "unique_id": "0x1F9EBF1B8F9E4D8A", "mem_info_vram_total": "25753026560", "mem_info_vram_used": "753026560"})
	card(2, "0000:05:00.0", "xe", map[string]string{"vendor": "0x8086", "device": "0x56a0", "tile0/physical_vram_size_bytes": "17179869184"})
	card(3, "0000:00:02.0", "i915", map[string]string{"vendor": "0x8086", "device": "0x4680"})
	write(filepath.Join(root, "pci.ids"), "# ids\n1002  Advanced Micro Devices, Inc. [AMD/ATI]\n\t744c  Navi 31 [Radeon RX 7900 XT/7900 XTX]\n\t\t1da2 471e  PULSE RX 7900 XTX\n8086  Intel Corporation\n\t4680  AlderLake-S GT1\n\t56a0  DG2 [Arc A770]\n")
	return root
}

func TestDRMSysfsReadsAMDAndIntelCards(t *testing.T) {
	root := fakeSysfs(t)
	res := drmSysfs{root: root, ids: []string{filepath.Join(root, "pci.ids")}, proc: t.TempDir()}.Run(context.Background())
	if res.Status != v1.ProbeStatus_PROBE_STATUS_OK || len(res.Devices) != 3 || len(res.Pools) != 2 {
		t.Fatalf("result %+v", res)
	}
	amd := res.Devices[0]
	if amd.GetId() != "amd-1f9ebf1b8f9e4d8a" || amd.GetVendor() != "amd" || amd.GetName() != "PULSE RX 7900 XTX" || amd.GetMemoryTotalBytes() != 25753026560 || amd.GetMemoryFreeBytes() != 25000000000 {
		t.Fatalf("amd %+v", amd)
	}
	if f := amd.GetFacts(); f["index"] != "1" || f["pci_bus_id"] != "0000:03:00.0" || f["driver"] != "amdgpu" || f["unique_id"] != "1f9ebf1b8f9e4d8a" || f["device_id"] != "0x744c" {
		t.Fatalf("amd facts %v", f)
	}
	if p := res.Pools[0]; p.GetId() != amd.GetId() || p.GetDeviceId() != amd.GetId() || p.GetKind() != v1.PoolKind_POOL_KIND_DEVICE || p.GetTotalBytes() != 25753026560 {
		t.Fatalf("amd pool %+v", p)
	}
	arc := res.Devices[1]
	if arc.GetId() != "intel-0000:05:00.0" || arc.GetVendor() != "intel" || arc.GetName() != "DG2 [Arc A770]" || arc.GetMemoryTotalBytes() != 17179869184 || res.Pools[1].GetDeviceId() != arc.GetId() {
		t.Fatalf("arc %+v %+v", arc, res.Pools[1])
	}
	igpu := res.Devices[2]
	if igpu.GetId() != "intel-0000:00:02.0" || igpu.GetName() != "AlderLake-S GT1" || igpu.GetMemoryTotalBytes() != 0 || igpu.GetFacts()["driver"] != "i915" {
		t.Fatalf("integrated %+v", igpu)
	}
	for _, p := range res.Pools {
		if p.GetDeviceId() == igpu.GetId() {
			t.Fatalf("a card without local memory got a pool %+v", p)
		}
	}
	if res := (drmSysfs{root: filepath.Join(root, "nowhere")}).Run(context.Background()); res.Status != v1.ProbeStatus_PROBE_STATUS_SKIPPED {
		t.Fatalf("no sysfs is skipped: %+v", res)
	}
}

func TestROCmSMIKeysDevicesOnTheUniqueID(t *testing.T) {
	cards := map[string]map[string]any{
		"card0":  {"VRAM Total Memory (B)": "25753026560", "VRAM Total Used Memory (B)": "1000", "Card Series": "Radeon RX 7900 XTX", "Card Model": "0x744c", "Unique ID": "0x1F9EBF1B8F9E4D8A", "PCI Bus": "0000:03:00.0"},
		"card1":  {"VRAM Total Memory (B)": "1024", "VRAM Total Used Memory (B)": "0", "Card Series": "APU", "Unique ID": "0x0", "PCI Bus": "0000:0C:00.0"},
		"system": {"Driver version": "6.8"},
	}
	res := parseROCmSMI(cards)
	if res.Status != v1.ProbeStatus_PROBE_STATUS_OK || len(res.Devices) != 2 {
		t.Fatalf("result %+v", res)
	}
	if d := res.Devices[0]; d.GetId() != "amd-1f9ebf1b8f9e4d8a" || d.GetFacts()["unique_id"] != "1f9ebf1b8f9e4d8a" || d.GetFacts()["pci_bus_id"] != "0000:03:00.0" || d.GetFacts()["index"] != "0" || res.Pools[0].GetId() != d.GetId() {
		t.Fatalf("device %+v", d)
	}
	if d := res.Devices[1]; d.GetId() != "amd-0000:0c:00.0" || d.GetFacts()["unique_id"] != "0" {
		t.Fatalf("an all zero unique id falls back to the bus: %+v", d)
	}
}
