package probes

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/snapshot"
)

// The checkpoint probe records criu with its version and cuda-checkpoint when PATH holds them and skips when it holds neither
func TestCheckpointProbeFindsTheTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the probe runs on linux hosts")
	}
	bin := t.TempDir()
	criu := filepath.Join(bin, "criu")
	if err := os.WriteFile(criu, []byte("#!/bin/sh\necho 'Version: 4.1'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cuda := filepath.Join(bin, "cuda-checkpoint")
	if err := os.WriteFile(cuda, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	res := checkpointTools{}.Run(context.Background())
	if res.Status != v1.ProbeStatus_PROBE_STATUS_OK {
		t.Fatalf("result %+v", res)
	}
	if res.Facts[snapshot.FactCRIU] != criu || res.Facts[snapshot.FactCRIUVersion] != "4.1" || res.Facts[snapshot.FactCUDACheckpoint] != cuda {
		t.Fatalf("facts %v", res.Facts)
	}
	profile := &v1.HostProfile{Facts: res.Facts}
	tools, err := snapshot.Detect(profile, true)
	if err != nil || tools.CRIU != criu || tools.CUDACheckpoint != cuda {
		t.Fatalf("detect %+v %v", tools, err)
	}
	t.Setenv("PATH", t.TempDir())
	if res := (checkpointTools{}).Run(context.Background()); res.Status != v1.ProbeStatus_PROBE_STATUS_SKIPPED {
		t.Fatalf("without tools %+v", res)
	}
	if _, err := snapshot.Detect(&v1.HostProfile{}, false); err == nil {
		t.Fatal("a profile without criu is refused")
	}
	if _, err := snapshot.Detect(&v1.HostProfile{Facts: map[string]string{snapshot.FactCRIU: criu}}, true); err == nil {
		t.Fatal("a GPU launch without cuda-checkpoint is refused")
	}
	if tools, err := snapshot.Detect(&v1.HostProfile{Facts: map[string]string{snapshot.FactCRIU: criu}}, false); err != nil || tools.CRIU != criu {
		t.Fatalf("a host launch needs criu alone: %+v %v", tools, err)
	}
}
