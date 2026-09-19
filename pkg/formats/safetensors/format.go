package safetensors

import (
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Safetensors shards with the transformers config beside them, the layout checkpoints are published in
type Format struct{}

func (Format) ID() string          { return "safetensors" }
func (Format) Description() string { return "Safetensors shards with a transformers config" }
func (Format) Blurb() string {
	return "Safetensors weights with a transformers config, usually at 16-bit precision"
}
func (Format) Priority() int { return 5 }
func (Format) Requires() []v1.ArtifactRole {
	return []v1.ArtifactRole{v1.ArtifactRole_ARTIFACT_ROLE_CONFIG}
}

// The files loaders read beside the shards, by name
var (
	configNames    = []string{"config.json", "generation_config.json", "preprocessor_config.json", "processor_config.json", "video_preprocessor_config.json"}
	tokenizerNames = []string{"tokenizer.json", "tokenizer.model", "tokenizer_config.json", "special_tokens_map.json", "added_tokens.json", "vocab.json", "vocab.txt", "merges.txt", "spiece.model", "sentencepiece.bpe.model"}
	templateNames  = []string{"chat_template.jinja", "chat_template.json"}
	codePrefixes   = []string{"configuration_", "modeling_", "tokenization_", "processing_", "image_processing_", "feature_extraction_"}
)

func (Format) Classify(p string) (formats.Claim, bool) {
	dir, base := formats.Split(p)
	lower := strings.ToLower(base)
	switch {
	case strings.HasSuffix(lower, ".safetensors"):
		_, index, count, _ := formats.Shard(base[:len(base)-len(".safetensors")])
		group := dir
		if group == "" {
			group = "default"
		}
		return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, Group: group, ShardIndex: index, ShardCount: count}, true
	case strings.HasSuffix(lower, ".safetensors.index.json"):
		return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_INDEX}, true
	case oneOf(base, configNames):
		return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_CONFIG}, true
	case oneOf(base, tokenizerNames) || strings.HasSuffix(base, ".tiktoken"):
		return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER}, true
	case oneOf(base, templateNames):
		return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_TEMPLATE}, true
	case strings.HasSuffix(base, ".py") && hasPrefix(base, codePrefixes):
		return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_CODE}, true
	}
	return formats.Claim{}, false
}

func oneOf(s string, names []string) bool {
	for _, n := range names {
		if s == n {
			return true
		}
	}
	return false
}

func hasPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// Uses the configured class, model type, text model, or diffusers component class, in that order.
func (Format) Architecture(raw *v1.RawModel) string {
	v := formats.First(raw.GetMetadata(), "architectures", "model_type", "text_config.model_type", "_class_name")
	if i := strings.Index(v, ","); i >= 0 {
		v = v[:i]
	}
	return v
}

// Config keys with the text model's fallback for a multimodal checkpoint whose language model sits under text_config
func (Format) Params(raw *v1.RawModel) formats.Params {
	m := raw.GetMetadata()
	both := func(key string) float64 { return formats.Num(m, key, "text_config."+key) }
	return formats.Params{
		Layers:            both("num_hidden_layers"),
		Embedding:         both("hidden_size"),
		Heads:             both("num_attention_heads"),
		HeadsKV:           both("num_key_value_heads"),
		HeadDim:           both("head_dim"),
		ContextTrain:      both("max_position_embeddings"),
		AttentionInterval: both("full_attention_interval"),
		Vocab:             both("vocab_size"),
		Experts:           formats.Num(m, "num_local_experts", "num_experts", "n_routed_experts", "text_config.num_local_experts"),
		ExpertsUsed:       both("num_experts_per_tok"),
		DraftLayers:       both("num_nextn_predict_layers"),
		KVLoraRank:        both("kv_lora_rank"),
		RopeDim:           both("qk_rope_head_dim"),
		SlidingWindow:     both("sliding_window"),
		SlidingPattern:    both("sliding_window_pattern"),
	}
}

var (
	visionWords    = []string{"vision", "vision_tower", "vision_model", "vision_encoder", "vision_backbone", "visual", "vit", "image_encoder", "vpm", "siglip", "clip", "vision_embed_tokens", "aligner", "projector", "mm_projector", "multi_modal_projector", "visual_projector", "vision_projection", "vision_language_adapter", "connector", "resampler", "image_newline", "image_start", "image_end", "image_pad"}
	audioWords     = []string{"audio_tower", "audio_model", "audio_encoder", "speech_encoder", "audio_projector", "audio_aligner", "whisper", "apm"}
	embeddingWords = []string{"embed_tokens", "embed_tokens_per_layer", "embed", "tok_embeddings", "wte", "word_embeddings", "embed_in"}
	outputWords    = []string{"lm_head", "head", "output", "embed_out", "score", "classifier"}
	normWords      = []string{"norm", "ln_f", "final_norm", "final_layernorm", "norm_f"}
)

// Classify encoders and prediction heads before numbered layers. Shared experts remain with their
// layer. Routed experts get a separate group.
func (Format) Tensor(name string) (v1.TensorGroupKind, int32) {
	// A diffusers component keeps the names its pipeline knows, none of which a language model uses
	if kind, ok := diffusion.Kind(name); ok && kind != v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER {
		return kind, -1
	}
	seg := formats.Segments(name)
	switch {
	case formats.HasSegment(seg, "mtp", "nextn"):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, formats.LayerAfter(seg, "mtp", "nextn")
	case formats.HasSegment(seg, visionWords...):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, -1
	case formats.HasSegment(seg, audioWords...):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, -1
	case followed(seg, embeddingWords):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, -1
	}
	if layer := formats.LayerAfter(seg, "layers"); layer >= 0 {
		if formats.HasSegment(seg, "experts") {
			return v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, layer
		}
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, layer
	}
	switch {
	case followed(seg, outputWords):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, -1
	case len(seg) >= 2 && oneOf(seg[len(seg)-2], normWords) && (seg[len(seg)-1] == "weight" || seg[len(seg)-1] == "bias"):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, -1
	case strings.HasPrefix(name, "hc_head_"):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, -1
	}
	return v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, -1
}

// Whether one of the words is a segment with more segments after it
func followed(seg []string, words []string) bool {
	for i := 0; i+1 < len(seg); i++ {
		if oneOf(seg[i], words) {
			return true
		}
	}
	return false
}

// A checkpoint numbers its prediction heads after the layers its config counts
func (Format) DraftFrom(p formats.Params) int32 {
	if p.Layers <= 0 {
		return -1
	}
	return int32(p.Layers)
}

// Counts packed weights using expert dtype widths or quantization config for qweight and qzeros.
func (Format) Elements(t *v1.TensorInfo, raw *v1.RawModel) uint64 {
	n := t.GetElements()
	if n == 0 {
		return 0
	}
	m := raw.GetMetadata()
	var bits uint64
	seg := formats.Segments(t.GetName())
	switch {
	case len(seg) >= 3 && formats.LayerAfter(seg, "experts") >= 0 && seg[len(seg)-1] == "weight":
		bits = packedBits(m["expert_dtype"])
	case len(seg) >= 1 && (seg[len(seg)-1] == "qweight" || seg[len(seg)-1] == "qzeros"):
		bits = uint64(formats.Num(m, "quantization_config.bits", "quantization_config.w_bit"))
	}
	if bits == 0 {
		return n
	}
	if stored := t.GetBytes() * 8 / n; stored > bits {
		return n * stored / bits
	}
	return n
}

// The width an expert dtype such as mxfp4, nvfp4, int4, or nf4 names
func packedBits(dtype string) uint64 {
	d := strings.ToLower(strings.TrimSpace(dtype))
	d = strings.TrimPrefix(strings.TrimPrefix(d, "mx"), "nv")
	for _, prefix := range []string{"fp", "int", "nf"} {
		if rest, ok := strings.CutPrefix(d, prefix); ok {
			if n, err := strconv.ParseUint(rest, 10, 8); err == nil {
				return n
			}
		}
	}
	return 0
}

// A quantization config names the method and width, the checkpoint dtype only when there is none
func (Format) Precision(raw *v1.RawModel, _ string) formats.Words {
	m := raw.GetMetadata()
	var w formats.Words
	matched := false
	if bits := formats.Num(m, "quantization_config.bits", "quantization_config.w_bit"); bits > 0 {
		w.Bits = uint32(bits)
		matched = true
	}
	if method := formats.First(m, "quantization_config.quant_method"); method != "" {
		w.Labels = append(w.Labels, method)
		w.Notes = append(w.Notes, "a calibrated quant that keeps quality close to the original at this bit width")
		matched = true
	}
	if expert := strings.ToLower(formats.First(m, "expert_dtype")); packedBits(expert) == 4 {
		w.Labels = append(w.Labels, expert+" experts")
		w.Notes = append(w.Notes, "the experts stored as "+expert+", two weights to a byte")
		matched = true
	}
	if matched {
		return w
	}
	switch strings.TrimPrefix(formats.First(m, "torch_dtype", "dtype"), "torch.") {
	case "bfloat16":
		w.Bits, w.Labels, w.Notes = 16, []string{"bfloat16"}, []string{"bfloat16, the training format on modern GPUs"}
	case "float16":
		w.Bits, w.Labels, w.Notes = 16, []string{"float16"}, []string{"float16, the training format on older GPUs"}
	case "float32":
		w.Bits, w.Labels = 32, []string{"float32"}
	}
	return w
}

func (Format) Metadata(raw *v1.RawModel) map[string]string {
	out := map[string]string{}
	for _, k := range []string{"torch_dtype", "dtype", "model_type", "_class_name", "quantization_config.quant_method", "quantization_config.bits", "__metadata__.format"} {
		if v, ok := raw.GetMetadata()[k]; ok {
			out[k] = v
		}
	}
	// A diffusers component keeps the names its pipeline knows, which say the family and the parts it bundles
	for k, v := range diffusion.Metadata(raw) {
		out[k] = v
	}
	return out
}
