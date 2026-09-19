// Package probes reads host information from system files and vendor tools.
package probes

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

const defaultTimeout = 5 * time.Second

// Every probe nebu ships, the prober keeping the ones for this OS and architecture
func All() []host.Probe {
	return []host.Probe{
		nvidiaSMI{},
		rocmSMI{},
		cpuinfo{},
		meminfo{},
		darwinCPU{},
		darwinGPU{},
		darwinMemory{},
		darwinUnified{},
		windowsCPU{},
		windowsGPU{},
		windowsMemory{},
	}
}

// Keep device details on devices and memory totals on pools. Host facts contain only remaining
// properties.

// Matches supported OS and architecture lists. Empty lists allow all values.
func on(oses, archs []string, os, arch string) bool {
	return (len(oses) == 0 || slices.Contains(oses, os)) && (len(archs) == 0 || slices.Contains(archs, arch))
}

// Missing tools or files mark the probe skipped.
func skipped(detail string) host.Result {
	return host.Result{Status: v1.ProbeStatus_PROBE_STATUS_SKIPPED, Detail: detail}
}

// Records a failed probe.
func failed(err error) host.Result {
	return host.Result{Status: v1.ProbeStatus_PROBE_STATUS_FAILED, Detail: err.Error()}
}

// Records a successful probe.
func found(devices []*v1.Device, pools []*v1.MemoryPool, facts map[string]string, detail string) host.Result {
	if facts == nil {
		facts = map[string]string{}
	}
	return host.Result{Devices: devices, Pools: pools, Facts: facts, Status: v1.ProbeStatus_PROBE_STATUS_OK, Detail: detail}
}

// Runs a tool from PATH with a timeout, telling a missing tool apart from a failing one
func command(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, host.Result, bool) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, skipped(name + " not found"), false
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, host.Result{Status: v1.ProbeStatus_PROBE_STATUS_FAILED, Detail: detail}, false
	}
	return stdout.Bytes(), host.Result{}, true
}

// Reads a system file, a missing one meaning the probe does not apply here
func file(path string) ([]byte, host.Result, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, skipped(err.Error()), false
	}
	return data, host.Result{}, true
}

// Splits key-value blocks on blank lines. Trims keys and skips lines without separators.
func kvBlocks(data []byte) []map[string]string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	var out []map[string]string
	for _, block := range strings.Split(text, "\n\n") {
		row := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			k, v, ok := splitKV(line)
			if ok {
				row[k] = v
			}
		}
		if len(row) > 0 {
			out = append(out, row)
		}
	}
	return out
}

// Splits one key: value or key=value line
func splitKV(line string) (string, string, bool) {
	for _, sep := range []string{":", "="} {
		if i := strings.Index(line, sep); i > 0 {
			return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
		}
	}
	return "", "", false
}

// Reads a byte count a tool printed, in the unit it prints when the text names none
func bytesIn(s, unit string) (uint64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return text.Bytes(s, unit)
}

// Formats a row count for a probe's detail line
func rows(n int) string {
	return fmt.Sprintf("%d rows", n)
}

func itoa(n int) string { return strconv.Itoa(n) }
