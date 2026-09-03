package gguf

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/gguf/gguftest"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

type memBlob struct {
	*bytes.Reader
	size int64
}

func (m memBlob) Size() int64  { return m.size }
func (m memBlob) Close() error { return nil }

func opener(data []byte, size int64) formats.Opener {
	return func(ctx context.Context, a *v1.Artifact) (sources.Blob, error) {
		return memBlob{Reader: bytes.NewReader(data), size: size}, nil
	}
}

func spec() *v1.FormatSpec {
	return &v1.FormatSpec{Id: "gguf", Dtypes: map[string]string{"0": "F32", "12": "Q4_K"}}
}

func TestSynthetic(t *testing.T) {
	var buf bytes.Buffer
	kv := map[string]any{
		"general.architecture":   "llama",
		"llama.block_count":      uint32(2),
		"general.alignment":      uint32(64),
		"tokenizer.ggml.tokens":  []string{"a", "b", "c"},
		"llama.rope.freq_base":   float32(10000),
		"general.flag":           true,
		"llama.attention.layers": []uint32{4, 8},
	}
	tensors := []gguftest.Tensor{
		{Name: "token_embd.weight", Dims: []uint64{4, 8}, Type: 0, Bytes: 128},
		{Name: "blk.0.attn_q.weight", Dims: []uint64{4, 4}, Type: 12, Bytes: 100},
		{Name: "blk.1.attn_q.weight", Dims: []uint64{4, 4}, Type: 12, Bytes: 100},
		{Name: "output.weight", Dims: []uint64{8, 4}, Type: 0, Bytes: 128},
	}
	if err := gguftest.Write(&buf, kv, tensors, 64); err != nil {
		t.Fatal(err)
	}
	r, _ := New(spec())
	raw, err := r.Read(context.Background(), opener(buf.Bytes(), int64(buf.Len())), &formats.Group{FormatID: "gguf", Name: "x", Weights: []*v1.Artifact{{Path: "m.gguf"}}})
	if err != nil {
		t.Fatal(err)
	}
	md := raw.GetMetadata()
	if md["general.architecture"] != "llama" || md["llama.block_count"] != "2" || md["tokenizer.ggml.tokens.length"] != "3" || md["tokenizer.ggml.tokens"] != "a,b,c" || md["general.flag"] != "true" || md["llama.attention.layers"] != "4,8" {
		t.Fatalf("metadata %v", md)
	}
	if !strings.HasPrefix(md["llama.rope.freq_base"], "10000") {
		t.Fatalf("float %q", md["llama.rope.freq_base"])
	}
	if len(raw.GetTensors()) != 4 {
		t.Fatalf("tensors %d", len(raw.GetTensors()))
	}
	for _, ti := range raw.GetTensors() {
		switch ti.GetName() {
		case "token_embd.weight":
			if ti.GetBytes() != 128 || ti.GetElements() != 32 || ti.GetDtype() != "F32" {
				t.Errorf("embd %+v", ti)
			}
		case "blk.0.attn_q.weight":
			if ti.GetBytes() != 128 || ti.GetDtype() != "Q4_K" {
				t.Errorf("padded layer bytes %+v", ti)
			}
		case "output.weight":
			if ti.GetBytes() != 128 {
				t.Errorf("last tensor from file size %+v", ti)
			}
		}
	}
}

func TestRejects(t *testing.T) {
	r, _ := New(spec())
	g := &formats.Group{FormatID: "gguf", Weights: []*v1.Artifact{{Path: "m.gguf"}}}
	if _, err := r.Read(context.Background(), opener([]byte("NOPE0000"), 8), g); err == nil {
		t.Fatal("bad magic should fail")
	}
	old := append([]byte("GGUF"), 1, 0, 0, 0)
	if _, err := r.Read(context.Background(), opener(old, 8), g); err == nil {
		t.Fatal("version 1 should fail")
	}
}

func TestRealHeader(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "test", "fixtures", "gguf")
	data, err := os.ReadFile(filepath.Join(dir, "stories260K.header.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	sizeText, err := os.ReadFile(filepath.Join(dir, "stories260K.size"))
	if err != nil {
		t.Fatal(err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeText)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := New(spec())
	raw, err := r.Read(context.Background(), opener(data, size), &formats.Group{FormatID: "gguf", Weights: []*v1.Artifact{{Path: "s.gguf"}}})
	if err != nil {
		t.Fatal(err)
	}
	if raw.GetMetadata()["general.architecture"] == "" || len(raw.GetTensors()) != 48 {
		t.Fatalf("arch %q tensors %d", raw.GetMetadata()["general.architecture"], len(raw.GetTensors()))
	}
	var total uint64
	for _, ti := range raw.GetTensors() {
		total += ti.GetBytes()
	}
	if total == 0 || total > uint64(size) {
		t.Fatalf("total %d size %d", total, size)
	}
}
