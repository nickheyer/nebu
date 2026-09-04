package formats

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/spec"
)

func loadFormats(t *testing.T) []*v1.FormatSpec {
	t.Helper()
	c, err := spec.Load(os.DirFS(filepath.Join("..", "..", "spec")))
	if err != nil {
		t.Fatal(err)
	}
	return c.Formats
}

func model(paths ...string) *v1.Model {
	m := &v1.Model{}
	for _, p := range paths {
		m.Artifacts = append(m.Artifacts, &v1.Artifact{Path: p, SizeBytes: 1})
	}
	return m
}

func TestClassify(t *testing.T) {
	c, err := NewClassifier(loadFormats(t))
	if err != nil {
		t.Fatal(err)
	}
	m := model(
		"Qwen3-0.6B-Q2_K.gguf", "Qwen3-0.6B-Q2_K_L.gguf", "Qwen3-0.6B-UD-Q2_K_XL.gguf",
		"Q4_K_M/model-Q4_K_M-00001-of-00002.gguf", "Q4_K_M/model-Q4_K_M-00002-of-00002.gguf",
		"gemma-3-27b-it-q4_0.gguf", "mmproj-F16.gguf", "README.md",
		"model-00001-of-00002.safetensors", "model-00002-of-00002.safetensors", "config.json", "tokenizer.json",
		"4bit/model.safetensors", "4bit/config.json",
	)
	c.Classify(m)
	want := map[string][3]string{
		"Qwen3-0.6B-Q2_K.gguf":                    {"gguf", "weights", "Q2_K"},
		"Qwen3-0.6B-Q2_K_L.gguf":                  {"gguf", "weights", "Q2_K_L"},
		"Qwen3-0.6B-UD-Q2_K_XL.gguf":              {"gguf", "weights", "UD-Q2_K_XL"},
		"Q4_K_M/model-Q4_K_M-00001-of-00002.gguf": {"gguf", "weights", "Q4_K_M"},
		"gemma-3-27b-it-q4_0.gguf":                {"gguf", "weights", "q4_0"},
		"mmproj-F16.gguf":                         {"gguf", "projector", ""},
		"README.md":                               {"", "other", ""},
		"model-00001-of-00002.safetensors":        {"safetensors", "weights", "default"},
		"config.json":                             {"safetensors", "config", ""},
		"tokenizer.json":                          {"safetensors", "tokenizer", ""},
		"4bit/model.safetensors":                  {"safetensors", "weights", "4bit"},
	}
	for _, a := range m.GetArtifacts() {
		w, ok := want[a.GetPath()]
		if !ok {
			continue
		}
		role := roleName(a.GetRole())
		if a.GetFormatId() != w[0] || role != w[1] || a.GetGroup() != w[2] {
			t.Errorf("%s: got %s/%s/%q want %v", a.GetPath(), a.GetFormatId(), role, a.GetGroup(), w)
		}
	}
	groups := c.Groups(m)
	byName := map[string]*Group{}
	for _, g := range groups {
		byName[g.FormatID+":"+g.Name] = g
	}
	if g := byName["gguf:Q4_K_M"]; g == nil || len(g.Weights) != 2 || g.Weights[0].GetShardIndex() != 1 || g.Weights[1].GetShardCount() != 2 {
		t.Fatalf("shard group %+v", g)
	}
	if g := byName["gguf:Q2_K"]; g == nil || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR]) != 1 {
		t.Fatalf("projector should attach to root gguf groups: %+v", g)
	}
	if g := byName["gguf:Q4_K_M"]; len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR]) != 0 {
		t.Fatal("projector must not attach across directories")
	}
	if g := byName["safetensors:default"]; g == nil || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG]) != 1 || g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG][0].GetPath() != "config.json" {
		t.Fatalf("root config attach %+v", g)
	}
	if g := byName["safetensors:4bit"]; g == nil || g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG][0].GetPath() != "4bit/config.json" {
		t.Fatalf("subdir config attach %+v", g)
	}
	if _, err := FindGroup(groups, "nope"); err == nil {
		t.Fatal("unknown group should fail")
	}
	if len(Missing(c.Spec("safetensors"), byName["safetensors:4bit"])) != 0 {
		t.Fatal("4bit has config")
	}
}

func roleName(r v1.ArtifactRole) string {
	switch r {
	case v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS:
		return "weights"
	case v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR:
		return "projector"
	case v1.ArtifactRole_ARTIFACT_ROLE_CONFIG:
		return "config"
	case v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER:
		return "tokenizer"
	}
	return "other"
}

func TestChunkReader(t *testing.T) {
	data := make([]byte, 3*firstChunk+123)
	for i := range data {
		data[i] = byte(i % 251)
	}
	got, err := io.ReadAll(NewChunkReader(bytes.NewReader(data), int64(len(data))))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
}
