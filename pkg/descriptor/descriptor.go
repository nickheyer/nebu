// Package descriptor turns raw header facts into format neutral descriptors.
package descriptor

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const archToken = "{arch}"

type tensorRule struct {
	re   *regexp.Regexp
	kind v1.TensorGroupKind
}

type paramRule struct {
	name   string
	keys   []string
	derive *eval.Expr
	tensor *regexp.Regexp
}

type precisionRule struct {
	spec *v1.PrecisionRule
	re   *regexp.Regexp
}

type packRule struct {
	tensor *regexp.Regexp
	key    string
	value  *regexp.Regexp
}

type compiledFormat struct {
	spec       *v1.FormatSpec
	tensors    []tensorRule
	params     []paramRule
	precisions []precisionRule
	packing    []packRule
	draftFrom  *eval.Expr
}

type compiledArch struct {
	spec     *v1.ArchSpec
	re       *regexp.Regexp
	formulas map[string]*eval.Expr
}

// Builds descriptors from raw models using format, arch, and precision specs
type Builder struct {
	formats map[string]*compiledFormat
	archs   []*compiledArch
	byID    map[string]*compiledArch
	levels  []*v1.PrecisionLevel
}

// Compiles format, arch, and precision specs
func New(formats []*v1.FormatSpec, archs []*v1.ArchSpec, precisions []*v1.PrecisionSpec) (*Builder, error) {
	b := &Builder{formats: map[string]*compiledFormat{}, byID: map[string]*compiledArch{}}
	for _, p := range precisions {
		b.levels = append(b.levels, p.GetLevels()...)
	}
	sort.SliceStable(b.levels, func(i, j int) bool { return b.levels[i].GetBits() > b.levels[j].GetBits() })
	for _, f := range formats {
		cf := &compiledFormat{spec: f}
		for _, t := range f.GetTensors() {
			re, err := regexp.Compile(t.GetMatch())
			if err != nil {
				return nil, fmt.Errorf("format %s tensor rule: %w", f.GetId(), err)
			}
			cf.tensors = append(cf.tensors, tensorRule{re: re, kind: t.GetKind()})
		}
		for _, r := range f.GetPrecisions() {
			re, err := regexp.Compile(r.GetMatch())
			if err != nil {
				return nil, fmt.Errorf("format %s precision rule: %w", f.GetId(), err)
			}
			if r.GetKey() == "" {
				return nil, fmt.Errorf("format %s precision rule %q: key required", f.GetId(), r.GetMatch())
			}
			cf.precisions = append(cf.precisions, precisionRule{spec: r, re: re})
		}
		for _, r := range f.GetPacking() {
			tensor, err := regexp.Compile(r.GetMatch())
			if err != nil {
				return nil, fmt.Errorf("format %s packing rule: %w", f.GetId(), err)
			}
			value, err := regexp.Compile(r.GetValue())
			if err != nil {
				return nil, fmt.Errorf("format %s packing rule %s: %w", f.GetId(), r.GetKey(), err)
			}
			if r.GetKey() == "" || value.SubexpIndex("bits") < 0 {
				return nil, fmt.Errorf("format %s packing rule %q: key and a bits capture required", f.GetId(), r.GetMatch())
			}
			cf.packing = append(cf.packing, packRule{tensor: tensor, key: r.GetKey(), value: value})
		}
		if f.GetDraftFrom() != "" {
			e, err := eval.Compile(f.GetDraftFrom())
			if err != nil {
				return nil, fmt.Errorf("format %s draft_from: %w", f.GetId(), err)
			}
			cf.draftFrom = e
		}
		for _, p := range f.GetParams() {
			rule := paramRule{name: p.GetName(), keys: p.GetKeys()}
			if p.GetDerive() != "" {
				e, err := eval.Compile(p.GetDerive())
				if err != nil {
					return nil, fmt.Errorf("format %s param %s: %w", f.GetId(), p.GetName(), err)
				}
				rule.derive = e
			}
			if p.GetTensor() != "" {
				re, err := regexp.Compile(p.GetTensor())
				if err != nil {
					return nil, fmt.Errorf("format %s param %s tensor: %w", f.GetId(), p.GetName(), err)
				}
				rule.tensor = re
			}
			cf.params = append(cf.params, rule)
		}
		b.formats[f.GetId()] = cf
	}
	for _, a := range archs {
		re, err := regexp.Compile(a.GetMatch())
		if err != nil {
			return nil, fmt.Errorf("arch %s: %w", a.GetId(), err)
		}
		formulas := map[string]*eval.Expr{}
		for name, src := range a.GetFormulas() {
			e, err := eval.Compile(src)
			if err != nil {
				return nil, fmt.Errorf("arch %s formula %s: %w", a.GetId(), name, err)
			}
			formulas[name] = e
		}
		ca := &compiledArch{spec: a, re: re, formulas: formulas}
		b.archs = append(b.archs, ca)
		b.byID[a.GetId()] = ca
	}
	sort.SliceStable(b.archs, func(i, j int) bool {
		if b.archs[i].spec.GetPriority() != b.archs[j].spec.GetPriority() {
			return b.archs[i].spec.GetPriority() > b.archs[j].spec.GetPriority()
		}
		return b.archs[i].spec.GetId() < b.archs[j].spec.GetId()
	})
	return b, nil
}

// Returns compiled formulas for an arch spec id
func (b *Builder) Formulas(archSpecID string) map[string]*eval.Expr {
	if a, ok := b.byID[archSpecID]; ok {
		return a.formulas
	}
	return nil
}

// Builds a descriptor from raw header facts
func (b *Builder) Build(raw *v1.RawModel) (*v1.Descriptor, error) {
	f, ok := b.formats[raw.GetFormatId()]
	if !ok {
		return nil, fmt.Errorf("unknown format %q", raw.GetFormatId())
	}
	d := &v1.Descriptor{
		FormatId: raw.GetFormatId(),
		Group:    raw.GetGroup(),
		Params:   map[string]float64{},
		Metadata: map[string]string{},
	}
	for _, key := range f.spec.GetArchitecture() {
		if v := strings.TrimSpace(raw.GetMetadata()[key]); v != "" {
			d.Architecture = strings.Split(v, ",")[0]
			break
		}
	}
	sub := func(k string) string { return strings.ReplaceAll(k, archToken, d.Architecture) }
	for _, rule := range f.params {
		for _, key := range rule.keys {
			v, ok := raw.GetMetadata()[sub(key)]
			if !ok {
				continue
			}
			if n, err := eval.ParseNumber(v); err == nil {
				d.Params[rule.name] = n
				break
			}
		}
	}
	groups := map[string]*v1.TensorGroup{}
	maxLayer := int32(-1)
	elements := f.elements(raw)
	draftFrom := f.draftStart(d.Params)
	for idx, t := range raw.GetTensors() {
		kind, layer := f.classify(t.GetName())
		// A layer numbered past the main stack is a prediction head the header counts apart
		if draftFrom >= 0 && layer >= draftFrom && (kind == v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER || kind == v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS) {
			kind = v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT
		}
		id := eval.EnumShort(kind)
		if layer >= 0 {
			id += "." + strconv.Itoa(int(layer))
			if kind != v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT {
				maxLayer = max(maxLayer, layer)
			}
		}
		g, ok := groups[id]
		if !ok {
			g = &v1.TensorGroup{Id: id, Kind: kind, Layer: layer}
			groups[id] = g
		}
		g.Bytes += t.GetBytes()
		g.Elements += elements[idx]
		d.TotalBytes += t.GetBytes()
		d.ParameterCount += elements[idx]
	}
	for _, rule := range f.params {
		if rule.tensor == nil {
			continue
		}
		var n uint64
		for idx, t := range raw.GetTensors() {
			if rule.tensor.MatchString(t.GetName()) {
				n += elements[idx]
			}
		}
		d.Params[rule.name] = float64(n)
	}
	for _, g := range groups {
		d.Groups = append(d.Groups, g)
	}
	sort.Slice(d.Groups, func(i, j int) bool {
		if d.Groups[i].GetKind() != d.Groups[j].GetKind() {
			return d.Groups[i].GetKind() < d.Groups[j].GetKind()
		}
		return d.Groups[i].GetLayer() < d.Groups[j].GetLayer()
	})
	if d.ParameterCount > 0 {
		d.BitsPerWeight = float64(d.TotalBytes) * 8 / float64(d.ParameterCount)
	}
	env := map[string]any{
		"layers_seen":     float64(maxLayer + 1),
		"total_bytes":     float64(d.TotalBytes),
		"parameter_count": float64(d.ParameterCount),
	}
	for k, v := range d.Params {
		env[k] = v
	}
	for pass := 0; pass < 2; pass++ {
		for _, rule := range f.params {
			if rule.derive == nil {
				continue
			}
			if _, set := d.Params[rule.name]; set {
				continue
			}
			v, err := rule.derive.Float(env)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			d.Params[rule.name] = v
			env[rule.name] = v
		}
	}
	for _, key := range f.spec.GetMetadata() {
		if v, ok := raw.GetMetadata()[sub(key)]; ok {
			d.Metadata[sub(key)] = v
		}
	}
	d.ArchSpecId = b.arch(d)
	d.Precision = b.precision(f, raw, d)
	return d, nil
}

// The first arch whose pattern matches and whose formulas evaluate over the
// params read, so a family spec never claims a checkpoint missing what it
// needs, the last match standing in when none evaluates
func (b *Builder) arch(d *v1.Descriptor) string {
	last := ""
	for _, a := range b.archs {
		if !a.re.MatchString(d.GetArchitecture()) {
			continue
		}
		last = a.spec.GetId()
		env := make(map[string]any, len(d.GetParams()))
		for k, v := range d.GetParams() {
			env[k] = v
		}
		if eval.Solve(a.formulas, env) == nil {
			return a.spec.GetId()
		}
	}
	return last
}

// The weights each tensor holds, a packed tensor counting every weight its elements carry
func (f *compiledFormat) elements(raw *v1.RawModel) []uint64 {
	widths := make([]uint64, len(f.packing))
	for i, rule := range f.packing {
		m := rule.value.FindStringSubmatch(raw.GetMetadata()[rule.key])
		if m == nil {
			continue
		}
		widths[i], _ = strconv.ParseUint(m[rule.value.SubexpIndex("bits")], 10, 32)
	}
	out := make([]uint64, len(raw.GetTensors()))
	for idx, t := range raw.GetTensors() {
		n := t.GetElements()
		for i, rule := range f.packing {
			if widths[i] == 0 || n == 0 || !rule.tensor.MatchString(t.GetName()) {
				continue
			}
			if stored := t.GetBytes() * 8 / n; stored > widths[i] {
				n = n * stored / widths[i]
			}
		}
		out[idx] = n
	}
	return out
}

// Puts a weight group's precision into words from the format's rules and the level table
func (b *Builder) precision(f *compiledFormat, raw *v1.RawModel, d *v1.Descriptor) *v1.Precision {
	var bits uint32
	var labels, notes []string
	matched := false
	for _, rule := range f.precisions {
		if rule.spec.GetFallback() && matched {
			continue
		}
		text := d.GetGroup()
		if rule.spec.GetKey() != "group" {
			text = raw.GetMetadata()[rule.spec.GetKey()]
		}
		m := rule.re.FindStringSubmatchIndex(text)
		if m == nil {
			continue
		}
		matched = true
		width := rule.spec.GetBits()
		if i := rule.re.SubexpIndex("bits"); i >= 0 && m[2*i] >= 0 {
			if n, err := strconv.ParseUint(text[m[2*i]:m[2*i+1]], 10, 32); err == nil {
				width = uint32(n)
			}
		}
		// The first rule carrying a width claims it, later widths are other readings of the same group
		if width > 0 {
			if bits > 0 {
				continue
			}
			bits = width
		}
		label := rule.spec.GetLabel()
		if label != "" {
			label = string(rule.re.ExpandString(nil, label, text, m))
		} else if i := rule.re.SubexpIndex("name"); i >= 0 && m[2*i] >= 0 {
			label = text[m[2*i]:m[2*i+1]]
		}
		if label != "" && !slices.Contains(labels, label) {
			labels = append(labels, label)
		}
		if rule.spec.GetNote() != "" {
			notes = append(notes, string(rule.re.ExpandString(nil, rule.spec.GetNote(), text, m)))
		}
	}
	if bits == 0 && d.GetBitsPerWeight() > 0 {
		// Measured widths sit between the named ones, a rounded 16 and a floored 4.6 read right
		if d.GetBitsPerWeight() >= 12 {
			bits = uint32(math.Round(d.GetBitsPerWeight()))
		} else {
			bits = uint32(math.Floor(d.GetBitsPerWeight()))
		}
	}
	out := &v1.Precision{Bits: bits}
	for _, l := range b.levels {
		if bits >= l.GetBits() {
			out.Label, out.Blurb, out.Level = l.GetLabel(), l.GetBlurb(), l.GetLevel()
			break
		}
	}
	if len(labels) > 0 && out.Level > 0 {
		out.Label += " " + strings.Join(labels, ", ")
	}
	if len(notes) > 0 {
		note := strings.Join(notes, ", ")
		out.Blurb = strings.TrimSpace(out.Blurb + " " + strings.ToUpper(note[:1]) + note[1:] + ".")
	}
	return out
}

// The first layer index that is a draft layer under the format's rule, -1 when there is no
// rule or the header lacks the facts it reads
func (f *compiledFormat) draftStart(params map[string]float64) int32 {
	if f.draftFrom == nil {
		return -1
	}
	env := make(map[string]any, len(params))
	for k, v := range params {
		env[k] = v
	}
	v, err := f.draftFrom.Float(env)
	if err != nil || math.IsNaN(v) || v < 0 || v > math.MaxInt32 {
		return -1
	}
	return int32(v)
}

func (f *compiledFormat) classify(name string) (v1.TensorGroupKind, int32) {
	for _, rule := range f.tensors {
		m := rule.re.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		layer := int32(-1)
		for i, sub := range rule.re.SubexpNames() {
			if sub == "layer" && i < len(m) {
				if n, err := strconv.Atoi(m[i]); err == nil {
					layer = int32(n)
				}
			}
		}
		return rule.kind, layer
	}
	return v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, -1
}
