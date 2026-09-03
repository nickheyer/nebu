package probes

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/spec"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "..", "test", "fixtures", "probes", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func loadProbe(t *testing.T, id, file string) *Probe {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "probes", id+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := &v1.ProbeSpec{}
	if err := spec.Decode(data, s); err != nil {
		t.Fatal(err)
	}
	s.Exec = &v1.ProbeExec{File: fixture(t, file)}
	p, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func runEmit(t *testing.T, p *Probe) *Emitted {
	t.Helper()
	res := p.Run(context.Background())
	if res.Status != v1.ProbeStatus_PROBE_STATUS_OK {
		t.Fatalf("status %s: %s", res.Status, res.Detail)
	}
	em, err := p.Emit(res.Rows)
	if err != nil {
		t.Fatal(err)
	}
	return em
}

func TestCSVProbe(t *testing.T) {
	em := runEmit(t, loadProbe(t, "nvidia-smi", "nvidia-smi.csv"))
	if len(em.Devices) != 1 || len(em.Pools) != 1 {
		t.Fatalf("devices=%d pools=%d", len(em.Devices), len(em.Pools))
	}
	d := em.Devices[0]
	if d.GetKind() != v1.DeviceKind_DEVICE_KIND_GPU || d.GetMemoryTotalBytes() != 12288<<20 || d.GetFacts()["compute_capability"] != "8.6" {
		t.Fatalf("device %+v", d)
	}
	if em.Pools[0].GetDeviceId() != d.GetId() || em.Pools[0].GetTotalBytes() != d.GetMemoryTotalBytes() {
		t.Fatalf("pool %+v", em.Pools[0])
	}
	if em.Facts["nvidia.count"] != "1" || em.Facts["nvidia.driver_version"] == "" {
		t.Fatalf("facts %v", em.Facts)
	}
}

func TestKVProbes(t *testing.T) {
	mem := runEmit(t, loadProbe(t, "meminfo", "meminfo.txt"))
	if len(mem.Pools) != 1 || mem.Pools[0].GetKind() != v1.PoolKind_POOL_KIND_HOST || mem.Pools[0].GetTotalBytes() != 65618532*1024 {
		t.Fatalf("pools %+v", mem.Pools)
	}
	cpu := runEmit(t, loadProbe(t, "cpuinfo", "cpuinfo.txt"))
	if len(cpu.Devices) != 1 || cpu.Devices[0].GetKind() != v1.DeviceKind_DEVICE_KIND_CPU || cpu.Devices[0].GetName() == "" {
		t.Fatalf("devices %+v", cpu.Devices)
	}
	if cpu.Facts["cpu.threads"] != "2" {
		t.Fatalf("facts %v", cpu.Facts)
	}
}

func TestJSONProbe(t *testing.T) {
	em := runEmit(t, loadProbe(t, "rocm-smi", "rocm-smi.json"))
	if len(em.Devices) != 2 || len(em.Pools) != 2 {
		t.Fatalf("devices=%d pools=%d", len(em.Devices), len(em.Pools))
	}
	if em.Devices[0].GetId() != "card0" || em.Devices[0].GetMemoryTotalBytes() != 17163091968 || em.Devices[0].GetFacts()["unique_id"] == "" {
		t.Fatalf("device %+v", em.Devices[0])
	}
}

func TestSkippedAndFailed(t *testing.T) {
	p, err := Compile(&v1.ProbeSpec{Id: "x", Exec: &v1.ProbeExec{Command: "definitely-not-a-command-xyz"}, Parse: &v1.ProbeParse{Kind: v1.ParseKind_PARSE_KIND_KV}})
	if err != nil {
		t.Fatal(err)
	}
	if res := p.Run(context.Background()); res.Status != v1.ProbeStatus_PROBE_STATUS_SKIPPED {
		t.Fatalf("want skipped, got %v", res)
	}
	p, err = Compile(&v1.ProbeSpec{Id: "y", Exec: &v1.ProbeExec{Command: "false"}, Parse: &v1.ProbeParse{Kind: v1.ParseKind_PARSE_KIND_KV}})
	if err != nil {
		t.Fatal(err)
	}
	if res := p.Run(context.Background()); res.Status != v1.ProbeStatus_PROBE_STATUS_FAILED {
		t.Fatalf("want failed, got %v", res)
	}
}

func TestBytes(t *testing.T) {
	cases := []struct {
		in, unit string
		want     uint64
	}{
		{"12288", "MiB", 12288 << 20},
		{"65618532 kB", "", 65618532 * 1024},
		{"1.5GiB", "B", 3 << 29},
		{"17163091968", "", 17163091968},
	}
	for _, c := range cases {
		got, err := Bytes(c.in, c.unit)
		if err != nil || got != c.want {
			t.Errorf("Bytes(%q,%q)=%d,%v want %d", c.in, c.unit, got, err, c.want)
		}
	}
	if _, err := Bytes("12", "parsecs"); err == nil {
		t.Error("unknown unit should fail")
	}
}

func TestKey(t *testing.T) {
	if got := Key("VRAM Total Memory (B)"); got != "VRAM_Total_Memory_B" {
		t.Errorf("got %q", got)
	}
	if got := Key(" model name "); got != "model_name" {
		t.Errorf("got %q", got)
	}
}

func TestRegexParse(t *testing.T) {
	p, err := Compile(&v1.ProbeSpec{
		Id:    "r",
		Exec:  &v1.ProbeExec{File: fixture(t, "nvidia-smi.csv")},
		Parse: &v1.ProbeParse{Kind: v1.ParseKind_PARSE_KIND_REGEX, Pattern: `(?m)^(?P<index>\d+), (?P<name>[^,]+)`},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := p.Run(context.Background())
	if len(res.Rows) != 1 || res.Rows[0]["name"] == "" {
		t.Fatalf("rows %v", res.Rows)
	}
}
