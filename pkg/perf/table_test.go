package perf_test

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func open(t *testing.T) (*perf.Table, *db.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	table, err := perf.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	return table, database
}

// A device plans from the profile table until a sample is learned, each number counting its own
// samples, and a member's rows stand in for another node's devices
func TestNumbersLearnAndCarry(t *testing.T) {
	table, database := open(t)
	gpu := &v1.Device{Id: "gpu0", Name: "NVIDIA GeForce RTX 4090", Kind: v1.DeviceKind_DEVICE_KIND_GPU}
	n := table.Numbers(gpu)
	if n.Stream != 1008e9 || n.StreamSource != "profile RTX 4090" || n.ComputeSource != "profile RTX 4090" || !n.FromProfile() {
		t.Fatalf("profile numbers %+v", n)
	}
	if n.Fixed != perf.DefaultFixedSeconds || n.FixedSource != "shipped default" || n.AcceptanceSource != "shipped default" {
		t.Fatalf("shipped numbers %+v", n)
	}
	changes := 0
	table.OnChange = func() { changes++ }
	ctx := context.Background()
	// A prefill only sample teaches compute and nothing else.
	if err := table.Record(ctx, perf.Sample{DeviceID: "gpu0", Parameters: 70e9, PromptTokens: 2048, TimeToFirst: 2, Prefill: true}); err != nil {
		t.Fatal(err)
	}
	n = table.Numbers(gpu)
	if n.Compute != 2*70e9*2048/2 || n.ComputeSamples != 1 || n.ComputeSource != "learned from 1 samples" {
		t.Fatalf("compute %+v", n)
	}
	if n.Stream != 1008e9 || n.StreamSamples != 0 || n.StreamSource != "profile RTX 4090" {
		t.Fatalf("a prefill sample must not touch the bandwidth: %+v", n)
	}
	if err := table.Record(ctx, perf.Sample{DeviceID: "gpu0", BytesPerToken: 40e9, TimePerToken: 0.05, Decode: true}); err != nil {
		t.Fatal(err)
	}
	n = table.Numbers(gpu)
	if n.Stream != 800e9 || n.StreamSamples != 1 || n.ComputeSamples != 1 || n.Samples != 2 || n.StreamSource != "learned from 1 samples" {
		t.Fatalf("learned numbers %+v", n)
	}
	if !strings.Contains(n.Describe(), "bandwidth 800 GB/s learned from 1 samples") || !strings.Contains(n.Describe(), "fixed cost 2.0 ms shipped default") || !strings.Contains(n.Describe(), "2 requests learned from in all") {
		t.Fatalf("describe %q", n.Describe())
	}
	if changes != 2 {
		t.Fatalf("the change hook fired %d times", changes)
	}
	// Another node's record carries its learned rows, each number only when its samples say so.
	other := table.NumbersFor(gpu, []*v1.Throughput{{DeviceId: "gpu0", StreamBytesPerSecond: 500e9, StreamSamples: 3, Samples: 3, FixedSeconds: 0.001}})
	if other.Stream != 500e9 || other.StreamSamples != 3 || other.Compute != 165e12 || other.ComputeSource != "profile RTX 4090" {
		t.Fatalf("carried numbers %+v", other)
	}
	if other.Fixed != perf.DefaultFixedSeconds || other.FixedSource != "shipped default" {
		t.Fatalf("a fixed cost under %d samples stays shipped: %+v", perf.InterceptSamples, other)
	}
	if err := table.RecordAcceptance(ctx, "gpu0", 10, 7); err != nil {
		t.Fatal(err)
	}
	n = table.Numbers(gpu)
	if n.Acceptance != 0.7 || n.AcceptanceSamples != 1 || n.AcceptanceSource != "learned from 1 samples" {
		t.Fatalf("acceptance %+v", n)
	}
	if err := table.RecordRatio(ctx, v1.Shape_SHAPE_CHAIN, "llamacpp", v1.LinkClass_LINK_CLASS_LAN, 1, 1.5, 0.1, 0.15); err != nil {
		t.Fatal(err)
	}
	ttft, tpt, samples := table.Ratio(v1.Shape_SHAPE_CHAIN, "llamacpp", v1.LinkClass_LINK_CLASS_LAN)
	if math.Abs(ttft-1.5) > 1e-9 || math.Abs(tpt-1.5) > 1e-9 || samples != 1 {
		t.Fatalf("ratio %v %v %d", ttft, tpt, samples)
	}
	if err := table.RecordRatio(ctx, v1.Shape_SHAPE_CHAIN, "llamacpp", v1.LinkClass_LINK_CLASS_LAN, 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, samples := table.Ratio(v1.Shape_SHAPE_CHAIN, "llamacpp", v1.LinkClass_LINK_CLASS_LAN); samples != 1 {
		t.Fatalf("a request with no usable timing teaches nothing, samples %d", samples)
	}
	// Everything survives a reopen, sample counts included.
	again, err := perf.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	n = again.Numbers(gpu)
	if n.Stream != 800e9 || n.StreamSamples != 1 || n.ComputeSamples != 1 || n.AcceptanceSamples != 1 || len(again.Ratios()) != 1 || len(again.Throughputs()) != 1 {
		t.Fatalf("learned numbers are kept in the store: %+v", n)
	}
	if th := again.Throughputs()[0]; th.GetStreamSamples() != 1 || th.GetComputeSamples() != 1 || th.GetAcceptanceSamples() != 1 || th.GetSamples() != 2 {
		t.Fatalf("the node record carries every count: %v", th)
	}
}

// A person's profile replaces the shipped numbers, and a profile without a bandwidth or a compute rate
// is refused with the field named
func TestSetProfile(t *testing.T) {
	table, _ := open(t)
	ctx := context.Background()
	if err := table.SetProfile(ctx, &v1.DeviceProfile{Pattern: "Custom Box", StreamBytesPerSecond: 300e9, ComputeFlops: 20e12}); err != nil {
		t.Fatal(err)
	}
	custom := table.Numbers(&v1.Device{Id: "x", Name: "custom box", Kind: v1.DeviceKind_DEVICE_KIND_ACCELERATOR})
	if custom.Stream != 300e9 || custom.Compute != 20e12 || custom.StreamSource != "profile Custom Box" {
		t.Fatalf("custom profile %+v", custom)
	}
	err := table.SetProfile(ctx, &v1.DeviceProfile{Pattern: "No Compute", StreamBytesPerSecond: 300e9})
	if err == nil || !strings.Contains(err.Error(), "compute_flops") {
		t.Fatalf("zero compute must be refused naming the field, got %v", err)
	}
	err = table.SetProfile(ctx, &v1.DeviceProfile{Pattern: "No Stream", ComputeFlops: 20e12})
	if err == nil || !strings.Contains(err.Error(), "stream_bytes_per_second") {
		t.Fatalf("zero bandwidth must be refused naming the field, got %v", err)
	}
	if err := table.SetProfile(ctx, &v1.DeviceProfile{StreamBytesPerSecond: 1, ComputeFlops: 1}); err == nil {
		t.Fatal("an empty pattern must be refused")
	}
}

// The fixed cost is the intercept of time per token against bytes per token, learned from the
// twentieth decode sample on, a zero intercept counting as learned
func TestInterceptLearnedAtTwentySamples(t *testing.T) {
	table, _ := open(t)
	ctx := context.Background()
	gpu := &v1.Device{Id: "gpu1", Name: "RTX 3090", Kind: v1.DeviceKind_DEVICE_KIND_GPU}
	const beta, fixed = 900e9, 0.004
	for i := 0; i < perf.InterceptSamples; i++ {
		bytes := 10e9 + float64(i)*2e9
		if err := table.Record(ctx, perf.Sample{DeviceID: "gpu1", BytesPerToken: bytes, TimePerToken: fixed + bytes/beta, Decode: true}); err != nil {
			t.Fatal(err)
		}
		n := table.Numbers(gpu)
		if i < perf.InterceptSamples-1 && (n.FixedSource != "shipped default" || n.Fixed != perf.DefaultFixedSeconds) {
			t.Fatalf("sample %d: the fixed cost stays shipped under %d samples: %+v", i+1, perf.InterceptSamples, n)
		}
	}
	n := table.Numbers(gpu)
	if math.Abs(n.Fixed-fixed) > 1e-6 || n.FixedSamples != perf.InterceptSamples || n.FixedSource != "learned from 20 samples" {
		t.Fatalf("intercept %+v", n)
	}
	// A device whose passes cost nothing beyond the bytes learns an intercept of zero, and zero is learned.
	for i := 0; i < perf.InterceptSamples; i++ {
		bytes := 10e9 + float64(i)*2e9
		if err := table.Record(ctx, perf.Sample{DeviceID: "gpu2", BytesPerToken: bytes, TimePerToken: bytes / beta, Decode: true}); err != nil {
			t.Fatal(err)
		}
	}
	zero := table.Numbers(&v1.Device{Id: "gpu2", Name: "RTX 3090", Kind: v1.DeviceKind_DEVICE_KIND_GPU})
	if zero.Fixed > 1e-9 || zero.FixedSource != "learned from 20 samples" {
		t.Fatalf("a learned zero intercept is learned: %+v", zero)
	}
	// The same record carried to another node yields the same fixed cost.
	carried := table.NumbersFor(gpu, table.Throughputs())
	if math.Abs(carried.Fixed-fixed) > 1e-6 || carried.FixedSource != "learned from 20 samples" {
		t.Fatalf("carried intercept %+v", carried)
	}
}

// A relay's saved cache moving through the disk teaches the disk's throughput
func TestRecordDisk(t *testing.T) {
	table, _ := open(t)
	ctx := context.Background()
	if table.Disk().Stream != 1e9 || table.Disk().StreamSource != "profile disk" {
		t.Fatalf("shipped disk %+v", table.Disk())
	}
	if err := table.RecordDisk(ctx, 2<<30, 1); err != nil {
		t.Fatal(err)
	}
	d := table.Disk()
	if d.Stream != float64(2<<30) || d.StreamSamples != 1 || d.StreamSource != "learned from 1 samples" {
		t.Fatalf("learned disk %+v", d)
	}
	if err := table.RecordDisk(ctx, 0, 1); err != nil {
		t.Fatal(err)
	}
	if table.Disk().StreamSamples != 1 {
		t.Fatal("an empty move teaches nothing")
	}
}

// Numbers a person set can be removed, after which devices they named plan from the shipped table;
// shipped profiles are not removed
func TestDeleteProfile(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	table, err := perf.Open(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	shipped := len(table.Profiles())
	if err := table.SetProfile(ctx, &v1.DeviceProfile{Pattern: "Test Card", StreamBytesPerSecond: 500e9, ComputeFlops: 50e12}); err != nil {
		t.Fatal(err)
	}
	if len(table.Profiles()) != shipped+1 {
		t.Fatalf("%d profiles after setting one over %d shipped", len(table.Profiles()), shipped)
	}
	if err := table.DeleteProfile(ctx, "test card"); err != nil {
		t.Fatal(err)
	}
	if len(table.Profiles()) != shipped {
		t.Fatalf("%d profiles after removing it", len(table.Profiles()))
	}
	if err := table.DeleteProfile(ctx, "Test Card"); err == nil {
		t.Fatal("removing twice fails")
	}
	var builtin string
	for _, p := range table.Profiles() {
		if p.GetBuiltin() {
			builtin = p.GetPattern()
			break
		}
	}
	if builtin != "" {
		if err := table.DeleteProfile(ctx, builtin); err == nil {
			t.Fatal("shipped profiles are not removed")
		}
	}
	reopened, err := perf.Open(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Profiles()) != shipped {
		t.Fatalf("%d profiles after reopening", len(reopened.Profiles()))
	}
}
