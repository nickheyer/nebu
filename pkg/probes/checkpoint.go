package probes

import (
	"context"
	"maps"
	"os/exec"
	"slices"
	"strings"

	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/snapshot"
)

// Directories cuda-checkpoint ships in beside PATH
var cudaCheckpointDirs = []string{"/usr/local/cuda/bin", "/usr/local/bin", "/opt/nvidia/bin"}

// Finds criu and cuda-checkpoint on the host, recording their paths and the criu version as facts
type checkpointTools struct{}

func (checkpointTools) ID() string          { return "checkpoint" }
func (checkpointTools) Description() string { return "CRIU and cuda-checkpoint for process snapshots" }
func (checkpointTools) Runs(os, _ string) bool {
	return os == "linux"
}

func (checkpointTools) Run(ctx context.Context) host.Result {
	facts := map[string]string{}
	criu, err := exec.LookPath("criu")
	if err == nil {
		facts[snapshot.FactCRIU] = criu
		vctx, cancel := context.WithTimeout(ctx, defaultTimeout)
		out, err := exec.CommandContext(vctx, criu, "--version").CombinedOutput()
		cancel()
		if err == nil {
			if v, ok := versionLine(string(out)); ok {
				facts[snapshot.FactCRIUVersion] = v
			}
		}
	}
	if path := firstOnPath("cuda-checkpoint", cudaCheckpointDirs); path != "" {
		facts[snapshot.FactCUDACheckpoint] = path
	}
	if len(facts) == 0 {
		return skipped("neither criu nor cuda-checkpoint is on PATH")
	}
	return found(nil, nil, facts, "found "+strings.Join(slices.Sorted(maps.Keys(facts)), ", "))
}

// The version criu prints as Version: 4.0
func versionLine(out string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Version:"); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// A binary on PATH, else in the first of the directories holding it
func firstOnPath(name string, dirs []string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	for _, dir := range dirs {
		if path, err := exec.LookPath(dir + "/" + name); err == nil {
			return path
		}
	}
	return ""
}
