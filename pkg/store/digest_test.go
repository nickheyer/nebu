package store

import (
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

func descriptorFixture() *v1.Descriptor {
	return &v1.Descriptor{
		FormatId:       "gguf",
		Group:          "Q4_K_M",
		Architecture:   "llama",
		Kind:           v1.ModelKind_MODEL_KIND_LANGUAGE,
		ParameterCount: 7_000_000_000,
		BitsPerWeight:  4.5,
		Params:         map[string]float64{"n_layer": 32, "n_embd": 4096, "n_ctx_train": 8192},
		Metadata:       map[string]string{"general.name": "Test", "general.architecture": "llama"},
		Groups:         []*v1.TensorGroup{{Id: "blk", Bytes: 10, Elements: 5}, {Id: "output", Bytes: 2, Elements: 1}},
	}
}

// Equal descriptors digest the same however their maps were filled, and any change shows
func TestDescriptorDigestIsCanonical(t *testing.T) {
	a := descriptorFixture()
	b := &v1.Descriptor{Params: map[string]float64{}}
	proto.Merge(b, a)
	b.Params = map[string]float64{"n_ctx_train": 8192, "n_embd": 4096, "n_layer": 32}
	if !proto.Equal(a, b) {
		t.Fatal("fixtures are equal")
	}
	if DescriptorDigest(a) != DescriptorDigest(b) || DescriptorDigest(a) == "" {
		t.Fatalf("equal descriptors digest apart: %s %s", DescriptorDigest(a), DescriptorDigest(b))
	}
	b.Params["n_layer"] = 33
	if DescriptorDigest(a) == DescriptorDigest(b) {
		t.Fatal("a changed parameter changes the digest")
	}
	c := descriptorFixture()
	c.Groups[0], c.Groups[1] = c.Groups[1], c.Groups[0]
	if DescriptorDigest(a) == DescriptorDigest(c) {
		t.Fatal("list order is part of the descriptor")
	}
	if DescriptorDigest(nil) != "" {
		t.Fatal("no descriptor digests to nothing")
	}
}
