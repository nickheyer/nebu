package nemo

import (
	"context"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A NeMo checkpoint packed as one .nemo tar, the older NVIDIA layout
type Packed struct{}

func (Packed) ID() string { return "nemo" }
func (Packed) Description() string {
	return "NeMo archive with model_config.yaml and torch, zarr, or distributed weights"
}
func (Packed) Blurb() string {
	return "NeMo checkpoint in a .nemo archive"
}
func (Packed) Priority() int               { return 6 }
func (Packed) Requires() []v1.ArtifactRole { return nil }

func (Packed) Classify(p string) (formats.Claim, bool) {
	_, base := formats.Split(p)
	if !strings.HasSuffix(strings.ToLower(base), tarExt) {
		return formats.Claim{}, false
	}
	return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, Group: base[:len(base)-len(tarExt)]}, true
}

func (Packed) Read(ctx context.Context, open formats.Opener, g *formats.Group) (*v1.RawModel, error) {
	return (&reader{}).Read(ctx, open, g)
}

func (Packed) Architecture(raw *v1.RawModel) string { return architecture(raw) }

// The packed config keeps its keys at the top level, a NeMo 2 config packed later under config
func (Packed) Params(raw *v1.RawModel) formats.Params {
	m := raw.GetMetadata()
	return formats.Params{
		Layers:            formats.Num(m, "num_layers", "config.num_layers"),
		Embedding:         formats.Num(m, "hidden_size", "config.hidden_size"),
		Heads:             formats.Num(m, "num_attention_heads", "config.num_attention_heads"),
		HeadsKV:           formats.Num(m, "num_query_groups", "config.num_query_groups"),
		HeadDim:           formats.Num(m, "kv_channels", "config.kv_channels"),
		ContextTrain:      formats.Num(m, "max_position_embeddings", "encoder_seq_length", "seq_length", "config.seq_length"),
		Vocab:             formats.Num(m, "vocab_size", "config.vocab_size", "tokenizer.vocab_size"),
		Experts:           formats.Num(m, "num_moe_experts", "config.num_moe_experts"),
		ExpertsUsed:       formats.Num(m, "moe_router_topk", "config.moe_router_topk"),
		EmbeddingElements: embeddingElements(raw),
	}
}

func (Packed) Tensor(name string) (v1.TensorGroupKind, int32)   { return tensor(name) }
func (Packed) DraftFrom(formats.Params) int32                   { return -1 }
func (Packed) Elements(t *v1.TensorInfo, _ *v1.RawModel) uint64 { return t.GetElements() }

// The dtype the config names, or the precision the trainer ran at
func (Packed) Precision(raw *v1.RawModel, _ string) formats.Words {
	m := raw.GetMetadata()
	if w, ok := configPrecision(m); ok {
		return w
	}
	p := strings.TrimSpace(m["precision"])
	base := strings.TrimSuffix(strings.TrimSuffix(p, "-mixed"), "-true")
	switch base {
	case "bf16":
		return formats.Words{Bits: 16, Labels: []string{"bf16"}, Notes: []string{"16-bit bfloat16"}}
	case "16", "fp16":
		return formats.Words{Bits: 16, Labels: []string{"16"}, Notes: []string{"16-bit float16"}}
	case "32", "fp32":
		return formats.Words{Bits: 32, Labels: []string{"32"}}
	}
	return formats.Words{}
}

func (Packed) Metadata(raw *v1.RawModel) map[string]string {
	return keep(raw, "precision", "config.params_dtype", "config.bf16", "config.fp16", "target", "_target_", "config._target_", backendKey, layersKey, "tokenizer.type", "config.seq_length")
}

// A NeMo 2 checkpoint directory, context/model.yaml beside a torch distributed checkpoint under weights/
type Directory struct{}

func (Directory) ID() string { return "nemo2" }
func (Directory) Description() string {
	return "NeMo 2 checkpoint directory, context/model.yaml beside a torch distributed checkpoint under weights/"
}
func (Directory) Blurb() string {
	return "NeMo 2 model config and distributed weights"
}
func (Directory) Priority() int { return 6 }
func (Directory) Requires() []v1.ArtifactRole {
	return []v1.ArtifactRole{v1.ArtifactRole_ARTIFACT_ROLE_CONFIG}
}

// Groups all files under the directory containing weights/ and context/.
func (Directory) Classify(p string) (formats.Claim, bool) {
	root, sub, ok := treeOf(p)
	if !ok {
		return formats.Claim{}, false
	}
	dir, base := formats.Split(sub)
	claim := formats.Claim{Root: root, Tree: true}
	switch {
	case dir == "weights" && strings.HasPrefix(base, "__") && strings.HasSuffix(base, ".distcp"):
		claim.Role = v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS
		claim.Group = root
		if claim.Group == "" {
			claim.Group = "default"
		}
	case dir == "weights" && (base == metaFile || base == "metadata.json" || base == "common.pt"):
		claim.Role = v1.ArtifactRole_ARTIFACT_ROLE_CONFIG
	case dir == "context" && (base == "model.yaml" || base == "io.json"):
		claim.Role = v1.ArtifactRole_ARTIFACT_ROLE_CONFIG
	case strings.HasPrefix(sub, "context/nemo_tokenizer/"):
		claim.Role = v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER
	default:
		return formats.Claim{}, false
	}
	return claim, true
}

// Splits a path at its weights/ or context/ directory: the tree root before it and the path under it
func treeOf(p string) (root, sub string, ok bool) {
	for _, dir := range []string{"weights/", "context/"} {
		if i := strings.LastIndex(p, "/"+dir); i >= 0 {
			return p[:i], p[i+1:], true
		}
		if strings.HasPrefix(p, dir) {
			return "", p, true
		}
	}
	return "", "", false
}

func (Directory) Read(ctx context.Context, open formats.Opener, g *formats.Group) (*v1.RawModel, error) {
	return (&reader{}).Read(ctx, open, g)
}

func (Directory) Architecture(raw *v1.RawModel) string { return architecture(raw) }

func (Directory) Params(raw *v1.RawModel) formats.Params {
	m := raw.GetMetadata()
	return formats.Params{
		Layers:            formats.Num(m, "config.num_layers"),
		Embedding:         formats.Num(m, "config.hidden_size"),
		Heads:             formats.Num(m, "config.num_attention_heads"),
		HeadsKV:           formats.Num(m, "config.num_query_groups"),
		HeadDim:           formats.Num(m, "config.kv_channels"),
		ContextTrain:      formats.Num(m, "config.seq_length"),
		Vocab:             formats.Num(m, "config.vocab_size"),
		Experts:           formats.Num(m, "config.num_moe_experts"),
		ExpertsUsed:       formats.Num(m, "config.moe_router_topk"),
		EmbeddingElements: embeddingElements(raw),
	}
}

func (Directory) Tensor(name string) (v1.TensorGroupKind, int32)   { return tensor(name) }
func (Directory) DraftFrom(formats.Params) int32                   { return -1 }
func (Directory) Elements(t *v1.TensorInfo, _ *v1.RawModel) uint64 { return t.GetElements() }

func (Directory) Precision(raw *v1.RawModel, _ string) formats.Words {
	w, _ := configPrecision(raw.GetMetadata())
	return w
}

func (Directory) Metadata(raw *v1.RawModel) map[string]string {
	return keep(raw, "config.params_dtype", "config.bf16", "config.fp16", "_target_", "config._target_", backendKey, layersKey, "config.seq_length", "config.rotary_base")
}

func architecture(raw *v1.RawModel) string {
	return formats.First(raw.GetMetadata(), archKey)
}

// The dtype the model config names, or the mixed precision flags it sets
func configPrecision(m map[string]string) (formats.Words, bool) {
	switch strings.TrimPrefix(strings.TrimSpace(m["config.params_dtype"]), "torch.") {
	case "bfloat16":
		return formats.Words{Bits: 16, Labels: []string{"bfloat16"}, Notes: []string{"16-bit bfloat16"}}, true
	case "float16":
		return formats.Words{Bits: 16, Labels: []string{"float16"}, Notes: []string{"16-bit float16"}}, true
	case "float32":
		return formats.Words{Bits: 32, Labels: []string{"float32"}}, true
	}
	if strings.EqualFold(strings.TrimSpace(m["config.bf16"]), "true") {
		return formats.Words{Bits: 16, Notes: []string{"16-bit bfloat16"}}, true
	}
	if strings.EqualFold(strings.TrimSpace(m["config.fp16"]), "true") {
		return formats.Words{Bits: 16, Notes: []string{"16-bit float16"}}, true
	}
	return formats.Words{}, false
}

// Megatron names encoders and embeddings by module, layers by number, experts under their layer
func tensor(name string) (v1.TensorGroupKind, int32) {
	seg := formats.Segments(name)
	switch {
	case formats.HasSegment(seg, "vision_model", "vision_encoder", "vision_projection"):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, -1
	case formats.HasSegment(seg, "audio_model", "audio_encoder", "speech_encoder"):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, -1
	case followed(seg, "word_embeddings", "position_embeddings"):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, -1
	}
	if layer := formats.LayerAfter(seg, "layers"); layer >= 0 {
		if formats.HasSegment(seg, "experts") {
			return v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, layer
		}
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, layer
	}
	if followed(seg, "output_layer", "final_layernorm", "final_norm") {
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, -1
	}
	return v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, -1
}

// Whether one of the words is a segment with more segments after it
func followed(seg []string, words ...string) bool {
	for i := 0; i+1 < len(seg); i++ {
		for _, w := range words {
			if seg[i] == w {
				return true
			}
		}
	}
	return false
}

// The elements of the word embedding, from which a config that counts no vocabulary gives one up
func embeddingElements(raw *v1.RawModel) float64 {
	var n uint64
	for _, t := range raw.GetTensors() {
		seg := formats.Segments(t.GetName())
		if len(seg) >= 2 && seg[len(seg)-2] == "word_embeddings" && seg[len(seg)-1] == "weight" {
			n += t.GetElements()
		}
	}
	return float64(n)
}

func keep(raw *v1.RawModel, keys ...string) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := raw.GetMetadata()[k]; ok {
			out[k] = v
		}
	}
	return out
}
