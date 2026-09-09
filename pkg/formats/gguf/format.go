package gguf

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// GGUF, the GGML universal file format, one file per precision with the whole model inside
type Format struct{}

func (Format) ID() string          { return "gguf" }
func (Format) Description() string { return "GGML universal file format" }
func (Format) Blurb() string {
	return "GGUF packs the whole model into one file per precision, usually quantized to fit in less memory"
}
func (Format) Priority() int               { return 10 }
func (Format) Requires() []v1.ArtifactRole { return nil }

// A projector file is the vision or audio encoder loaded beside the weights; a file named as an MTP
// or draft head loads as a draft; an importance matrix is an input to quantization, not weights
func (Format) Classify(p string) (formats.Claim, bool) {
	_, base := formats.Split(p)
	lower := strings.ToLower(base)
	if !strings.HasSuffix(lower, ".gguf") {
		return formats.Claim{}, false
	}
	switch {
	case strings.HasPrefix(lower, "mmproj"):
		return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR}, true
	case headFile(lower):
		return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_DRAFT}, true
	case strings.Contains(lower, "imatrix"):
		return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_OTHER}, true
	}
	stem, index, count, _ := formats.Shard(base[:len(base)-len(".gguf")])
	return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, Group: groupName(p, stem), ShardIndex: index, ShardCount: count}, true
}

// Whether a file name says it holds a prediction head: mtp or draft followed by a separator
func headFile(lower string) bool {
	for _, word := range []string{"mtp", "draft"} {
		if strings.HasPrefix(lower, word) && len(lower) > len(word) && strings.ContainsRune("-_.", rune(lower[len(word)])) {
			return true
		}
	}
	return false
}

// The quant token names the group, with whatever follows it, so model-Q4_K_M and model-Q4_K_M-mtp
// are two groups; a file without a quant token groups by its directory, and one at the root by default
func groupName(p, stem string) string {
	if q, ok := quantOf(stem); ok {
		return q
	}
	if dir, _ := formats.Split(p); dir != "" {
		return dir
	}
	return "default"
}

// The group a file's stem names, the quant token onward with an Unsloth dynamic prefix kept
func quantOf(stem string) (string, bool) {
	for i := 0; i < len(stem); i++ {
		if i > 0 && !strings.ContainsRune("-_./", rune(stem[i-1])) {
			continue
		}
		n, ok := quantToken(stem[i:])
		if !ok {
			continue
		}
		if i+n < len(stem) && stem[i+n] != '-' {
			continue
		}
		if i >= 3 && strings.EqualFold(stem[i-3:i], "UD-") {
			return stem[i-3:], true
		}
		return stem[i:], true
	}
	return "", false
}

// The length of the quant token at the start of s, false when none starts there
//
// Tokens are Q or IQ and a digit with underscore parts after, TQ with a digit pair, or the
// unquantized widths BF16, F16, F32, and MXFP4, all case insensitive.
func quantToken(s string) (int, bool) {
	upper := strings.ToUpper(s)
	for _, fixed := range []string{"BF16", "F16", "F32", "MXFP4"} {
		if strings.HasPrefix(upper, fixed) {
			return len(fixed), true
		}
	}
	if len(upper) >= 5 && strings.HasPrefix(upper, "TQ") && digit(upper[2]) && upper[3] == '_' && digit(upper[4]) {
		return 5, true
	}
	n := 0
	switch {
	case strings.HasPrefix(upper, "IQ"):
		n = 2
	case strings.HasPrefix(upper, "Q"):
		n = 1
	default:
		return 0, false
	}
	if n >= len(upper) || !digit(upper[n]) {
		return 0, false
	}
	n++
	for n < len(upper) && upper[n] == '_' {
		m := n + 1
		for m < len(upper) && alnum(upper[m]) {
			m++
		}
		if m == n+1 {
			break
		}
		n = m
	}
	return n, true
}

func digit(c byte) bool { return c >= '0' && c <= '9' }
func alnum(c byte) bool { return digit(c) || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

func (f Format) Read(ctx context.Context, open formats.Opener, group *formats.Group) (*v1.RawModel, error) {
	raw, err := formats.EachWeight(ctx, open, group, parse)
	if err != nil {
		return nil, err
	}
	// The projector a run loads beside the weights counts with them, the one a launch picks, its
	// tensors joining the table while its header, which describes the encoder alone, stays out
	if files := group.Files[v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR]; len(files) > 0 {
		blob, err := open(ctx, files[0])
		if err != nil {
			return nil, err
		}
		_, tensors, err := parse(blob, blob.Size())
		blob.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", files[0].GetPath(), err)
		}
		raw.Tensors = append(raw.Tensors, tensors...)
	}
	return raw, nil
}

func (Format) Architecture(raw *v1.RawModel) string {
	return formats.First(raw.GetMetadata(), "general.architecture")
}

func (Format) Params(raw *v1.RawModel) formats.Params {
	m := raw.GetMetadata()
	arch := formats.First(m, "general.architecture")
	key := func(suffix string) string { return arch + "." + suffix }
	return formats.Params{
		Layers:            formats.Num(m, key("block_count")),
		Embedding:         formats.Num(m, key("embedding_length")),
		Heads:             formats.Num(m, key("attention.head_count")),
		HeadsKV:           formats.Num(m, key("attention.head_count_kv")),
		HeadDim:           formats.Num(m, key("attention.key_length")),
		HeadDimV:          formats.Num(m, key("attention.value_length")),
		ContextTrain:      formats.Num(m, key("context_length")),
		AttentionInterval: formats.Num(m, key("full_attention_interval")),
		Vocab:             formats.Num(m, key("vocab_size"), "tokenizer.ggml.tokens.length"),
		Experts:           formats.Num(m, key("expert_count")),
		ExpertsUsed:       formats.Num(m, key("expert_used_count")),
		DraftLayers:       formats.Num(m, key("nextn_predict_layers")),
		KVLoraRank:        formats.Num(m, key("attention.kv_lora_rank")),
		RopeDim:           formats.Num(m, key("rope.dimension_count")),
		SlidingWindow:     formats.Num(m, key("attention.sliding_window")),
		SlidingPattern:    formats.Num(m, key("attention.sliding_window_pattern")),
	}
}

// Tensors sit in numbered blocks, prediction heads under nextn, experts under _exps; the projector's
// encoder and adapter tensors come from the mmproj file a run loads beside the weights
func (Format) Tensor(name string) (v1.TensorGroupKind, int32) {
	if rest, ok := strings.CutPrefix(name, "blk."); ok {
		if n, tail, ok := strings.Cut(rest, "."); ok {
			if layer, err := strconv.Atoi(n); err == nil {
				switch {
				case strings.HasPrefix(tail, "nextn."):
					return v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, int32(layer)
				case strings.Contains(tail, "_exps."):
					return v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, int32(layer)
				}
				return v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, int32(layer)
			}
		}
	}
	switch {
	case strings.HasPrefix(name, "a.") || strings.HasPrefix(name, "mm.a."):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, -1
	case strings.HasPrefix(name, "v.") || strings.HasPrefix(name, "mm.") || strings.HasPrefix(name, "resampler."):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, -1
	case strings.HasPrefix(name, "token_embd.") || strings.HasPrefix(name, "per_layer_token_embd."):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, -1
	case strings.HasPrefix(name, "output.") || strings.HasPrefix(name, "output_norm.") || strings.HasPrefix(name, "output_hc_"):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, -1
	}
	return v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, -1
}

// The block count includes the prediction heads, which sit in the last blocks
func (Format) DraftFrom(p formats.Params) int32 {
	if p.Layers <= 0 {
		return -1
	}
	return int32(p.Layers - p.DraftLayers)
}

func (Format) Elements(t *v1.TensorInfo, _ *v1.RawModel) uint64 { return t.GetElements() }

// The quant token in the group name names the width, the flavour of the quant adds words
func (Format) Precision(_ *v1.RawModel, group string) formats.Words {
	var w formats.Words
	q, ok := quantOf(group)
	if !ok {
		return w
	}
	token := q
	if i := strings.Index(token, "-"); i > 0 && !strings.EqualFold(token[:3], "UD-") {
		token = token[:i]
	} else if strings.HasPrefix(strings.ToUpper(token), "UD-") {
		if i := strings.Index(token[3:], "-"); i > 0 {
			token = token[:3+i]
		}
	}
	upper := strings.ToUpper(token)
	name := strings.TrimPrefix(upper, "UD-")
	w.Labels = append(w.Labels, token)
	switch {
	case strings.HasPrefix(name, "IQ") && len(name) > 2 && digit(name[2]):
		w.Bits = uint32(name[2] - '0')
	case strings.HasPrefix(name, "Q") && len(name) > 1 && digit(name[1]):
		w.Bits = uint32(name[1] - '0')
	case strings.HasPrefix(name, "TQ") && len(name) > 2 && digit(name[2]):
		w.Bits = uint32(name[2] - '0')
	case name == "MXFP4":
		w.Bits = 4
	case name == "F32":
		w.Bits = 32
	case name == "BF16":
		w.Bits = 16
		w.Notes = append(w.Notes, "bfloat16, the training format on modern GPUs")
	case name == "F16":
		w.Bits = 16
		w.Notes = append(w.Notes, "float16, the training format on older GPUs")
	}
	if strings.HasPrefix(upper, "UD-") {
		w.Notes = append(w.Notes, "Unsloth dynamic, key layers kept at higher precision")
	}
	if strings.HasPrefix(name, "IQ") {
		w.Notes = append(w.Notes, "importance-matrix quant, better quality than plain quants at the same bits")
	}
	switch {
	case strings.HasSuffix(name, "_K_XL"):
		w.Notes = append(w.Notes, "extra large K-quant, the most careful of its bit width")
	case strings.HasSuffix(name, "_K_L"):
		w.Notes = append(w.Notes, "large K-quant, more of the sensitive layers kept precise")
	case strings.HasSuffix(name, "_K_M"):
		w.Notes = append(w.Notes, "medium K-quant, the recommended balance")
	case strings.HasSuffix(name, "_K_S"):
		w.Notes = append(w.Notes, "small K-quant, a bit smaller and a bit rougher")
	case len(name) == 4 && name[0] == 'Q' && digit(name[1]) && name[2] == '_' && (name[3] == '0' || name[3] == '1'):
		w.Notes = append(w.Notes, "older quant scheme, a K-quant at the same bits is usually better")
	}
	return w
}

// The general keys and the block count, which the descriptor shows beside the architecture
func (Format) Metadata(raw *v1.RawModel) map[string]string {
	m := raw.GetMetadata()
	arch := formats.First(m, "general.architecture")
	out := map[string]string{}
	for _, k := range []string{"general.type", "general.name", "general.basename", "general.size_label", "general.file_type", "general.quantization_version", "general.finetune", arch + ".block_count"} {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}
