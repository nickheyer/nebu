// Package descriptor builds format neutral model descriptors from headers.
package descriptor

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	"github.com/nickheyer/nebu/pkg/precision"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

// Builds descriptors using format readers, attention families, and precision tables.
type Builder struct {
	Formats *formats.Registry
	Archs   *archs.Registry
	Scale   precision.Scale
}

// Returns the attention family used to size the descriptor.
func (b *Builder) Family(d *v1.Descriptor) archs.Arch {
	return b.Archs.Get(d.GetFamily())
}

func (b *Builder) Build(raw *v1.RawModel) (*v1.Descriptor, error) {
	f := b.Formats.Get(raw.GetFormatId())
	if f == nil {
		return nil, fmt.Errorf("unknown format %q", raw.GetFormatId())
	}
	d := &v1.Descriptor{
		FormatId:     raw.GetFormatId(),
		Group:        raw.GetGroup(),
		Architecture: f.Architecture(raw),
		Metadata:     f.Metadata(raw),
	}
	params := f.Params(raw)
	groups := map[string]*v1.TensorGroup{}
	maxLayer := int32(-1)
	draftFrom := f.DraftFrom(params)
	for _, t := range raw.GetTensors() {
		kind, layer := f.Tensor(t.GetName())
		// Layers beyond the main stack are prediction heads.
		if draftFrom >= 0 && layer >= draftFrom && (kind == v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER || kind == v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS) {
			kind = v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT
		}
		id := text.Enum(kind)
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
		elements := f.Elements(t, raw)
		g.Bytes += t.GetBytes()
		g.Elements += elements
		d.TotalBytes += t.GetBytes()
		d.ParameterCount += elements
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
	params.Derive(float64(maxLayer + 1))
	d.Params = params.Map()
	if family := b.Archs.Pick(d.Architecture, params); family != nil {
		d.Family = family.ID()
	}
	d.Precision = b.precision(f.Precision(raw, d.GetGroup()), d.GetBitsPerWeight())
	d.Kind = kindOf(d)
	if d.Kind == v1.ModelKind_MODEL_KIND_DIFFUSION {
		d.Generates = diffusion.Generates(diffusion.FamilyOf(d))
	}
	return d, nil
}

// Classifies denoisers, standalone components, and language models. Language models used as text
// encoders retain their language kind.
func kindOf(d *v1.Descriptor) v1.ModelKind {
	var denoiser, part, language bool
	for _, g := range d.GetGroups() {
		switch g.GetKind() {
		case v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION:
			denoiser = true
		case v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE, v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER:
			part = true
		case v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS:
			language = true
		}
	}
	profile := diffusion.ProfileOf(d)
	switch {
	case denoiser || profile.Family != "" && !language:
		return v1.ModelKind_MODEL_KIND_DIFFUSION
	case profile.Component != "" && profile.Component != "llm":
		return v1.ModelKind_MODEL_KIND_COMPONENT
	case diffusion.Component(d.GetArchitecture()) && diffusion.Canonical(d.GetArchitecture()) != "llm":
		return v1.ModelKind_MODEL_KIND_COMPONENT
	case d.GetFormatId() == "diffusion":
		// Non-denoiser checkpoints in this format are components.
		return v1.ModelKind_MODEL_KIND_COMPONENT
	case part && !language:
		return v1.ModelKind_MODEL_KIND_COMPONENT
	}
	return v1.ModelKind_MODEL_KIND_LANGUAGE
}

// Resolves precision labels from format metadata and the level table.
func (b *Builder) precision(w formats.Words, measured float64) *v1.Precision {
	bits := w.Bits
	if bits == 0 && measured > 0 {
		// Round widths above 8 bits and floor quantized widths such as 4.6.
		if measured >= 12 {
			bits = uint32(math.Round(measured))
		} else {
			bits = uint32(math.Floor(measured))
		}
	}
	level := b.Scale.Level(bits)
	out := &v1.Precision{Bits: bits, Label: level.Label, Blurb: level.Blurb, Level: level.Quality}
	if len(w.Labels) > 0 && out.Level > 0 {
		out.Label += " " + strings.Join(w.Labels, ", ")
	}
	if len(w.Notes) > 0 {
		note := strings.Join(w.Notes, ", ")
		out.Blurb = strings.TrimSpace(out.Blurb + " " + strings.ToUpper(note[:1]) + note[1:] + ".")
	}
	return out
}
