package probes

import (
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
