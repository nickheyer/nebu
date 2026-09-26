package perf

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Exponential weight of a fresh sample
const smoothing = 0.3

// Where a number that no sample taught came from
const shippedDefault = "shipped default"

// The numbers the planner prices one device with, each with where it came from
type Numbers struct {
	// Bytes per second the device streams weights at
	Stream float64
	// Floating point operations per second during prefill
	Compute float64
	// Fixed cost per forward pass in seconds
	Fixed float64
	// Draft tokens accepted per offered
	Acceptance float64
	// Requests each number was learned from, zero for a declared or shipped number
	StreamSamples, ComputeSamples, FixedSamples, AcceptanceSamples uint32
	// Requests learned from in all
	Samples uint32
	// Where each number came from: learned from N samples, profile <pattern>, or the shipped default
	StreamSource, ComputeSource, FixedSource, AcceptanceSource string
	// The sources of the streaming bandwidth and the compute in one line
	Source string
}

// Whether any number came from the profile table rather than a sample
func (n Numbers) FromProfile() bool {
	for _, s := range []string{n.StreamSource, n.ComputeSource, n.FixedSource} {
		if strings.HasPrefix(s, "profile ") {
			return true
		}
	}
	return false
}

// Every number with its value and source, and the requests learned from in all, for a plan's
// source list
func (n Numbers) Describe() string {
	out := fmt.Sprintf("bandwidth %s %s, compute %s %s, fixed cost %s %s, acceptance %.2f %s",
		rate(n.Stream), n.StreamSource, flops(n.Compute), n.ComputeSource, seconds(n.Fixed), n.FixedSource, n.Acceptance, n.AcceptanceSource)
	if n.Samples > 0 {
		out += fmt.Sprintf(", %d requests learned from in all", n.Samples)
	}
	return out
}

func rate(bps float64) string {
	switch {
	case bps >= 1e12:
		return fmt.Sprintf("%.1f TB/s", bps/1e12)
	case bps >= 1e9:
		return fmt.Sprintf("%.0f GB/s", bps/1e9)
	case bps >= 1e6:
		return fmt.Sprintf("%.0f MB/s", bps/1e6)
	}
	return fmt.Sprintf("%.0f B/s", bps)
}

func flops(f float64) string {
	switch {
	case f >= 1e15:
		return fmt.Sprintf("%.1f PFLOPS", f/1e15)
	case f >= 1e12:
		return fmt.Sprintf("%.0f TFLOPS", f/1e12)
	case f >= 1e9:
		return fmt.Sprintf("%.0f GFLOPS", f/1e9)
	}
	return fmt.Sprintf("%.0f FLOPS", f)
}

func seconds(s float64) string {
	switch {
	case s >= 1:
		return fmt.Sprintf("%.1f s", s)
	case s >= 1e-3:
		return fmt.Sprintf("%.1f ms", s*1e3)
	}
	return fmt.Sprintf("%.0f µs", s*1e6)
}

func learnedFrom(samples uint32) string { return fmt.Sprintf("learned from %d samples", samples) }

// Learned throughput, declared profiles, and formation ratios, kept in the store
type Table struct {
	store *db.DB
	mu    sync.Mutex
	rows  map[string]*db.ThroughputRow
	// A person's profiles, over the shipped table
	custom []*v1.DeviceProfile
	ratios map[string]*v1.FormationRatio
	// Called after every learned number changes, so the node record carries it
	OnChange func()
}

// Tells the listener a number changed
func (t *Table) changed() {
	if t.OnChange != nil {
		t.OnChange()
	}
}

// Loads every learned number and profile from the store
func Open(ctx context.Context, store *db.DB) (*Table, error) {
	t := &Table{store: store, rows: map[string]*db.ThroughputRow{}, ratios: map[string]*v1.FormationRatio{}}
	rows, err := store.ListThroughput(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		r.Throughput.StreamSamples = r.Points
		r.Throughput.AcceptanceSamples = r.AcceptanceSamples
		t.rows[r.Throughput.GetDeviceId()] = r
	}
	if t.custom, err = store.ListDeviceProfiles(ctx); err != nil {
		return nil, err
	}
	ratios, err := store.ListRatios(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range ratios {
		t.ratios[ratioKey(r.GetShape(), r.GetRuntimeId(), r.GetLinkClass())] = r
	}
	return t, nil
}

// The declared numbers for a device from the profile table, the fixed cost and acceptance shipped
// when the profile names none
func declared(custom []*v1.DeviceProfile, d *v1.Device) Numbers {
	out := Numbers{Fixed: DefaultFixedSeconds, Acceptance: DefaultAcceptance, FixedSource: shippedDefault, AcceptanceSource: shippedDefault}
	p := match(merged(custom), d)
	if p == nil {
		return out
	}
	source := "profile " + p.GetPattern()
	out.Stream, out.Compute = p.GetStreamBytesPerSecond(), p.GetComputeFlops()
	out.StreamSource, out.ComputeSource = source, source
	if p.GetFixedSeconds() > 0 {
		out.Fixed, out.FixedSource = p.GetFixedSeconds(), source
	}
	return out
}

// Lays a learned record over the declared numbers: each number replaced when a sample taught it,
// the fixed cost once the intercept has its samples
func (n *Numbers) apply(th *v1.Throughput) {
	if th.GetStreamSamples() > 0 {
		n.Stream, n.StreamSamples, n.StreamSource = th.GetStreamBytesPerSecond(), th.GetStreamSamples(), learnedFrom(th.GetStreamSamples())
	}
	if th.GetComputeSamples() > 0 {
		n.Compute, n.ComputeSamples, n.ComputeSource = th.GetComputeFlops(), th.GetComputeSamples(), learnedFrom(th.GetComputeSamples())
	}
	if th.GetStreamSamples() >= InterceptSamples {
		n.Fixed, n.FixedSamples, n.FixedSource = th.GetFixedSeconds(), th.GetStreamSamples(), learnedFrom(th.GetStreamSamples())
	}
	if th.GetAcceptanceSamples() > 0 {
		n.Acceptance, n.AcceptanceSamples, n.AcceptanceSource = th.GetAcceptance(), th.GetAcceptanceSamples(), learnedFrom(th.GetAcceptanceSamples())
	}
	n.Samples = th.GetSamples()
}

// Fills the summary line from the sources
func (n *Numbers) summarize() {
	n.Source = fmt.Sprintf("bandwidth %s, compute %s", n.StreamSource, n.ComputeSource)
}

// The numbers for a device: learned where a sample exists for the device, the profile table
// otherwise, with the source of every number named
func (t *Table) Numbers(d *v1.Device) Numbers {
	t.mu.Lock()
	row, learned := t.rows[d.GetId()]
	custom := t.custom
	t.mu.Unlock()
	out := declared(custom, d)
	if learned {
		out.apply(row.Throughput)
	}
	out.summarize()
	return out
}

// The numbers for a device on another node, from the throughput rows its record carries and the
// profile table for what it has not learned
func (t *Table) NumbersFor(d *v1.Device, learned []*v1.Throughput) Numbers {
	t.mu.Lock()
	custom := t.custom
	t.mu.Unlock()
	out := declared(custom, d)
	for _, th := range learned {
		if th.GetDeviceId() == d.GetId() {
			out.apply(th)
		}
	}
	out.summarize()
	return out
}

// Numbers for a node's disk, what a relay's saved cache moves through
func (t *Table) Disk() Numbers {
	return t.Numbers(&v1.Device{Id: DiskDevice, Name: DiskDevice})
}

// Learned numbers for the node record
func (t *Table) Throughputs() []*v1.Throughput {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*v1.Throughput, 0, len(t.rows))
	for _, r := range t.rows {
		out = append(out, proto.Clone(r.Throughput).(*v1.Throughput))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetDeviceId() < out[j].GetDeviceId() })
	return out
}

// Every profile, shipped rows set over left out
func (t *Table) Profiles() []*v1.DeviceProfile {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*v1.DeviceProfile
	for _, p := range merged(t.custom) {
		if p.GetBuiltin() && shadowed(t.custom, p.GetPattern()) {
			continue
		}
		out = append(out, proto.Clone(p).(*v1.DeviceProfile))
	}
	return out
}

// Removes a person's numbers for a pattern; devices it named plan from the shipped table again
func (t *Table) DeleteProfile(ctx context.Context, pattern string) error {
	pattern = strings.TrimSpace(pattern)
	t.mu.Lock()
	at := -1
	stored := ""
	for i, c := range t.custom {
		if strings.EqualFold(c.GetPattern(), pattern) {
			at, stored = i, c.GetPattern()
		}
	}
	t.mu.Unlock()
	if at < 0 {
		return fmt.Errorf("no numbers were set for %q; shipped profiles are not removed, set numbers over them instead", pattern)
	}
	if _, err := t.store.DeleteDeviceProfile(ctx, stored); err != nil {
		return err
	}
	defer t.changed()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.custom = append(t.custom[:at:at], t.custom[at+1:]...)
	return nil
}

// Sets a person's numbers for a pattern, replacing the shipped ones for devices it names
func (t *Table) SetProfile(ctx context.Context, p *v1.DeviceProfile) error {
	pattern := strings.TrimSpace(p.GetPattern())
	if pattern == "" {
		return fmt.Errorf("a device profile needs a pattern")
	}
	if p.GetStreamBytesPerSecond() <= 0 {
		return fmt.Errorf("a device profile needs a streaming bandwidth above zero (stream_bytes_per_second)")
	}
	if p.GetComputeFlops() <= 0 {
		return fmt.Errorf("a device profile needs a compute rate above zero (compute_flops)")
	}
	if p.GetFixedSeconds() < 0 {
		return fmt.Errorf("a device profile's fixed cost cannot be negative (fixed_seconds)")
	}
	row := &v1.DeviceProfile{Pattern: pattern, StreamBytesPerSecond: p.GetStreamBytesPerSecond(), ComputeFlops: p.GetComputeFlops(), FixedSeconds: p.GetFixedSeconds()}
	if err := t.store.PutDeviceProfile(ctx, row); err != nil {
		return err
	}
	defer t.changed()
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, c := range t.custom {
		if strings.EqualFold(c.GetPattern(), pattern) {
			t.custom[i] = row
			return nil
		}
	}
	t.custom = append(t.custom, row)
	return nil
}

// One measured request on one device
type Sample struct {
	DeviceID string
	// Bytes a token read: device weights at the share a token touches plus the cache at the request's context
	BytesPerToken float64
	// Seconds per completion token
	TimePerToken float64
	// Parameters a token touched, experts at their share, for prefill compute
	Parameters float64
	// Prompt tokens and seconds to the first token
	PromptTokens float64
	TimeToFirst  float64
	// Whether each number is usable: enough completion tokens for the first, enough prompt for the second
	Decode, Prefill bool
}

func (t *Table) row(deviceID string) *db.ThroughputRow {
	row, ok := t.rows[deviceID]
	if !ok {
		row = &db.ThroughputRow{Throughput: &v1.Throughput{DeviceId: deviceID, Source: "learned"}}
		t.rows[deviceID] = row
	}
	return row
}

// Learns streaming bandwidth from decode, compute from prefill, and the fixed cost from the
// intercept of time per token against bytes per token once it has its samples
func (t *Table) Record(ctx context.Context, s Sample) error {
	decode := s.Decode && s.TimePerToken > 0 && s.BytesPerToken > 0
	prefill := s.Prefill && s.TimeToFirst > 0 && s.Parameters > 0 && s.PromptTokens > 0
	if !decode && !prefill {
		return nil
	}
	defer t.changed()
	t.mu.Lock()
	defer t.mu.Unlock()
	row := t.row(s.DeviceID)
	th := row.Throughput
	if decode {
		beta := s.BytesPerToken / s.TimePerToken
		th.StreamBytesPerSecond = ewma(th.GetStreamBytesPerSecond(), beta, th.GetStreamSamples() == 0)
		row.SumX += s.BytesPerToken
		row.SumY += s.TimePerToken
		row.SumXY += s.BytesPerToken * s.TimePerToken
		row.SumXX += s.BytesPerToken * s.BytesPerToken
		row.Points++
		th.StreamSamples = row.Points
		if row.Points >= InterceptSamples {
			th.FixedSeconds = intercept(row)
		}
	}
	if prefill {
		gamma := 2 * s.Parameters * s.PromptTokens / s.TimeToFirst
		th.ComputeFlops = ewma(th.GetComputeFlops(), gamma, th.GetComputeSamples() == 0)
		th.ComputeSamples++
	}
	th.Samples++
	th.UpdatedAt = timestamppb.Now()
	return t.store.PutThroughput(ctx, row)
}

// Learns draft acceptance from a request's speculative counters
func (t *Table) RecordAcceptance(ctx context.Context, deviceID string, offered, accepted uint32) error {
	if offered == 0 {
		return nil
	}
	defer t.changed()
	t.mu.Lock()
	defer t.mu.Unlock()
	row := t.row(deviceID)
	rate := math.Min(1, float64(accepted)/float64(offered))
	row.Throughput.Acceptance = ewma(row.Throughput.GetAcceptance(), rate, row.AcceptanceSamples == 0)
	row.AcceptanceSamples++
	row.Throughput.AcceptanceSamples = row.AcceptanceSamples
	row.Throughput.UpdatedAt = timestamppb.Now()
	return t.store.PutThroughput(ctx, row)
}

// Learns a disk's throughput from a relay's saved cache moving through it
func (t *Table) RecordDisk(ctx context.Context, bytes uint64, seconds float64) error {
	if bytes == 0 || seconds <= 0 {
		return nil
	}
	return t.Record(ctx, Sample{DeviceID: DiskDevice, BytesPerToken: float64(bytes), TimePerToken: seconds, Decode: true})
}

// The intercept of time per token against bytes per token, the cost a pass pays with nothing to read
func intercept(r *db.ThroughputRow) float64 {
	n := float64(r.Points)
	den := n*r.SumXX - r.SumX*r.SumX
	if den == 0 {
		return 0
	}
	slope := (n*r.SumXY - r.SumX*r.SumY) / den
	c := (r.SumY - slope*r.SumX) / n
	return math.Max(c, 0)
}

func ewma(old, fresh float64, first bool) float64 {
	if first {
		return fresh
	}
	return (1-smoothing)*old + smoothing*fresh
}

func ratioKey(shape v1.Shape, runtime string, class v1.LinkClass) string {
	return fmt.Sprintf("%d|%s|%d", shape, runtime, class)
}

// The correction for a shape, runtime, and link class: one before anything was measured
func (t *Table) Ratio(shape v1.Shape, runtime string, class v1.LinkClass) (ttft, tpt float64, samples uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.ratios[ratioKey(shape, runtime, class)]; ok && r.GetSamples() > 0 {
		return r.GetTtftRatio(), r.GetTptRatio(), r.GetSamples()
	}
	return 1, 1, 0
}

// Learns how far the model's prediction was from a measured request on a formation
func (t *Table) RecordRatio(ctx context.Context, shape v1.Shape, runtime string, class v1.LinkClass, predictedTTFT, measuredTTFT, predictedTPT, measuredTPT float64) error {
	ttft := predictedTTFT > 0 && measuredTTFT > 0
	tpt := predictedTPT > 0 && measuredTPT > 0
	if !ttft && !tpt {
		return nil
	}
	defer t.changed()
	t.mu.Lock()
	defer t.mu.Unlock()
	key := ratioKey(shape, runtime, class)
	r, ok := t.ratios[key]
	if !ok {
		r = &v1.FormationRatio{Shape: shape, RuntimeId: runtime, LinkClass: class, TtftRatio: 1, TptRatio: 1}
		t.ratios[key] = r
	}
	first := r.GetSamples() == 0
	if ttft {
		r.TtftRatio = ewma(r.GetTtftRatio(), measuredTTFT/predictedTTFT, first)
	}
	if tpt {
		r.TptRatio = ewma(r.GetTptRatio(), measuredTPT/predictedTPT, first)
	}
	r.Samples++
	r.UpdatedAt = timestamppb.Now()
	return t.store.PutRatio(ctx, r)
}

// Every ratio learned
func (t *Table) Ratios() []*v1.FormationRatio {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*v1.FormationRatio, 0, len(t.ratios))
	for _, r := range t.ratios {
		out = append(out, proto.Clone(r).(*v1.FormationRatio))
	}
	sort.Slice(out, func(i, j int) bool {
		return ratioKey(out[i].GetShape(), out[i].GetRuntimeId(), out[i].GetLinkClass()) < ratioKey(out[j].GetShape(), out[j].GetRuntimeId(), out[j].GetLinkClass())
	})
	return out
}
