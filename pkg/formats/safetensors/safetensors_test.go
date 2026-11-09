package safetensors

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

type blob struct{ *bytes.Reader }

func (blob) Close() error { return nil }

// An opener over in memory files by path
func opener(files map[string][]byte) formats.Opener {
	return func(_ context.Context, a *v1.Artifact) (sources.Blob, error) {
		data, ok := files[a.GetPath()]
		if !ok {
			return nil, errors.New("no file " + a.GetPath())
		}
		return blob{bytes.NewReader(data)}, nil
	}
}

// A safetensors shard holding the tensors and metadata, the data region zero filled
func shard(t *testing.T, tensors map[string]any, metadata map[string]string) []byte {
	t.Helper()
	header := map[string]any{}
	for k, v := range tensors {
		header[k] = v
	}
	if metadata != nil {
		header["__metadata__"] = metadata
	}
	js, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint64(len(js)))
	buf.Write(js)
	buf.Write(make([]byte, 64))
	return buf.Bytes()
}

func entry(dtype string, shape []uint64, from, to uint64) map[string]any {
	return map[string]any{"dtype": dtype, "shape": shape, "data_offsets": []uint64{from, to}}
}

// The format under test
var format = Format{}

func artifact(p string, size int) *v1.Artifact {
	return &v1.Artifact{Path: p, SizeBytes: uint64(size)}
}

// Shards group by directory with their index, configs, tokenizers, templates and code each take their role
func TestClassify(t *testing.T) {
	for p, want := range map[string]struct {
		ok           bool
		role         v1.ArtifactRole
		group        string
		index, count uint32
	}{
		"model.safetensors":                {true, v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, "default", 0, 0},
		"model-00002-of-00003.safetensors": {true, v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, "default", 2, 3},
		"text_encoder/model.SafeTensors":   {true, v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, "text_encoder", 0, 0},
		"model.safetensors.index.json":     {true, v1.ArtifactRole_ARTIFACT_ROLE_INDEX, "", 0, 0},
		"config.json":                      {true, v1.ArtifactRole_ARTIFACT_ROLE_CONFIG, "", 0, 0},
		"sub/generation_config.json":       {true, v1.ArtifactRole_ARTIFACT_ROLE_CONFIG, "", 0, 0},
		"tokenizer.json":                   {true, v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER, "", 0, 0},
		"vocab.txt":                        {true, v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER, "", 0, 0},
		"cl100k.tiktoken":                  {true, v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER, "", 0, 0},
		"chat_template.jinja":              {true, v1.ArtifactRole_ARTIFACT_ROLE_TEMPLATE, "", 0, 0},
		"modeling_llama.py":                {true, v1.ArtifactRole_ARTIFACT_ROLE_CODE, "", 0, 0},
		"setup.py":                         {false, 0, "", 0, 0},
		"README.md":                        {false, 0, "", 0, 0},
		"model.bin":                        {false, 0, "", 0, 0},
	} {
		claim, ok := format.Classify(p)
		if ok != want.ok || claim.Role != want.role || claim.Group != want.group || claim.ShardIndex != want.index || claim.ShardCount != want.count {
			t.Fatalf("Classify(%s) = %+v %v, want %+v", p, claim, ok, want)
		}
	}
	if format.ID() != "safetensors" || format.Priority() != 5 || len(format.Requires()) != 1 || format.Description() == "" || format.Blurb() == "" {
		t.Fatal("descriptor")
	}
}

// Every shard's tensors are read in shard order with the header metadata and the flattened config beside them
func TestRead(t *testing.T) {
	one := shard(t, map[string]any{"model.embed_tokens.weight": entry("BF16", []uint64{4, 2}, 0, 16), "model.layers.0.mlp.weight": entry("BF16", []uint64{2, 2}, 16, 24)}, map[string]string{"format": "pt"})
	two := shard(t, map[string]any{"lm_head.weight": entry("F32", []uint64{4, 2}, 0, 32)}, map[string]string{"format": "ignored", "extra": "two"})
	config := []byte(`{"architectures": ["LlamaForCausalLM"], "model_type": "llama", "num_hidden_layers": 1, "hidden_size": 2, "num_attention_heads": 2, "num_key_value_heads": 1, "vocab_size": 4, "torch_dtype": "bfloat16", "max_position_embeddings": 64}`)
	files := map[string][]byte{"a-00001-of-00002.safetensors": one, "a-00002-of-00002.safetensors": two, "config.json": config}
	group := &formats.Group{FormatID: "safetensors", Name: "default",
		Weights: []*v1.Artifact{artifact("a-00001-of-00002.safetensors", len(one)), artifact("a-00002-of-00002.safetensors", len(two))},
		Files:   map[v1.ArtifactRole][]*v1.Artifact{v1.ArtifactRole_ARTIFACT_ROLE_CONFIG: {artifact("config.json", len(config))}}}
	raw, err := format.Read(context.Background(), opener(files), group)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.GetTensors()) != 3 || raw.GetTensors()[2].GetName() != "lm_head.weight" || raw.GetTensors()[2].GetBytes() != 32 || raw.GetTensors()[2].GetElements() != 8 {
		t.Fatalf("tensors %v", raw.GetTensors())
	}
	m := raw.GetMetadata()
	if m["__metadata__.format"] != "ignored" && m["__metadata__.format"] != "pt" || m["__metadata__.extra"] != "two" || m["architectures"] != "LlamaForCausalLM" || m["num_hidden_layers"] != "1" {
		t.Fatalf("metadata %v", m)
	}
	if format.Architecture(raw) != "LlamaForCausalLM" {
		t.Fatalf("architecture %q", format.Architecture(raw))
	}
	p := format.Params(raw)
	if p.Layers != 1 || p.Embedding != 2 || p.Heads != 2 || p.HeadsKV != 1 || p.Vocab != 4 || p.ContextTrain != 64 {
		t.Fatalf("params %+v", p)
	}
	if format.DraftFrom(p) != 1 || format.DraftFrom(formats.Params{}) != -1 {
		t.Fatal("draft heads follow the layer count")
	}
	if w := format.Precision(raw, ""); w.Bits != 16 || w.Labels[0] != "bfloat16" {
		t.Fatalf("precision %+v", w)
	}
	if md := format.Metadata(raw); md["torch_dtype"] != "bfloat16" || md["model_type"] != "llama" || md["__metadata__.format"] == "" {
		t.Fatalf("kept metadata %v", md)
	}
	// A missing shard, a broken header and a config past its limit each fail naming the file
	for name, g := range map[string]*formats.Group{
		"missing shard":  {Weights: []*v1.Artifact{artifact("nope.safetensors", 8)}},
		"broken header":  {Weights: []*v1.Artifact{artifact("config.json", len(config))}},
		"config too big": {Weights: nil, Files: map[v1.ArtifactRole][]*v1.Artifact{v1.ArtifactRole_ARTIFACT_ROLE_CONFIG: {artifact("config.json", maxConfig+1)}}},
	} {
		if _, err := format.Read(context.Background(), opener(files), g); err == nil {
			t.Fatalf("%s: read", name)
		}
	}
}

// Architecture falls back from the class list to the model type, the text model, then the diffusers class
func TestArchitecture(t *testing.T) {
	for name, tc := range map[string]struct {
		meta map[string]string
		want string
	}{
		"class list":  {map[string]string{"architectures": "A,B", "model_type": "x"}, "A"},
		"model type":  {map[string]string{"model_type": "mistral"}, "mistral"},
		"text config": {map[string]string{"text_config.model_type": "gemma"}, "gemma"},
		"class name":  {map[string]string{"_class_name": "UNet2DConditionModel"}, "UNet2DConditionModel"},
		"none":        {map[string]string{}, ""},
	} {
		if got := format.Architecture(&v1.RawModel{Metadata: tc.meta}); got != tc.want {
			t.Fatalf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// Tensor names sort into vision, audio, embedding, numbered layers, experts, draft heads and output
func TestTensor(t *testing.T) {
	for name, want := range map[string]struct {
		kind  v1.TensorGroupKind
		layer int32
	}{
		"model.layers.7.self_attn.q_proj.weight":  {v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, 7},
		"model.layers.2.mlp.experts.3.w1.weight":  {v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, 2},
		"model.layers.2.mlp.shared_expert.weight": {v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, 2},
		"model.embed_tokens.weight":               {v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, -1},
		"transformer.wte.weight":                  {v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, -1},
		"lm_head.weight":                          {v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, -1},
		"model.norm.weight":                       {v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, -1},
		"transformer.ln_f.bias":                   {v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, -1},
		"hc_head_0.weight":                        {v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, -1},
		"vision_tower.encoder.layers.1.weight":    {v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, -1},
		"multi_modal_projector.linear.weight":     {v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, -1},
		"audio_tower.conv1.weight":                {v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, -1},
		"model.mtp.1.weight":                      {v1.TensorGroupKind_TENSOR_GROUP_KIND_DRAFT, 1},
		"model.rotary_emb.inv_freq":               {v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, -1},
		"norm":                                    {v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, -1},
	} {
		if kind, layer := format.Tensor(name); kind != want.kind || layer != want.layer {
			t.Fatalf("Tensor(%s) = %v %d, want %v %d", name, kind, layer, want.kind, want.layer)
		}
	}
}

// Packed experts and quantized weights count the weights their bytes hold, plain tensors their elements
func TestElements(t *testing.T) {
	for name, tc := range map[string]struct {
		tensor *v1.TensorInfo
		meta   map[string]string
		want   uint64
	}{
		"plain":           {&v1.TensorInfo{Name: "model.layers.0.mlp.weight", Elements: 10, Bytes: 20}, nil, 10},
		"empty":           {&v1.TensorInfo{Name: "x", Elements: 0}, nil, 0},
		"mxfp4 experts":   {&v1.TensorInfo{Name: "model.layers.0.mlp.experts.0.weight", Elements: 8, Bytes: 8}, map[string]string{"expert_dtype": "mxfp4"}, 16},
		"experts unknown": {&v1.TensorInfo{Name: "model.layers.0.mlp.experts.0.weight", Elements: 8, Bytes: 8}, map[string]string{"expert_dtype": "odd"}, 8},
		"gptq qweight":    {&v1.TensorInfo{Name: "model.layers.0.mlp.qweight", Elements: 4, Bytes: 16}, map[string]string{"quantization_config.bits": "4"}, 32},
		"awq qzeros":      {&v1.TensorInfo{Name: "model.layers.0.mlp.qzeros", Elements: 4, Bytes: 16}, map[string]string{"quantization_config.w_bit": "8"}, 16},
		"narrower stored": {&v1.TensorInfo{Name: "model.layers.0.mlp.qweight", Elements: 16, Bytes: 4}, map[string]string{"quantization_config.bits": "4"}, 16},
	} {
		if got := format.Elements(tc.tensor, &v1.RawModel{Metadata: tc.meta}); got != tc.want {
			t.Fatalf("%s: %d, want %d", name, got, tc.want)
		}
	}
	for dtype, bits := range map[string]uint64{"mxfp4": 4, "nvfp4": 4, "int8": 8, "nf4": 4, "fp8": 8, "bf16": 0, "": 0} {
		if got := packedBits(dtype); got != bits {
			t.Fatalf("packedBits(%s) = %d", dtype, got)
		}
	}
}

// A quantization config names the method and width, packed experts their format, else the checkpoint dtype decides
func TestPrecision(t *testing.T) {
	for name, tc := range map[string]struct {
		meta   map[string]string
		bits   uint32
		labels string
	}{
		"gptq":           {map[string]string{"quantization_config.quant_method": "gptq", "quantization_config.bits": "4", "torch_dtype": "float16"}, 4, "gptq"},
		"awq":            {map[string]string{"quantization_config.quant_method": "awq", "quantization_config.w_bit": "4"}, 4, "awq"},
		"method only":    {map[string]string{"quantization_config.quant_method": "fp8"}, 0, "fp8"},
		"mxfp4 experts":  {map[string]string{"expert_dtype": "MXFP4", "torch_dtype": "bfloat16"}, 0, "mxfp4 experts"},
		"bfloat16":       {map[string]string{"torch_dtype": "bfloat16"}, 16, "bfloat16"},
		"float16 dtype":  {map[string]string{"dtype": "torch.float16"}, 16, "float16"},
		"float32":        {map[string]string{"torch_dtype": "float32"}, 32, "float32"},
		"unknown":        {map[string]string{"torch_dtype": "int8"}, 0, ""},
		"nothing at all": {map[string]string{}, 0, ""},
	} {
		w := format.Precision(&v1.RawModel{Metadata: tc.meta}, "")
		if w.Bits != tc.bits || strings.Join(w.Labels, ",") != tc.labels {
			t.Fatalf("%s: %+v", name, w)
		}
	}
}
