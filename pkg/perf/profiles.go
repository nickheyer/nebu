// Package perf keeps the numbers the formation planner prices nodes with: declared device profiles,
// throughput learned from traces, and the ratio of predicted to measured formation timings.
package perf

import (
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	gbps   = 1e9
	tflops = 1e12
	// Fixed cost per forward pass before any sample says otherwise
	DefaultFixedSeconds = 0.002
	// Draft tokens accepted per offered before the first sample
	DefaultAcceptance = 0.7
	// Samples the fixed cost intercept needs before it replaces the default
	InterceptSamples = 20
	// The device id disk throughput is learned under, per node
	DiskDevice = "disk"
)

// Declared numbers for devices whose name holds a pattern, in the order they are tried. Streaming
// bandwidth is the memory bandwidth the device reads weights at, compute the dense half precision
// rate. The kind rows at the end catch every device no pattern names.
var builtin = []*v1.DeviceProfile{
	{Pattern: "H200", StreamBytesPerSecond: 4800 * gbps, ComputeFlops: 990 * tflops, Builtin: true},
	{Pattern: "H100", StreamBytesPerSecond: 3350 * gbps, ComputeFlops: 990 * tflops, Builtin: true},
	{Pattern: "GH200", StreamBytesPerSecond: 4000 * gbps, ComputeFlops: 990 * tflops, Builtin: true},
	{Pattern: "B200", StreamBytesPerSecond: 8000 * gbps, ComputeFlops: 2250 * tflops, Builtin: true},
	{Pattern: "A100", StreamBytesPerSecond: 2000 * gbps, ComputeFlops: 312 * tflops, Builtin: true},
	{Pattern: "A10", StreamBytesPerSecond: 600 * gbps, ComputeFlops: 125 * tflops, Builtin: true},
	{Pattern: "L40", StreamBytesPerSecond: 864 * gbps, ComputeFlops: 181 * tflops, Builtin: true},
	{Pattern: "L4", StreamBytesPerSecond: 300 * gbps, ComputeFlops: 121 * tflops, Builtin: true},
	{Pattern: "RTX PRO 6000", StreamBytesPerSecond: 1792 * gbps, ComputeFlops: 250 * tflops, Builtin: true},
	{Pattern: "RTX 6000 Ada", StreamBytesPerSecond: 960 * gbps, ComputeFlops: 182 * tflops, Builtin: true},
	{Pattern: "RTX A6000", StreamBytesPerSecond: 768 * gbps, ComputeFlops: 77 * tflops, Builtin: true},
	{Pattern: "RTX 5090", StreamBytesPerSecond: 1792 * gbps, ComputeFlops: 210 * tflops, Builtin: true},
	{Pattern: "RTX 5080", StreamBytesPerSecond: 960 * gbps, ComputeFlops: 112 * tflops, Builtin: true},
	{Pattern: "RTX 5070", StreamBytesPerSecond: 672 * gbps, ComputeFlops: 62 * tflops, Builtin: true},
	{Pattern: "RTX 4090", StreamBytesPerSecond: 1008 * gbps, ComputeFlops: 165 * tflops, Builtin: true},
	{Pattern: "RTX 4080", StreamBytesPerSecond: 717 * gbps, ComputeFlops: 97 * tflops, Builtin: true},
	{Pattern: "RTX 4070", StreamBytesPerSecond: 504 * gbps, ComputeFlops: 46 * tflops, Builtin: true},
	{Pattern: "RTX 4060", StreamBytesPerSecond: 272 * gbps, ComputeFlops: 22 * tflops, Builtin: true},
	{Pattern: "RTX 3090", StreamBytesPerSecond: 936 * gbps, ComputeFlops: 71 * tflops, Builtin: true},
	{Pattern: "RTX 3080", StreamBytesPerSecond: 760 * gbps, ComputeFlops: 60 * tflops, Builtin: true},
	{Pattern: "RTX 3070", StreamBytesPerSecond: 448 * gbps, ComputeFlops: 40 * tflops, Builtin: true},
	{Pattern: "RTX 3060", StreamBytesPerSecond: 360 * gbps, ComputeFlops: 25 * tflops, Builtin: true},
	{Pattern: "RTX 2080", StreamBytesPerSecond: 448 * gbps, ComputeFlops: 40 * tflops, Builtin: true},
	{Pattern: "V100", StreamBytesPerSecond: 900 * gbps, ComputeFlops: 112 * tflops, Builtin: true},
	{Pattern: "T4", StreamBytesPerSecond: 320 * gbps, ComputeFlops: 65 * tflops, Builtin: true},
	{Pattern: "MI300", StreamBytesPerSecond: 5300 * gbps, ComputeFlops: 1300 * tflops, Builtin: true},
	{Pattern: "MI250", StreamBytesPerSecond: 3200 * gbps, ComputeFlops: 383 * tflops, Builtin: true},
	{Pattern: "MI210", StreamBytesPerSecond: 1600 * gbps, ComputeFlops: 181 * tflops, Builtin: true},
	{Pattern: "7900 XTX", StreamBytesPerSecond: 960 * gbps, ComputeFlops: 123 * tflops, Builtin: true},
	{Pattern: "7900 XT", StreamBytesPerSecond: 800 * gbps, ComputeFlops: 103 * tflops, Builtin: true},
	{Pattern: "7800 XT", StreamBytesPerSecond: 624 * gbps, ComputeFlops: 75 * tflops, Builtin: true},
	{Pattern: "9070 XT", StreamBytesPerSecond: 640 * gbps, ComputeFlops: 97 * tflops, Builtin: true},
	{Pattern: "Strix Halo", StreamBytesPerSecond: 256 * gbps, ComputeFlops: 30 * tflops, Builtin: true},
	{Pattern: "Ryzen AI Max", StreamBytesPerSecond: 256 * gbps, ComputeFlops: 30 * tflops, Builtin: true},
	{Pattern: "GB10", StreamBytesPerSecond: 273 * gbps, ComputeFlops: 100 * tflops, Builtin: true},
	{Pattern: "DGX Spark", StreamBytesPerSecond: 273 * gbps, ComputeFlops: 100 * tflops, Builtin: true},
	{Pattern: "AGX Thor", StreamBytesPerSecond: 273 * gbps, ComputeFlops: 100 * tflops, Builtin: true},
	{Pattern: "AGX Orin", StreamBytesPerSecond: 204 * gbps, ComputeFlops: 60 * tflops, Builtin: true},
	{Pattern: "M4 Ultra", StreamBytesPerSecond: 1092 * gbps, ComputeFlops: 68 * tflops, Builtin: true},
	{Pattern: "M4 Max", StreamBytesPerSecond: 546 * gbps, ComputeFlops: 34 * tflops, Builtin: true},
	{Pattern: "M4 Pro", StreamBytesPerSecond: 273 * gbps, ComputeFlops: 17 * tflops, Builtin: true},
	{Pattern: "M4", StreamBytesPerSecond: 120 * gbps, ComputeFlops: 9 * tflops, Builtin: true},
	{Pattern: "M3 Ultra", StreamBytesPerSecond: 819 * gbps, ComputeFlops: 56 * tflops, Builtin: true},
	{Pattern: "M3 Max", StreamBytesPerSecond: 400 * gbps, ComputeFlops: 28 * tflops, Builtin: true},
	{Pattern: "M3 Pro", StreamBytesPerSecond: 150 * gbps, ComputeFlops: 14 * tflops, Builtin: true},
	{Pattern: "M3", StreamBytesPerSecond: 100 * gbps, ComputeFlops: 7 * tflops, Builtin: true},
	{Pattern: "M2 Ultra", StreamBytesPerSecond: 800 * gbps, ComputeFlops: 54 * tflops, Builtin: true},
	{Pattern: "M2 Max", StreamBytesPerSecond: 400 * gbps, ComputeFlops: 27 * tflops, Builtin: true},
	{Pattern: "M2 Pro", StreamBytesPerSecond: 200 * gbps, ComputeFlops: 13 * tflops, Builtin: true},
	{Pattern: "M2", StreamBytesPerSecond: 100 * gbps, ComputeFlops: 7 * tflops, Builtin: true},
	{Pattern: "M1 Ultra", StreamBytesPerSecond: 800 * gbps, ComputeFlops: 42 * tflops, Builtin: true},
	{Pattern: "M1 Max", StreamBytesPerSecond: 400 * gbps, ComputeFlops: 21 * tflops, Builtin: true},
	{Pattern: "M1 Pro", StreamBytesPerSecond: 200 * gbps, ComputeFlops: 10 * tflops, Builtin: true},
	{Pattern: "M1", StreamBytesPerSecond: 68 * gbps, ComputeFlops: 5 * tflops, Builtin: true},
	// Devices no pattern names, by kind
	{Pattern: "kind:gpu", StreamBytesPerSecond: 400 * gbps, ComputeFlops: 40 * tflops, Builtin: true},
	{Pattern: "kind:accelerator", StreamBytesPerSecond: 400 * gbps, ComputeFlops: 40 * tflops, Builtin: true},
	{Pattern: "kind:cpu", StreamBytesPerSecond: 50 * gbps, ComputeFlops: 1 * tflops, Builtin: true},
	// A disk moving a relay's saved cache between seats
	{Pattern: DiskDevice, StreamBytesPerSecond: 1 * gbps, ComputeFlops: 0, Builtin: true},
}

// The profile that names a device: a person's rows first, then the shipped table, the kind row last
func match(profiles []*v1.DeviceProfile, d *v1.Device) *v1.DeviceProfile {
	name := strings.ToLower(d.GetName())
	kind := "kind:" + strings.ToLower(strings.TrimPrefix(d.GetKind().String(), "DEVICE_KIND_"))
	var byKind *v1.DeviceProfile
	for _, p := range profiles {
		pat := strings.ToLower(p.GetPattern())
		switch {
		case pat == kind:
			if byKind == nil {
				byKind = p
			}
		case strings.HasPrefix(pat, "kind:"):
		case name != "" && strings.Contains(name, pat):
			return p
		}
	}
	return byKind
}

// Every profile, a person's rows before the shipped ones
func merged(custom []*v1.DeviceProfile) []*v1.DeviceProfile {
	out := append([]*v1.DeviceProfile{}, custom...)
	return append(out, builtin...)
}

// Whether a person's row names a pattern
func shadowed(custom []*v1.DeviceProfile, pattern string) bool {
	for _, c := range custom {
		if strings.EqualFold(c.GetPattern(), pattern) {
			return true
		}
	}
	return false
}
