package scrape

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	dto "github.com/prometheus/client_model/go"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

var (
	ErrSampleType    = errors.New("sample is not a metrics scrape")
	ErrIncomplete    = errors.New("pool declares no complete metric names")
	ErrMissingGauge  = errors.New("mandatory gauge absent from the scrape")
	ErrBadValue      = errors.New("metric value out of range")
	ErrNotGauge      = errors.New("family is not a gauge, counter or untyped")
	ErrNoKVCapacity  = errors.New("endpoint lacks a positive kv-capacity label")
	ErrNoCapacity    = errors.New("endpoint lacks a positive capacity attribute")
	ErrSlotsOverflow = errors.New("slot tokens exceed the endpoint's kv capacity")
	ErrSlotContext   = errors.New("slot context exceeds the endpoint's kv capacity")
	ErrNoMaxLoRA     = errors.New("adapter cap document lacks the declared field")
	ErrAdapterFormat = errors.New("adapter listing format is not one the collector reads")
)

// Maps scraped families to server metrics using the names the endpoint's pool declares
type Collector struct{}

func (Collector) Name() string { return datalayer.CollectorMetrics }

// Builds server metrics, failing when a declared gauge, label, listing or field is absent or malformed
func (Collector) Extract(ep datalayer.Endpoint, sample any) (any, error) {
	s, ok := sample.(*Sample)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrSampleType, sample)
	}
	c := s.Capabilities
	if !c.Complete() {
		return nil, ErrIncomplete
	}
	m := &datalayer.ServerMetrics{UpdatedAt: s.ScrapedAt}
	queued, err := mandatory(s.Families, c.QueuedRequests, c.QueuedRequestsMatch, c.ReplicaLabel)
	if err != nil {
		return nil, err
	}
	running, err := mandatory(s.Families, c.RunningRequests, c.RunningRequestsMatch, c.ReplicaLabel)
	if err != nil {
		return nil, err
	}
	m.QueuedRequests, m.RunningRequests = count(sum(queued)), count(sum(running))
	switch {
	case c.KVCacheUtilization != "":
		kv, err := mandatory(s.Families, c.KVCacheUtilization, c.KVCacheUtilizationMatch, c.ReplicaLabel)
		if err != nil {
			return nil, err
		}
		if m.KVCacheUtilization, err = meanFraction(kv); err != nil {
			return nil, fmt.Errorf("%s: %w", c.KVCacheUtilization, err)
		}
	case c.SlotsPath != "":
		if err := slotCache(m, ep, s.Slots); err != nil {
			return nil, err
		}
	default:
		if err := staticCache(m, ep); err != nil {
			return nil, err
		}
	}
	if c.CacheConfigInfo != "" {
		if m.BlockSize, m.NumGPUBlocks, err = cacheInfo(s.Families, c); err != nil {
			return nil, fmt.Errorf("%s: %w", c.CacheConfigInfo, err)
		}
	}
	if c.BlockSize != "" {
		vs, err := mandatory(s.Families, c.BlockSize, c.BlockSizeMatch, c.ReplicaLabel)
		if err != nil {
			return nil, err
		}
		m.BlockSize = count(slices.Max(vs))
	}
	if c.NumBlocks != "" {
		vs, err := mandatory(s.Families, c.NumBlocks, c.NumBlocksMatch, c.ReplicaLabel)
		if err != nil {
			return nil, err
		}
		m.NumGPUBlocks = count(sum(vs))
	}
	if c.LoRAInfo != "" {
		if err := loraInfo(m, s.Families[c.LoRAInfo], c); err != nil {
			return nil, fmt.Errorf("%s: %w", c.LoRAInfo, err)
		}
	}
	if c.AdaptersPath != "" {
		if err := adapterListing(m, s, c); err != nil {
			return nil, fmt.Errorf("%s: %w", c.AdaptersPath, err)
		}
	}
	return m, nil
}

// Values of a gauge needing at least one matching series, one per replica when a replica label is named
func mandatory(fams map[string]*dto.MetricFamily, name string, match map[string]string, replica string) ([]float64, error) {
	vs, err := series(fams, name, match, replica)
	if err == nil && len(vs) == 0 {
		return nil, fmt.Errorf("%w: %s%s", ErrMissingGauge, name, selector(match))
	}
	return vs, err
}

// Matching sample values of a family, one per replica at its largest when a replica label is named
func series(fams map[string]*dto.MetricFamily, name string, match map[string]string, replica string) ([]float64, error) {
	fam := fams[name]
	if fam == nil {
		return nil, nil
	}
	var out []float64
	at := map[string]int{}
	for _, m := range fam.GetMetric() {
		if !matches(m, match) {
			continue
		}
		v, err := value(fam.GetType(), m)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if replica == "" {
			out = append(out, v)
			continue
		}
		id := label(m, replica)
		if i, ok := at[id]; ok {
			out[i] = max(out[i], v)
			continue
		}
		at[id] = len(out)
		out = append(out, v)
	}
	return out, nil
}

// The number a gauge, counter or untyped sample holds, refusing NaN, infinities and negatives
func value(t dto.MetricType, m *dto.Metric) (float64, error) {
	var v float64
	switch t {
	case dto.MetricType_GAUGE:
		v = m.GetGauge().GetValue()
	case dto.MetricType_COUNTER:
		v = m.GetCounter().GetValue()
	case dto.MetricType_UNTYPED:
		v = m.GetUntyped().GetValue()
	default:
		return 0, fmt.Errorf("%w: %s", ErrNotGauge, t)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, fmt.Errorf("%w: %v", ErrBadValue, v)
	}
	return v, nil
}

// Whether every label of the matcher has the same value on the series, an absent label reading as empty
func matches(m *dto.Metric, match map[string]string) bool {
	for name, want := range match {
		if label(m, name) != want {
			return false
		}
	}
	return true
}

// The value a series carries for a label, empty when it lacks the label as Prometheus reads it
func label(m *dto.Metric, name string) string {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == name {
			return lp.GetValue()
		}
	}
	return ""
}

// The mean over series of each value read as a fraction, a value above 1 taken as a percentage
func meanFraction(vs []float64) (float64, error) {
	var total float64
	for _, v := range vs {
		if v > 100 {
			return 0, fmt.Errorf("%w: utilization %v", ErrBadValue, v)
		}
		if v > 1 {
			v /= 100
		}
		total += v
	}
	return total / float64(len(vs)), nil
}

// Cells of the endpoint's whole KV pool from its kv-capacity label, an error when absent or not a positive count
func poolCells(ep datalayer.Endpoint) (int, error) {
	n, err := strconv.Atoi(ep.Labels()[datalayer.LabelKVCapacity])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %s", ErrNoKVCapacity, ep.ID())
	}
	return n, nil
}

// KV use as busy slot tokens over the pool the kv-capacity label states, refusing slots larger than it
func slotCache(m *datalayer.ServerMetrics, ep datalayer.Endpoint, slots []Slot) error {
	cells, err := poolCells(ep)
	if err != nil {
		return err
	}
	used := 0
	for _, s := range slots {
		if s.NCtx > cells {
			return fmt.Errorf("%w: slot of %d tokens in a pool of %d", ErrSlotContext, s.NCtx, cells)
		}
		if s.IsProcessing {
			used += s.NPromptTokens
		}
	}
	if used > cells {
		return fmt.Errorf("%w: %d of %d", ErrSlotsOverflow, used, cells)
	}
	m.KVCacheUtilization = float64(used) / float64(cells)
	m.BlockSize, m.NumGPUBlocks = 1, cells
	return nil
}

// KV use as running requests over the capacity attribute, each request one block of kv-capacity over capacity cells
func staticCache(m *datalayer.ServerMetrics, ep datalayer.Endpoint) error {
	capacity, ok := datalayer.Attribute[int](ep, datalayer.CollectorCapacity)
	if !ok || capacity <= 0 {
		return fmt.Errorf("%w: %s", ErrNoCapacity, ep.ID())
	}
	cells, err := poolCells(ep)
	if err != nil {
		return err
	}
	if m.RunningRequests > capacity {
		return fmt.Errorf("%w: %d running of %d", ErrSlotsOverflow, m.RunningRequests, capacity)
	}
	m.KVCacheUtilization = float64(m.RunningRequests) / float64(capacity)
	m.BlockSize, m.NumGPUBlocks = cells/capacity, capacity
	return nil
}

// Block size as the largest and total blocks as the sum over replicas of the info gauge, which must be present
func cacheInfo(fams map[string]*dto.MetricFamily, c datalayer.MetricCapabilities) (int, int, error) {
	fam := fams[c.CacheConfigInfo]
	if len(fam.GetMetric()) == 0 {
		return 0, 0, ErrMissingGauge
	}
	blockSize, perReplica := 0, map[string]int{}
	for i, m := range fam.GetMetric() {
		labels := labelMap(m)
		bs, err := intLabel(labels, c.BlockSizeLabel)
		if err != nil {
			return 0, 0, err
		}
		nb, err := intLabel(labels, c.NumBlocksLabel)
		if err != nil {
			return 0, 0, err
		}
		id := strconv.Itoa(i)
		if c.ReplicaLabel != "" {
			id = labels[c.ReplicaLabel]
		}
		blockSize, perReplica[id] = max(blockSize, bs), max(perReplica[id], nb)
	}
	blocks := 0
	for _, nb := range perReplica {
		blocks += nb
	}
	return blockSize, blocks, nil
}

// Fills LoRA state from the series with the greatest timestamp value, a runtime exporting none serving no adapters
func loraInfo(m *datalayer.ServerMetrics, fam *dto.MetricFamily, c datalayer.MetricCapabilities) error {
	var newest *dto.Metric
	best := math.Inf(-1)
	for _, s := range fam.GetMetric() {
		v, err := value(fam.GetType(), s)
		if err != nil {
			return err
		}
		if v > best {
			newest, best = s, v
		}
	}
	if newest == nil {
		return nil
	}
	labels := labelMap(newest)
	maxLoRA, err := intLabel(labels, c.MaxLoRALabel)
	if err != nil {
		return err
	}
	m.MaxLoRA = maxLoRA
	m.RunningAdapters = adapterSet(labels[c.RunningAdaptersLabel])
	m.WaitingAdapters = adapterSet(labels[c.WaitingAdaptersLabel])
	return nil
}

// Fills LoRA state from the adapter listing, applied adapters running and the cap from the listing or the cap document
func adapterListing(m *datalayer.ServerMetrics, s *Sample, c datalayer.MetricCapabilities) error {
	m.RunningAdapters = map[string]struct{}{}
	m.WaitingAdapters = map[string]struct{}{}
	for _, a := range s.Adapters {
		if a.Applied {
			m.RunningAdapters[a.Name] = struct{}{}
		}
	}
	if c.MaxLoRAPath == "" {
		m.MaxLoRA = len(s.Adapters)
		return nil
	}
	if s.MaxLoRA == nil {
		return fmt.Errorf("%w: %s %s", ErrNoMaxLoRA, c.MaxLoRAPath, c.MaxLoRAField)
	}
	if *s.MaxLoRA < 0 {
		return fmt.Errorf("%w: %s=%d", ErrBadValue, c.MaxLoRAField, *s.MaxLoRA)
	}
	m.MaxLoRA = *s.MaxLoRA
	return nil
}

// Every label of a series by name
func labelMap(m *dto.Metric) map[string]string {
	out := make(map[string]string, len(m.GetLabel()))
	for _, lp := range m.GetLabel() {
		out[lp.GetName()] = lp.GetValue()
	}
	return out
}

// A non-negative integer label, an empty, absent or None label being an error
func intLabel(labels map[string]string, name string) (int, error) {
	raw := strings.TrimSpace(labels[name])
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: label %s=%q", ErrBadValue, name, raw)
	}
	return n, nil
}

// The trimmed non-empty names of a comma separated adapter list
func adapterSet(raw string) map[string]struct{} {
	out := map[string]struct{}{}
	for name := range strings.SplitSeq(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

// A gauge's label matcher in exposition brace form
func selector(match map[string]string) string {
	if len(match) == 0 {
		return ""
	}
	parts := make([]string, 0, len(match))
	for _, k := range slices.Sorted(maps.Keys(match)) {
		parts = append(parts, fmt.Sprintf("%s=%q", k, match[k]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func sum(vs []float64) float64 {
	var t float64
	for _, v := range vs {
		t += v
	}
	return t
}

func count(v float64) int { return int(math.Round(v)) }
