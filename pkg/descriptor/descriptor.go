// Package descriptor turns raw header facts into format neutral descriptors.
package descriptor

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/precision"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

// Builds descriptors from raw models through the format that read them, the attention families,
// and the words for precision
type Builder struct {
	Formats *formats.Registry
	Archs   *archs.Registry
	Scale   precision.Scale
}

// The attention family a descriptor was sized by
func (b *Builder) Family(d *v1.Descriptor) archs.Arch {
	return b.Archs.Get(d.GetFamily())
}

// Builds a descriptor from raw header facts
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
		// A layer numbered past the main stack is a prediction head the header counts apart
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
	return d, nil
}

// Puts a weight group's precision into words from what its format read and the level table
func (b *Builder) precision(w formats.Words, measured float64) *v1.Precision {
	bits := w.Bits
	if bits == 0 && measured > 0 {
		// Measured widths sit between the named ones, a rounded 16 and a floored 4.6 read right
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
