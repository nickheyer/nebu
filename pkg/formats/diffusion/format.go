// Package diffusion reads safetensors and torch checkpoints without configs, including denoisers
// and standalone components.
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

type Format struct{}

func (Format) ID() string          { return "diffusion" }
func (Format) Description() string { return "Single file diffusion checkpoints" }
func (Format) Blurb() string {
	return "Safetensors or torch checkpoints without configs, including diffusion models, VAEs, and text encoders"
}

// Checkpoints with transformers configs use the higher priority safetensors format.
func (Format) Priority() int               { return 4 }
func (Format) Requires() []v1.ArtifactRole { return nil }

var extensions = []string{".safetensors", ".sft", ".ckpt", ".pt", ".pth"}

// Groups checkpoint files by stem, keeping shards together.
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

// Reports whether the weights use torch serialization.
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

func profile(raw *v1.RawModel) Profile {
	return Scan(raw.GetTensors(), raw.GetGroup())
}

func (Format) Architecture(raw *v1.RawModel) string {
	return Architecture(raw)
}

// Architecture infers the denoiser family or component kind from tensors.
func Architecture(raw *v1.RawModel) string {
	p := profile(raw)
	if p.Family != "" {
		return p.Family
	}
	return p.Component
}

// Returns denoiser dimensions or standalone encoder width and vocabulary.
func (Format) Params(raw *v1.RawModel) formats.Params {
	p := profile(raw)
	if p.Family == "" {
		return formats.Params{Embedding: p.Width, Vocab: p.Vocab}
	}
	return formats.Params{Layers: p.Blocks, Embedding: p.Embedding}
}

// Unknown tensors use the helper group.
func (Format) Tensor(name string) (v1.TensorGroupKind, int32) {
	if kind, ok := Any(name); ok {
		return kind, -1
	}
	return v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, -1
}

func (Format) DraftFrom(formats.Params) int32                   { return -1 }
func (Format) Elements(t *v1.TensorInfo, _ *v1.RawModel) uint64 { return t.GetElements() }

// Infers precision from the predominant dtype, falling back to the group name.
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
		w.Bits, w.Labels, w.Notes = 16, []string{"bfloat16"}, []string{"16-bit bfloat16"}
	case "F16":
		w.Bits, w.Labels, w.Notes = 16, []string{"float16"}, []string{"16-bit float16"}
	case "F32":
		w.Bits, w.Labels = 32, []string{"float32"}
	case "F8_E4M3", "F8_E5M2", "FLOAT8_E4M3FN", "FLOAT8_E5M2":
		w.Bits, w.Labels, w.Notes = 8, []string{"float8"}, []string{"8-bit floating point"}
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

// Retains header metadata and inferred diffusion properties.
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

// Descriptor metadata keys.
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
	// Comma-separated bundled slot IDs.
	KeySlots = "diffusion.slots"
	// Standalone autoencoder latent channels and video flag.
	KeyLatentChannels = "diffusion.latent_channels"
	KeyVideoVAE       = "diffusion.video_vae"
)

// Metadata infers family, variant, bundled components, and outputs from tensors. Returns empty for
// unrelated models.
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
	if p.LatentChannels > 0 {
		out[KeyLatentChannels] = strconv.Itoa(int(p.LatentChannels))
		out[KeyVideoVAE] = strconv.FormatBool(p.VideoVAE)
	}
	for kind, n := range p.Kinds {
		if n > 0 {
			out["diffusion.bytes."+strings.ToLower(strings.TrimPrefix(kind.String(), "TENSOR_GROUP_KIND_"))] = fmt.Sprint(n)
		}
	}
	return out
}

// FamilyOf returns the denoiser family, or empty if absent.
func FamilyOf(d *v1.Descriptor) string { return ProfileOf(d).Family }

// PartOf returns the component kind, falling back to the canonical architecture.
func PartOf(d *v1.Descriptor) string {
	if p := ProfileOf(d); p.Component != "" {
		return p.Component
	}
	return Canonical(d.GetArchitecture())
}

// ProfileOf reconstructs a profile from stored descriptor metadata.
func ProfileOf(d *v1.Descriptor) Profile {
	m := d.GetMetadata()
	p := Profile{Variant: m[KeyVariant]}
	// Prefer the configured class over tensor inference.
	switch arch := Canonical(d.GetArchitecture()); {
	case Denoiser(arch):
		p.Family = arch
	case Component(arch):
		p.Component = arch
	default:
		p.Family, p.Component = Canonical(m[KeyFamily]), m[KeyComponent]
	}
	p.VAE, _ = strconv.ParseBool(m[KeyVAE])
	p.TextEncoder, _ = strconv.ParseBool(m[KeyTextEncoder])
	p.ClipVision, _ = strconv.ParseBool(m[KeyClipVision])
	p.ImageInput, _ = strconv.ParseBool(m[KeyImageInput])
	p.AudioInput, _ = strconv.ParseBool(m[KeyAudioInput])
	p.VideoVAE, _ = strconv.ParseBool(m[KeyVideoVAE])
	for _, slot := range strings.Split(m[KeySlots], ",") {
		if slot == "" {
			continue
		}
		if p.Slots == nil {
			p.Slots = map[string]bool{}
		}
		p.Slots[slot] = true
	}
	if n, err := strconv.ParseFloat(m[KeyLatentChannels], 64); err == nil {
		p.LatentChannels = n
	}
	p.Width, p.Vocab = d.GetParams()["n_embd"], d.GetParams()["n_vocab"]
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
