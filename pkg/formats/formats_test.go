package formats

import (
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
		"Q8_0/model-Q8_0-00001-of-00002.gguf", "Q8_0/model-Q8_0-00002-of-00002.gguf",
		"MTP/mtp-model-Q8_0.gguf", "MTP/mtp-model-shared-Q4_K_M.gguf", "draft-model-Q4_K_M.gguf",
		"Qwen3.8-27B-GSQ-RCO-IQ2_S.gguf", "Qwen3.8-27B-GSQ-RCO-IQ2_S-mtp.gguf", "imatrix-qwen3.8-27b.gguf", "Meta-Llama-3-8B.Q5_K_S.gguf",
		"model.safetensors.index.json", "generation_config.json", "preprocessor_config.json", "special_tokens_map.json", "merges.txt", "modeling_deepseek.py", "chat_template.jinja", ".gitattributes",
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
		"Q8_0/model-Q8_0-00001-of-00002.gguf":     {"gguf", "weights", "Q8_0"},
		"MTP/mtp-model-Q8_0.gguf":                 {"gguf", "draft", ""},
		"MTP/mtp-model-shared-Q4_K_M.gguf":        {"gguf", "draft", ""},
		"draft-model-Q4_K_M.gguf":                 {"gguf", "draft", ""},
		"Qwen3.8-27B-GSQ-RCO-IQ2_S.gguf":          {"gguf", "weights", "IQ2_S"},
		"Qwen3.8-27B-GSQ-RCO-IQ2_S-mtp.gguf":      {"gguf", "weights", "IQ2_S-mtp"},
		"imatrix-qwen3.8-27b.gguf":                {"gguf", "other", ""},
		"Meta-Llama-3-8B.Q5_K_S.gguf":             {"gguf", "weights", "Q5_K_S"},
		"model.safetensors.index.json":            {"safetensors", "index", ""},
		"generation_config.json":                  {"safetensors", "config", ""},
		"preprocessor_config.json":                {"safetensors", "config", ""},
		"special_tokens_map.json":                 {"safetensors", "tokenizer", ""},
		"merges.txt":                              {"safetensors", "tokenizer", ""},
		"modeling_deepseek.py":                    {"safetensors", "code", ""},
		"chat_template.jinja":                     {"safetensors", "template", ""},
		".gitattributes":                          {"", "other", ""},
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
	// A quant with a suffix is a second model, not a second file of the first
	if g := byName["gguf:IQ2_S"]; g == nil || len(g.Weights) != 1 || byName["gguf:IQ2_S-mtp"] == nil || len(byName["gguf:IQ2_S-mtp"].Weights) != 1 {
		t.Fatalf("suffixed quant should be its own group: %+v %+v", g, byName["gguf:IQ2_S-mtp"])
	}
	// The MTP heads carry the quant tokens of Q8_0 and Q4_K_M but load as drafts, so neither group grows a file
	if g := byName["gguf:Q8_0"]; g == nil || len(g.Weights) != 2 {
		t.Fatalf("draft heads must not join the group of their quant token: %+v", g)
	}
	if len(byName["gguf:Q4_K_M"].Weights) != 2 {
		t.Fatalf("draft heads must not join the group of their quant token: %+v", byName["gguf:Q4_K_M"])
	}
	if g := byName["gguf:Q2_K"]; g == nil || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR]) != 1 || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_DRAFT]) != 1 {
		t.Fatalf("projector and root draft should attach to root gguf groups: %+v", g)
	}
	if g := byName["gguf:Q2_K"]; g == nil || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR]) != 1 {
		t.Fatalf("projector should attach to root gguf groups: %+v", g)
	}
	if g := byName["gguf:Q4_K_M"]; len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR]) != 0 {
		t.Fatal("projector must not attach across directories")
	}
	if g := byName["safetensors:default"]; g == nil || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG]) != 3 || g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG][0].GetPath() != "config.json" {
		t.Fatalf("root config attach %+v", g)
	}
	// Every file a loader reads beside the weights attaches to the group, so a pull lands it
	if g := byName["safetensors:default"]; len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_INDEX]) != 1 || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CODE]) != 1 || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER]) != 3 || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_TEMPLATE]) != 1 {
		t.Fatalf("index, code, tokenizer, and template attach: %+v", g.Files)
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
	case v1.ArtifactRole_ARTIFACT_ROLE_DRAFT:
		return "draft"
	case v1.ArtifactRole_ARTIFACT_ROLE_INDEX:
		return "index"
	case v1.ArtifactRole_ARTIFACT_ROLE_CODE:
		return "code"
	case v1.ArtifactRole_ARTIFACT_ROLE_TEMPLATE:
		return "template"
	}
	return "other"
}
