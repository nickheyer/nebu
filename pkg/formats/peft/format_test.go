package peft

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	"github.com/nickheyer/nebu/pkg/formats/safetensors"
	"github.com/nickheyer/nebu/pkg/precision"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

func header(names ...string) []byte {
	header := map[string]any{}
	var off uint64
	for _, name := range names {
		header[name] = map[string]any{"dtype": "F32", "shape": []uint64{128, 8}, "data_offsets": []uint64{off, off + 4096}}
		off += 4096
	}
	js, _ := json.Marshal(header)
	out := make([]byte, 8)
	binary.LittleEndian.PutUint64(out, uint64(len(js)))
	return append(append(out, js...), make([]byte, off)...)
}

type memBlob struct{ *strings.Reader }

func (memBlob) Close() error { return nil }

func TestAdapterDirectory(t *testing.T) {
	files := map[string][]byte{
		"adapter_model.safetensors": header("blocks.0.attn.qkv_proj.lora_a", "blocks.0.attn.qkv_proj.lora_b", "token_refiner.blocks.0.mlp.fc1.lora_a", "token_refiner.blocks.0.mlp.fc1.lora_b"),
		"adapter_config.json":       []byte(`{"rank":128,"alpha":128.0,"weight_source":"generator_ema"}`),
		"config.json":               []byte(`{"rank":128,"alpha":128.0}`),
		"README.md":                 []byte(`# lora`),
	}
	m := &v1.Model{Repo: "TaoLiveAIGC/TaoMate-H3"}
	for p, data := range files {
		m.Artifacts = append(m.Artifacts, &v1.Artifact{Path: p, SizeBytes: uint64(len(data))})
	}
	// Every format that could claim the files, in the order the registry ranks them
	r, err := formats.New([]formats.Format{Format{}, safetensors.Format{}, diffusion.Format{}})
	if err != nil {
		t.Fatal(err)
	}
	r.Classify(m)
	groups := r.Groups(m)
	if len(groups) != 1 || groups[0].FormatID != "peft" || groups[0].Name != "default" || len(groups[0].Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG]) != 1 {
		t.Fatalf("groups %+v", groups)
	}
	open := func(_ context.Context, a *v1.Artifact) (sources.Blob, error) {
		return memBlob{strings.NewReader(string(files[a.GetPath()]))}, nil
	}
	raw, err := Format{}.Read(context.Background(), open, groups[0])
	if err != nil {
		t.Fatal(err)
	}
	families, err := archs.New(archs.All())
	if err != nil {
		t.Fatal(err)
	}
	b := &descriptor.Builder{Formats: r, Archs: families, Scale: precision.Bits{}}
	d, err := b.Build(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.GetKind() != v1.ModelKind_MODEL_KIND_COMPONENT || diffusion.PartOf(d) != "lora" || d.GetArchitecture() != "lora" {
		t.Fatalf("kind %v part %q architecture %q", d.GetKind(), diffusion.PartOf(d), d.GetArchitecture())
	}
	if d.GetMetadata()["adapter.rank"] != "128" || d.GetMetadata()["adapter.alpha"] != "128.0" || d.GetPrecision().GetBits() != 32 {
		t.Fatalf("metadata %v precision %+v", d.GetMetadata(), d.GetPrecision())
	}
	// A PEFT adapter on a language model is the same form with its own names
	for _, p := range []string{"adapter_model.safetensors", "sft/adapter_model-00001-of-00002.safetensors", "adapter_model.bin"} {
		if c, ok := (Format{}).Classify(p); !ok || c.Role != v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS {
			t.Errorf("Classify(%s) = %+v %v", p, c, ok)
		}
	}
	if _, ok := (Format{}).Classify("model.safetensors"); ok {
		t.Fatal("a checkpoint is not an adapter")
	}
	var _ formats.Format = Format{}
}
