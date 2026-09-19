package gguf

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"testing"

	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

func TestChunkReader(t *testing.T) {
	data := make([]byte, 3*firstChunk+123)
	for i := range data {
		data[i] = byte(i % 251)
	}
	got, err := io.ReadAll(newChunkReader(bytes.NewReader(data), int64(len(data))))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
}

// Writes a GGUF header fixture without tensor data or padding.
func header(tensors ...string) []byte {
	var b bytes.Buffer
	b.WriteString(magic)
	binary.Write(&b, binary.LittleEndian, uint32(3))
	binary.Write(&b, binary.LittleEndian, uint64(len(tensors)))
	binary.Write(&b, binary.LittleEndian, uint64(1))
	str := func(s string) {
		binary.Write(&b, binary.LittleEndian, uint64(len(s)))
		b.WriteString(s)
	}
	str("general.architecture")
	binary.Write(&b, binary.LittleEndian, typeString)
	str("llama")
	for _, name := range tensors {
		str(name)
		binary.Write(&b, binary.LittleEndian, uint32(1))
		binary.Write(&b, binary.LittleEndian, uint64(4))
		binary.Write(&b, binary.LittleEndian, uint32(0))
		binary.Write(&b, binary.LittleEndian, uint64(0))
	}
	return b.Bytes()
}

func TestMetadataOnlyShardParses(t *testing.T) {
	only := header()
	if len(only)%defaultAlignment == 0 {
		only = append(only, 0)
		binary.LittleEndian.PutUint64(only[8:], 0)
	}
	meta, tensors, err := parse(bytes.NewReader(only), int64(len(only)))
	if err != nil || len(tensors) != 0 || meta["general.architecture"] != "llama" {
		t.Fatalf("metadata only shard: tensors=%d meta=%v err=%v", len(tensors), meta, err)
	}
	// A shard that declares a tensor still has to reach the aligned data start
	if _, _, err := parse(bytes.NewReader(header("blk.0.attn_q.weight")), int64(len(header("blk.0.attn_q.weight")))); err == nil {
		t.Fatal("a tensor without data should fail")
	}
	// With the data present the tensor spans to the end of the file
	full := header("blk.0.attn_q.weight")
	for len(full)%defaultAlignment != 0 {
		full = append(full, 0)
	}
	full = append(full, make([]byte, 16)...)
	_, tensors, err = parse(bytes.NewReader(full), int64(len(full)))
	if err != nil || len(tensors) != 1 || tensors[0].GetBytes() != 16 || tensors[0].GetDtype() != "F32" {
		t.Fatalf("tensor shard: %v %v", tensors, err)
	}
}

type memBlob struct{ *bytes.Reader }

func (memBlob) Close() error { return nil }

func TestProjectorTensorsJoinTheWeights(t *testing.T) {
	file := func(names ...string) []byte {
		b := header(names...)
		for len(b)%defaultAlignment != 0 {
			b = append(b, 0)
		}
		return append(b, make([]byte, 16*len(names))...)
	}
	files := map[string][]byte{"model.gguf": file("blk.0.attn_q.weight"), "mmproj.gguf": file("v.blk.0.attn_q.weight", "mm.0.weight"), "mmproj-bf16.gguf": file("v.blk.0.attn_k.weight")}
	open := func(ctx context.Context, a *v1.Artifact) (sources.Blob, error) {
		return memBlob{bytes.NewReader(files[a.GetPath()])}, nil
	}
	g := &formats.Group{FormatID: "gguf", Name: "default", Weights: []*v1.Artifact{{Path: "model.gguf"}}, Files: map[v1.ArtifactRole][]*v1.Artifact{
		v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR: {{Path: "mmproj-bf16.gguf"}, {Path: "mmproj.gguf"}},
	}}
	raw, err := Format{}.Read(context.Background(), open, g)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, ti := range raw.GetTensors() {
		names = append(names, ti.GetName())
	}
	// The first projector by path is the one a launch passes, so it alone is counted
	if len(names) != 2 || names[0] != "blk.0.attn_q.weight" || names[1] != "v.blk.0.attn_k.weight" {
		t.Fatalf("tensors %v", names)
	}
	if raw.GetMetadata()["general.architecture"] != "llama" {
		t.Fatalf("metadata %v", raw.GetMetadata())
	}
}
