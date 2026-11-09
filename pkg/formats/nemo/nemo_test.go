package nemo

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

// Writes the pickle opcodes the fixtures need
type pk struct{ bytes.Buffer }

func (p *pk) op(b ...byte) *pk       { p.Write(b); return p }
func (p *pk) str(s string) *pk       { return p.op('U', byte(len(s))).op([]byte(s)...) }
func (p *pk) global(m, n string) *pk { p.op('c'); p.WriteString(m + "\n" + n + "\n"); return p }
func (p *pk) int(n int32) *pk        { p.op('J'); binary.Write(p, binary.LittleEndian, n); return p }
func (p *pk) mark() *pk              { return p.op('(') }
func (p *pk) tuple() *pk             { return p.op('t') }
func (p *pk) emptyTuple() *pk        { return p.op(')') }
func (p *pk) reduce() *pk            { return p.op('R') }
func (p *pk) newobj() *pk            { return p.op(0x81) }
func (p *pk) build() *pk             { return p.op('b') }
func (p *pk) emptyDict() *pk         { return p.op('}') }
func (p *pk) setItems() *pk          { return p.op('u') }
func (p *pk) stop() []byte           { return p.op('.').Bytes() }

func (p *pk) size(dims ...int32) *pk {
	p.global("torch", "Size").mark()
	for _, d := range dims {
		p.int(d)
	}
	return p.tuple().op(0x85).reduce()
}

// A _rebuild_tensor_v2 call over a storage class with the shape
func (p *pk) tensor(class string, dims ...int32) *pk {
	p.global("torch._utils", "_rebuild_tensor_v2").mark()
	p.mark().str("storage").global("torch", class).str("0").str("cpu").int(99).tuple().op('Q')
	p.int(0).size(dims...).mark().int(1).tuple().op(0x89).global("collections", "OrderedDict").emptyTuple().reduce().tuple()
	return p.reduce()
}

// A torch.save zip of the named tensors
func torchZip(t *testing.T, tensors map[string][]int32, order []string) []byte {
	t.Helper()
	p := new(pk).op(0x80, 2).emptyDict().mark()
	for _, name := range order {
		p.str(name).tensor("BFloat16Storage", tensors[name]...)
	}
	pickle := p.setItems().stop()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("archive/data.pkl")
	if err != nil {
		t.Fatal(err)
	}
	w.Write(pickle)
	zw.Close()
	return buf.Bytes()
}

// A torch distributed checkpoint .metadata pickle naming the tensors with their dtype and shape
func distMetadata(names []string, shapes map[string][]int32, dtype string) []byte {
	p := new(pk).op(0x80, 2).global("torch.distributed.checkpoint.metadata", "Metadata").emptyTuple().newobj()
	p.emptyDict().mark().str("state_dict_metadata").emptyDict().mark()
	for _, name := range names {
		p.str(name).global("torch.distributed.checkpoint.metadata", "TensorStorageMetadata").emptyTuple().newobj()
		p.emptyDict().mark()
		p.str("properties").global("torch.distributed.checkpoint.metadata", "TensorProperties").emptyTuple().newobj().emptyDict().mark().str("dtype").global("torch", dtype).setItems().build()
		p.str("size").size(shapes[name]...)
		p.setItems().build()
	}
	p.str("scalar").int(1)
	return p.setItems().setItems().build().stop()
}

type member struct {
	name string
	body []byte
	dir  bool
}

// A tar of the members, long names through pax headers
func tarOf(t *testing.T, members ...member) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, m := range members {
		hdr := &tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.body)), Typeflag: tar.TypeReg}
		if m.dir {
			hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeDir, 0o755, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if !m.dir {
			tw.Write(m.body)
		}
	}
	tw.Close()
	return buf.Bytes()
}

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

func artifact(p string, size int) *v1.Artifact {
	return &v1.Artifact{Path: p, SizeBytes: uint64(size)}
}

// The two layouts under test
var packed, directory = Packed{}, Directory{}

const llamaConfig = "target: nemo.collections.llm.gpt.model.llama.LlamaModel\nnum_layers: 2\nhidden_size: 4\nnum_attention_heads: 2\nprecision: bf16-mixed\ntokenizer:\n  type: sentencepiece\n"

// Tensor names sort by what they hold, layers by number and experts under theirs
func TestTensor(t *testing.T) {
	for name, want := range map[string]struct {
		kind  v1.TensorGroupKind
		layer int32
	}{
		"decoder.layers.3.mlp.weight":          {v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, 3},
		"decoder.layers.1.mlp.experts.0.w":     {v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, 1},
		"embedding.word_embeddings.weight":     {v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, -1},
		"embedding.position_embeddings.weight": {v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, -1},
		"output_layer.weight":                  {v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, -1},
		"decoder.final_layernorm.weight":       {v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, -1},
		"vision_model.encoder.weight":          {v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION, -1},
		"audio_encoder.conv.weight":            {v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO, -1},
		"rotary_pos_emb.inv_freq":              {v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, -1},
		"word_embeddings":                      {v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, -1},
	} {
		if kind, layer := packed.Tensor(name); kind != want.kind || layer != want.layer {
			t.Fatalf("Tensor(%s) = %v %d, want %v %d", name, kind, layer, want.kind, want.layer)
		}
		if kind, layer := directory.Tensor(name); kind != want.kind || layer != want.layer {
			t.Fatalf("Directory Tensor(%s) = %v %d", name, kind, layer)
		}
	}
}

// A .nemo archive is a weights artifact grouped by its stem, nothing else is claimed
func TestPackedClassify(t *testing.T) {
	for p, want := range map[string]struct {
		ok    bool
		group string
	}{"a/b/Llama-7B.nemo": {true, "Llama-7B"}, "model.NEMO": {true, "model"}, "model.nemo.json": {false, ""}, "weights/.metadata": {false, ""}} {
		claim, ok := packed.Classify(p)
		if ok != want.ok || claim.Group != want.group || ok && claim.Role != v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS {
			t.Fatalf("Classify(%s) = %+v %v", p, claim, ok)
		}
	}
	if packed.ID() != "nemo" || packed.Priority() != 6 || packed.Requires() != nil || packed.DraftFrom(formats.Params{}) != -1 || packed.Description() == "" || packed.Blurb() == "" {
		t.Fatal("packed descriptor")
	}
}

// A NeMo 2 tree claims its distributed shards, metadata, configs and tokenizer under one root
func TestDirectoryClassify(t *testing.T) {
	for p, want := range map[string]struct {
		ok    bool
		role  v1.ArtifactRole
		root  string
		group string
	}{
		"ckpt/weights/__0_0.distcp":                  {true, v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, "ckpt", "ckpt"},
		"weights/__1_0.distcp":                       {true, v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, "", "default"},
		"ckpt/weights/.metadata":                     {true, v1.ArtifactRole_ARTIFACT_ROLE_CONFIG, "ckpt", ""},
		"ckpt/weights/metadata.json":                 {true, v1.ArtifactRole_ARTIFACT_ROLE_CONFIG, "ckpt", ""},
		"ckpt/weights/common.pt":                     {true, v1.ArtifactRole_ARTIFACT_ROLE_CONFIG, "ckpt", ""},
		"ckpt/context/model.yaml":                    {true, v1.ArtifactRole_ARTIFACT_ROLE_CONFIG, "ckpt", ""},
		"ckpt/context/io.json":                       {true, v1.ArtifactRole_ARTIFACT_ROLE_CONFIG, "ckpt", ""},
		"ckpt/context/nemo_tokenizer/tokenizer.json": {true, v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER, "ckpt", ""},
		"ckpt/weights/notes.txt":                     {false, 0, "", ""},
		"ckpt/context/other.yaml":                    {false, 0, "", ""},
		"ckpt/readme.md":                             {false, 0, "", ""},
	} {
		claim, ok := directory.Classify(p)
		if ok != want.ok || claim.Role != want.role || claim.Root != want.root || claim.Group != want.group || ok && !claim.Tree {
			t.Fatalf("Classify(%s) = %+v %v, want %+v", p, claim, ok, want)
		}
	}
	if directory.ID() != "nemo2" || len(directory.Requires()) != 1 || directory.DraftFrom(formats.Params{}) != -1 || directory.Description() == "" || directory.Blurb() == "" {
		t.Fatal("directory descriptor")
	}
}

// A packed archive with torch weights expands stacked layers, names the architecture and reads the precision
func TestPackedReadTorchWeights(t *testing.T) {
	ckpt := torchZip(t, map[string][]int32{"decoder.layers.mlp.weight": {2, 4}, "embedding.word_embeddings.weight": {3, 4}, "decoder.layers.0.norm.weight": {4}, "decoder.layers.odd.weight": {3, 2}}, []string{"decoder.layers.mlp.weight", "embedding.word_embeddings.weight", "decoder.layers.0.norm.weight", "decoder.layers.odd.weight"})
	long := "model_weights/" + strings.Repeat("deep/", 30) + "notes.txt"
	archive := tarOf(t, member{name: "./", dir: true}, member{name: "./model_config.yaml", body: []byte(llamaConfig)}, member{name: long, body: []byte("x")}, member{name: "./model_weights.ckpt", body: ckpt})
	group := &formats.Group{FormatID: "nemo", Name: "llama", Weights: []*v1.Artifact{artifact("llama.nemo", len(archive))}}
	raw, err := packed.Read(context.Background(), opener(map[string][]byte{"llama.nemo": archive}), group)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]uint64{}
	for _, tt := range raw.GetTensors() {
		names[tt.GetName()] = tt.GetElements()
		if tt.GetDtype() != "BF16" || tt.GetBytes() != tt.GetElements()*2 {
			t.Fatalf("tensor %v", tt)
		}
	}
	want := map[string]uint64{"decoder.layers.0.mlp.weight": 4, "decoder.layers.1.mlp.weight": 4, "embedding.word_embeddings.weight": 12, "decoder.layers.0.norm.weight": 4, "decoder.layers.odd.weight": 6}
	if len(names) != len(want) {
		t.Fatalf("tensors %v", names)
	}
	for k, v := range want {
		if names[k] != v {
			t.Fatalf("tensor %s has %d elements, want %d", k, names[k], v)
		}
	}
	if packed.Architecture(raw) != "llama" || raw.GetMetadata()[layersKey] != "2" {
		t.Fatalf("architecture %q metadata %v", packed.Architecture(raw), raw.GetMetadata())
	}
	params := packed.Params(raw)
	if params.Layers != 2 || params.Embedding != 4 || params.Heads != 2 || params.EmbeddingElements != 12 {
		t.Fatalf("params %+v", params)
	}
	if w := packed.Precision(raw, ""); w.Bits != 16 || len(w.Labels) != 1 || w.Labels[0] != "bf16" {
		t.Fatalf("precision %+v", w)
	}
	meta := packed.Metadata(raw)
	if meta["precision"] != "bf16-mixed" || meta["target"] == "" || meta[layersKey] != "2" || meta["tokenizer.type"] != "sentencepiece" || len(meta) != 4 {
		t.Fatalf("metadata %v", meta)
	}
	if packed.Elements(raw.GetTensors()[0], raw) != raw.GetTensors()[0].GetElements() {
		t.Fatal("elements pass through")
	}
}

// Zarr headers and a distributed checkpoint index each name the tensors of a packed archive
func TestPackedReadZarrAndDistributed(t *testing.T) {
	zarr := tarOf(t, member{name: "model_config.yaml", body: []byte("num_layers: 1\nprecision: 16\n")},
		member{name: "model_weights/decoder.layers.mlp.weight/.zarray", body: []byte(`{"shape": [1, 4], "dtype": "<f2"}`)},
		member{name: "model_weights/decoder.layers.mlp.weight/0.0", body: []byte("data")},
		member{name: "model_weights/output_layer.weight/.zarray", body: []byte(`{"shape": [3], "dtype": "bfloat16"}`)})
	dist := tarOf(t, member{name: "model_config.yaml", body: []byte("_target_: nemo.collections.nlp.models.language_modeling.megatron_gpt_model.MegatronGPTModel\nnum_layers: 2\nprecision: bf16\n")},
		member{name: "model_weights/.metadata", body: distMetadata([]string{"decoder.layers.mlp.weight", "output_layer.weight"}, map[string][]int32{"decoder.layers.mlp.weight": {2, 3}, "output_layer.weight": {5}}, "bfloat16")},
		member{name: "model_weights/metadata.json", body: []byte(`{"sharded_backend": "torch_dist"}`)})
	for _, tc := range []struct {
		name    string
		archive []byte
		arch    string
		tensors map[string]string
		bits    uint32
		backend string
	}{
		{"zarr", zarr, "", map[string]string{"decoder.layers.0.mlp.weight": "F16", "output_layer.weight": "BF16"}, 16, ""},
		{"distributed", dist, "gpt", map[string]string{"decoder.layers.0.mlp.weight": "BF16", "decoder.layers.1.mlp.weight": "BF16", "output_layer.weight": "BF16"}, 16, "torch_dist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := &formats.Group{FormatID: "nemo", Name: "m", Weights: []*v1.Artifact{artifact("m.nemo", len(tc.archive))}}
			raw, err := packed.Read(context.Background(), opener(map[string][]byte{"m.nemo": tc.archive}), group)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, tt := range raw.GetTensors() {
				got[tt.GetName()] = tt.GetDtype()
			}
			if len(got) != len(tc.tensors) {
				t.Fatalf("tensors %v, want %v", got, tc.tensors)
			}
			for k, v := range tc.tensors {
				if got[k] != v {
					t.Fatalf("tensor %s dtype %q, want %q", k, got[k], v)
				}
			}
			if packed.Architecture(raw) != tc.arch || packed.Precision(raw, "").Bits != tc.bits || raw.GetMetadata()[backendKey] != tc.backend {
				t.Fatalf("architecture %q precision %+v metadata %v", packed.Architecture(raw), packed.Precision(raw, ""), raw.GetMetadata())
			}
		})
	}
}

// A NeMo 2 directory reads its config and distributed index, shard sizes standing in without the index
func TestDirectoryRead(t *testing.T) {
	config := []byte("_target_: nemo.collections.llm.gpt.model.llama.LlamaModel\nconfig:\n  _target_: nemo.collections.llm.gpt.model.llama.Llama3Config8B\n  num_layers: 2\n  hidden_size: 8\n  params_dtype: torch.bfloat16\n  seq_length: 128\n")
	meta := distMetadata([]string{"decoder.layers.mlp.weight"}, map[string][]int32{"decoder.layers.mlp.weight": {2, 8}}, "bfloat16")
	files := map[string][]byte{"ckpt/context/model.yaml": config, "ckpt/weights/.metadata": meta, "ckpt/weights/metadata.json": []byte(`{"sharded_backend": "torch_dist"}`), "ckpt/weights/__0_0.distcp": bytes.Repeat([]byte("w"), 64)}
	withIndex := &formats.Group{FormatID: "nemo2", Name: "ckpt", Root: "ckpt",
		Weights: []*v1.Artifact{artifact("ckpt/weights/__0_0.distcp", 64)},
		Files:   map[v1.ArtifactRole][]*v1.Artifact{v1.ArtifactRole_ARTIFACT_ROLE_CONFIG: {artifact("ckpt/context/model.yaml", len(config)), artifact("ckpt/weights/.metadata", len(meta)), artifact("ckpt/weights/metadata.json", 32)}}}
	raw, err := directory.Read(context.Background(), opener(files), withIndex)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.GetTensors()) != 2 || raw.GetTensors()[0].GetName() != "decoder.layers.0.mlp.weight" || raw.GetTensors()[1].GetElements() != 8 {
		t.Fatalf("tensors %v", raw.GetTensors())
	}
	if directory.Architecture(raw) != "llama" || raw.GetMetadata()[backendKey] != "torch_dist" {
		t.Fatalf("architecture %q metadata %v", directory.Architecture(raw), raw.GetMetadata())
	}
	if p := directory.Params(raw); p.Layers != 2 || p.Embedding != 8 || p.ContextTrain != 128 {
		t.Fatalf("params %+v", p)
	}
	if w := directory.Precision(raw, ""); w.Bits != 16 || w.Labels[0] != "bfloat16" {
		t.Fatalf("precision %+v", w)
	}
	if m := directory.Metadata(raw); m["config.params_dtype"] != "torch.bfloat16" || m["config.seq_length"] != "128" || m[layersKey] != "2" || m["config._target_"] == "" || m["_target_"] == "" {
		t.Fatalf("metadata %v", m)
	}
	bare := &formats.Group{FormatID: "nemo2", Name: "ckpt", Weights: []*v1.Artifact{artifact("ckpt/weights/__0_0.distcp", 64)},
		Files: map[v1.ArtifactRole][]*v1.Artifact{v1.ArtifactRole_ARTIFACT_ROLE_CONFIG: {artifact("ckpt/context/model.yaml", len(config))}}}
	raw, err = directory.Read(context.Background(), opener(files), bare)
	if err != nil || len(raw.GetTensors()) != 1 || raw.GetTensors()[0].GetBytes() != 64 {
		t.Fatalf("shard sizes stand in: %v %v", raw.GetTensors(), err)
	}
	for _, w := range []formats.Words{directory.Precision(&v1.RawModel{Metadata: map[string]string{"config.bf16": "true"}}, ""), directory.Precision(&v1.RawModel{Metadata: map[string]string{"config.fp16": "True"}}, "")} {
		if w.Bits != 16 {
			t.Fatalf("mixed precision flag %+v", w)
		}
	}
	for p, bits := range map[string]uint32{"16": 16, "fp16-mixed": 16, "32-true": 32, "fp32": 32, "odd": 0} {
		if w := packed.Precision(&v1.RawModel{Metadata: map[string]string{"precision": p}}, ""); w.Bits != bits {
			t.Fatalf("precision %s = %+v", p, w)
		}
	}
}

// Archives without weights, broken ones, and unreadable configs are refused with the member named
func TestReadErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		archive []byte
		msg     string
	}{
		{"no weights", tarOf(t, member{name: "model_config.yaml", body: []byte("a: 1\n")}), "no model weights"},
		{"not a tar", []byte(strings.Repeat("x", 1024)), "not a tar"},
		{"bad config", tarOf(t, member{name: "model_config.yaml", body: []byte(":\n  - [")}), "model_config.yaml"},
		{"bad zarr", tarOf(t, member{name: "w/a/.zarray", body: []byte("{")}), ".zarray"},
		{"bad index", tarOf(t, member{name: "w/.metadata", body: []byte{0x80, 2, '.'}}), ".metadata"},
		{"index without tensors", tarOf(t, member{name: "w/.metadata", body: distMetadata(nil, nil, "float32")}), "lists no tensors"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := &formats.Group{FormatID: "nemo", Name: "m", Weights: []*v1.Artifact{artifact("m.nemo", len(tc.archive))}}
			_, err := packed.Read(context.Background(), opener(map[string][]byte{"m.nemo": tc.archive}), group)
			if err == nil || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("error %v lacks %q", err, tc.msg)
			}
		})
	}
	if _, err := packed.Read(context.Background(), opener(nil), &formats.Group{Weights: []*v1.Artifact{artifact("missing.nemo", 1)}}); err == nil {
		t.Fatal("a missing archive must fail")
	}
}

// Stacked names split only at an unnumbered layers segment
func TestStackedName(t *testing.T) {
	for name, want := range map[string]struct {
		head, tail string
		ok         bool
	}{
		"decoder.layers.mlp.weight":   {"decoder.layers.", "mlp.weight", true},
		"encoder.layers.attn.q":       {"encoder.layers.", "attn.q", true},
		"decoder.layers.0.mlp.weight": {"", "", false},
		"decoder.layers":              {"", "", false},
		"embedding.weight":            {"", "", false},
	} {
		head, tail, ok := stackedName(name)
		if head != want.head || tail != want.tail || ok != want.ok {
			t.Fatalf("stackedName(%s) = %q %q %v", name, head, tail, ok)
		}
	}
	for target, want := range map[string]string{"nemo.collections.llm.gpt.model.llama.LlamaModel": "llama", "a.MegatronGPTModel": "gpt", "Mixtral": "mixtral", "x.Llama3Config8B": "llama3config8b"} {
		if got := archName(target); got != want {
			t.Fatalf("archName(%s) = %q", target, got)
		}
	}
}
