package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/spec"
)

func fixtureProbes(t *testing.T) []*v1.ProbeSpec {
	t.Helper()
	var out []*v1.ProbeSpec
	for id, file := range map[string]string{"nvidia-smi": "nvidia-smi.csv", "meminfo": "meminfo.txt", "cpuinfo": "cpuinfo.txt"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "spec", "probes", id+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		s := &v1.ProbeSpec{}
		if err := spec.Decode(data, s); err != nil {
			t.Fatal(err)
		}
		abs, _ := filepath.Abs(filepath.Join("..", "..", "test", "fixtures", "probes", file))
		s.Exec = &v1.ProbeExec{File: abs}
		s.Os = nil
		out = append(out, s)
	}
	return out
}

func TestProfile(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	p, err := New(fixtureProbes(t), []string{dirA, dirB}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := p.Profile(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	var gpu, cpu, dev, hostPool bool
	for _, d := range profile.GetDevices() {
		gpu = gpu || d.GetKind() == v1.DeviceKind_DEVICE_KIND_GPU
		cpu = cpu || d.GetKind() == v1.DeviceKind_DEVICE_KIND_CPU
	}
	for _, pl := range profile.GetPools() {
		dev = dev || pl.GetKind() == v1.PoolKind_POOL_KIND_DEVICE
		hostPool = hostPool || pl.GetKind() == v1.PoolKind_POOL_KIND_HOST
	}
	if !gpu || !cpu || !dev || !hostPool || len(profile.GetStorage()) != 1 || len(profile.GetProbes()) != 3 {
		t.Fatalf("profile incomplete: %+v", profile)
	}
	if st := profile.GetStorage()[0]; len(st.GetUses()) != 2 || st.GetUses()[0] != dirA || st.GetUses()[1] != dirB || st.GetPath() == dirA {
		t.Fatalf("storage should collapse to one mount listing both dirs: %+v", st)
	}
	if profile.GetFacts()["nvidia.count"] != "1" || profile.GetFacts()["os"] == "" {
		t.Fatalf("facts %v", profile.GetFacts())
	}
	again, _ := p.Profile(context.Background(), false)
	if !again.GetProbedAt().AsTime().Equal(profile.GetProbedAt().AsTime()) {
		t.Fatal("expected cached profile")
	}
	env := Env(profile)
	if len(env["devices"].([]any)) != 2 {
		t.Fatalf("env %v", env)
	}
}
