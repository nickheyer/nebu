package safetensors

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

type memBlob struct {
	*bytes.Reader
	size int64
}

func (m memBlob) Size() int64  { return m.size }
func (m memBlob) Close() error { return nil }

func opener(files map[string][]byte, sizes map[string]int64) formats.Opener {
	return func(ctx context.Context, a *v1.Artifact) (sources.Blob, error) {
		data := files[a.GetPath()]
		size := int64(len(data))
		if s, ok := sizes[a.GetPath()]; ok {
			size = s
		}
		return memBlob{Reader: bytes.NewReader(data), size: size}, nil
	}
}

func shard(header string) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint64(len(header)))
	buf.WriteString(header)
	buf.Write(make([]byte, 64))
	return buf.Bytes()
}

func TestSynthetic(t *testing.T) {
	files := map[string][]byte{
		"model.safetensors": shard(`{"__metadata__":{"format":"pt"},"model.embed_tokens.weight":{"dtype":"BF16","shape":[4,8],"data_offsets":[0,64]},"model.layers.0.mlp.up_proj.weight":{"dtype":"BF16","shape":[2,2],"data_offsets":[64,72]}}`),
		"config.json":       []byte(`{"architectures":["XForCausalLM"],"num_hidden_layers":1,"hidden_size":8,"text_config":{"num_attention_heads":2}}`),
	}
	r, _ := New(nil)
	g := &formats.Group{FormatID: "safetensors", Name: "default",
		Weights: []*v1.Artifact{{Path: "model.safetensors"}},
		Files:   map[v1.ArtifactRole][]*v1.Artifact{v1.ArtifactRole_ARTIFACT_ROLE_CONFIG: {{Path: "config.json", SizeBytes: 10}}},
	}
	raw, err := r.Read(context.Background(), opener(files, nil), g)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.GetTensors()) != 2 {
		t.Fatalf("tensors %d", len(raw.GetTensors()))
	}
	md := raw.GetMetadata()
	if md["architectures"] != "XForCausalLM" || md["num_hidden_layers"] != "1" || md["text_config.num_attention_heads"] != "2" || md["__metadata__.format"] != "pt" {
		t.Fatalf("metadata %v", md)
	}
	for _, ti := range raw.GetTensors() {
		if ti.GetName() == "model.embed_tokens.weight" && (ti.GetBytes() != 64 || ti.GetElements() != 32 || ti.GetDtype() != "BF16") {
			t.Fatalf("tensor %+v", ti)
		}
	}
}

func TestRealHeader(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "test", "fixtures", "safetensors")
	header, err := os.ReadFile(filepath.Join(dir, "qwen3-0.6b.header.safetensors"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(dir, "qwen3-0.6b.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"model.safetensors": header, "config.json": config}
	r, _ := New(nil)
	g := &formats.Group{FormatID: "safetensors", Name: "default",
		Weights: []*v1.Artifact{{Path: "model.safetensors"}},
		Files:   map[v1.ArtifactRole][]*v1.Artifact{v1.ArtifactRole_ARTIFACT_ROLE_CONFIG: {{Path: "config.json", SizeBytes: uint64(len(config))}}},
	}
	raw, err := r.Read(context.Background(), opener(files, map[string]int64{"model.safetensors": 1503300328}), g)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.GetTensors()) < 100 || raw.GetMetadata()["hidden_size"] == "" || raw.GetMetadata()["num_hidden_layers"] != "28" {
		t.Fatalf("tensors %d metadata %v", len(raw.GetTensors()), raw.GetMetadata()["num_hidden_layers"])
	}
}
