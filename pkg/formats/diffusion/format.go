// Package diffusion reads the checkpoints diffusion models are published as: one safetensors or
// torch file per model, with no config beside it, holding a denoiser with or without the
// autoencoder and text encoders it samples with, or one of those parts on its own.
package diffusion

import (
	"context"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/torch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Single file diffusion checkpoints, safetensors or torch, as image and video models are published
type Format struct{}

func (Format) ID() string          { return "diffusion" }
func (Format) Description() string { return "Single file diffusion checkpoints" }
func (Format) Blurb() string {
	return "One safetensors or torch file holds a diffusion model, or one part of its pipeline such as a VAE or a text encoder, with no config beside it"
}

// Below the safetensors format, so a checkpoint with a transformers config keeps that format and one without falls here
func (Format) Priority() int               { return 4 }
func (Format) Requires() []v1.ArtifactRole { return nil }

var extensions = []string{".safetensors", ".sft", ".ckpt", ".pt", ".pth"}

// Every checkpoint file is a group of its own named by its stem, shards of one file sharing the stem
func (Format) Classify(p string) (formats.Claim, bool) {
	_, base := formats.Split(p)
	lower := strings.ToLower(base)
	ext := ""
	for _, e := range extensions {
		if strings.HasSuffix(lower, e) {
			ext = e
			break
		}
	}
	if ext == "" {
		return formats.Claim{}, false
	}
	stem, index, count, _ := formats.Shard(base[:len(base)-len(ext)])
	return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, Group: stem, ShardIndex: index, ShardCount: count}, true
}

func (Format) Read(ctx context.Context, open formats.Opener, g *formats.Group) (*v1.RawModel, error) {
	return formats.EachWeight(ctx, open, g, func(ra io.ReaderAt, size int64) (map[string]string, []*v1.TensorInfo, error) {
		if isTorch(g) {
			ck, err := torch.ReadZip(ra, size)
			if err != nil {
				return nil, nil, err
			}
			return ck.Metadata, ck.Tensors, nil
		}
		return formats.SafetensorsHeader(ra, size)
	})
}

// Whether the group's files are torch pickles rather than safetensors
func isTorch(g *formats.Group) bool {
	if len(g.Weights) == 0 {
		return false
	}
	switch strings.ToLower(path.Ext(g.Weights[0].GetPath())) {
	case ".ckpt", ".pt", ".pth":
		return true
	}
	return false
}

// The profile of a checkpoint, read from the tensors the header listed
func profile(raw *v1.RawModel) Profile {
	return Scan(raw.GetTensors(), raw.GetGroup())
}

// The family of the denoiser, or the part a checkpoint without one is
func (Format) Architecture(raw *v1.RawModel) string {
	return Architecture(raw)
}

// Architecture names what a checkpoint holds from its tensors alone: the family of its denoiser, else
// the part it is, else nothing; any format whose header names no architecture asks this
func Architecture(raw *v1.RawModel) string {
	p := profile(raw)
	if p.Family != "" {
		return p.Family
	}
	return p.Component
}

// The blocks and width of the denoiser, which size its activations
func (Format) Params(raw *v1.RawModel) formats.Params {
	p := profile(raw)
	return formats.Params{Layers: p.Blocks, Embedding: p.Embedding}
}

// A name the diffusion rules do not know is a helper tensor, never a layer of a language model
func (Format) Tensor(name string) (v1.TensorGroupKind, int32) {
	if kind, ok := Any(name); ok {
		return kind, -1
	}
	return v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, -1
}

func (Format) DraftFrom(formats.Params) int32                   { return -1 }
func (Format) Elements(t *v1.TensorInfo, _ *v1.RawModel) uint64 { return t.GetElements() }

// The precision the tensors are stored at, from the dtype most of the denoiser's bytes take, else the name
func (Format) Precision(raw *v1.RawModel, group string) formats.Words {
	bytesBy := map[string]uint64{}
	for _, t := range raw.GetTensors() {
		bytesBy[strings.ToUpper(t.GetDtype())] += t.GetBytes()
	}
	var top string
	for dtype, n := range bytesBy {
		if top == "" || n > bytesBy[top] {
			top = dtype
		}
	}
	var w formats.Words
	switch top {
	case "BF16":
		w.Bits, w.Labels, w.Notes = 16, []string{"bfloat16"}, []string{"bfloat16, the training format on modern GPUs"}
	case "F16":
		w.Bits, w.Labels, w.Notes = 16, []string{"float16"}, []string{"float16, the training format on older GPUs"}
	case "F32":
		w.Bits, w.Labels = 32, []string{"float32"}
	case "F8_E4M3", "F8_E5M2", "FLOAT8_E4M3FN", "FLOAT8_E5M2":
		w.Bits, w.Labels, w.Notes = 8, []string{"float8"}, []string{"8-bit floating point, half the size of 16-bit with little lost"}
	}
	lower := strings.ToLower(group)
	if w.Bits == 0 {
		switch {
		case strings.Contains(lower, "fp8") || strings.Contains(lower, "e4m3"):
			w.Bits, w.Labels = 8, []string{"float8"}
		case strings.Contains(lower, "fp16") || strings.Contains(lower, "bf16"):
			w.Bits = 16
		case strings.Contains(lower, "int8"):
			w.Bits, w.Labels = 8, []string{"int8"}
		}
	}
	return w
}

// What the header said of itself, the family, and which parts the checkpoint bundles
func (Format) Metadata(raw *v1.RawModel) map[string]string {
	out := map[string]string{}
	for k, v := range raw.GetMetadata() {
		if strings.HasPrefix(k, "__metadata__.") && len(v) < 200 {
			out[k] = v
		}
	}
	for k, v := range Metadata(raw) {
		out[k] = v
	}
	return out
}

// The keys every descriptor of a diffusion checkpoint carries, whatever format holds it
const (
	KeyFamily      = "diffusion.family"
	KeyVariant     = "diffusion.variant"
	KeyComponent   = "diffusion.component"
	KeyVAE         = "diffusion.vae"
	KeyTextEncoder = "diffusion.text_encoder"
	KeyClipVision  = "diffusion.clip_vision"
	KeyImageInput  = "diffusion.image_input"
	KeyAudioInput  = "diffusion.audio_input"
	KeyGenerates   = "diffusion.generates"
)

// Metadata reads the family, its variant, the parts a checkpoint bundles, and what it makes, off the
// tensors of any format, empty for a file that is no part of a diffusion pipeline
func Metadata(raw *v1.RawModel) map[string]string {
	out := map[string]string{}
	p := profile(raw)
	if p.Family != "" {
		out[KeyFamily] = p.Family
		if p.Variant != "" {
			out[KeyVariant] = p.Variant
		}
		out[KeyVAE] = strconv.FormatBool(p.VAE)
		out[KeyTextEncoder] = strconv.FormatBool(p.TextEncoder)
		out[KeyClipVision] = strconv.FormatBool(p.ClipVision)
		out[KeyImageInput] = strconv.FormatBool(p.ImageInput)
		out[KeyAudioInput] = strconv.FormatBool(p.AudioInput)
		out[KeyGenerates] = strings.Join(Generates(p.Family), ",")
	}
	if p.Component != "" {
		out[KeyComponent] = p.Component
	}
	for kind, n := range p.Kinds {
		if n > 0 {
			out["diffusion.bytes."+strings.ToLower(strings.TrimPrefix(kind.String(), "TENSOR_GROUP_KIND_"))] = fmt.Sprint(n)
		}
	}
	return out
}

// ProfileOf rebuilds the profile a descriptor's metadata recorded, so a runtime reads the parts a
// stored model needs without the headers
func ProfileOf(d *v1.Descriptor) Profile {
	m := d.GetMetadata()
	p := Profile{Family: Canonical(m[KeyFamily]), Variant: m[KeyVariant], Component: m[KeyComponent]}
	if p.Family == "" && Denoiser(d.GetArchitecture()) {
		p.Family = Canonical(d.GetArchitecture())
	}
	if p.Component == "" && Component(d.GetArchitecture()) {
		p.Component = Canonical(d.GetArchitecture())
	}
	p.VAE, _ = strconv.ParseBool(m[KeyVAE])
	p.TextEncoder, _ = strconv.ParseBool(m[KeyTextEncoder])
	p.ClipVision, _ = strconv.ParseBool(m[KeyClipVision])
	p.ImageInput, _ = strconv.ParseBool(m[KeyImageInput])
	p.AudioInput, _ = strconv.ParseBool(m[KeyAudioInput])
	for _, g := range d.GetGroups() {
		switch g.GetKind() {
		case v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE:
			p.VAE = true
		case v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER:
			p.TextEncoder = true
		case v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION:
			p.ClipVision = true
		}
	}
	return p
}
