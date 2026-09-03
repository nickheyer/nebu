// Package descriptor turns raw header facts into format neutral descriptors.
package descriptor

import (
	"fmt"
	"math"
	"regexp"
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
}

type compiledFormat struct {
	spec    *v1.FormatSpec
	tensors []tensorRule
	params  []paramRule
}

type compiledArch struct {
	spec     *v1.ArchSpec
	re       *regexp.Regexp
	formulas map[string]*eval.Expr
}

// Builds descriptors from raw models using format and arch specs
type Builder struct {
	formats map[string]*compiledFormat
	archs   []*compiledArch
	byID    map[string]*compiledArch
}

// Compiles format and arch specs
func New(formats []*v1.FormatSpec, archs []*v1.ArchSpec) (*Builder, error) {
	b := &Builder{formats: map[string]*compiledFormat{}, byID: map[string]*compiledArch{}}
	for _, f := range formats {
		cf := &compiledFormat{spec: f}
		for _, t := range f.GetTensors() {
			re, err := regexp.Compile(t.GetMatch())
			if err != nil {
				return nil, fmt.Errorf("format %s tensor rule: %w", f.GetId(), err)
			}
			cf.tensors = append(cf.tensors, tensorRule{re: re, kind: t.GetKind()})
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
	for _, t := range raw.GetTensors() {
		kind, layer := f.classify(t.GetName())
		id := eval.EnumShort(kind)
		if layer >= 0 {
			id += "." + strconv.Itoa(int(layer))
			maxLayer = max(maxLayer, layer)
		}
		g, ok := groups[id]
		if !ok {
			g = &v1.TensorGroup{Id: id, Kind: kind, Layer: layer}
			groups[id] = g
		}
		g.Bytes += t.GetBytes()
		g.Elements += t.GetElements()
		d.TotalBytes += t.GetBytes()
		d.ParameterCount += t.GetElements()
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
	for _, a := range b.archs {
		if a.re.MatchString(d.Architecture) {
			d.ArchSpecId = a.spec.GetId()
			break
		}
	}
	return d, nil
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
