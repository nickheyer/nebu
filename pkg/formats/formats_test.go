package formats_test

import (
	"testing"

	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/all"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

func registry(t *testing.T) *formats.Registry {
	t.Helper()
	r, err := all.Registry()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func model(paths ...string) *v1.Model {
	m := &v1.Model{}
	for _, p := range paths {
		m.Artifacts = append(m.Artifacts, &v1.Artifact{Path: p, SizeBytes: 1})
	}
	return m
}

func TestClassify(t *testing.T) {
	c := registry(t)
	m := model(
		"Qwen3-0.6B-Q2_K.gguf", "Qwen3-0.6B-Q2_K_L.gguf", "Qwen3-0.6B-UD-Q2_K_XL.gguf",
		"Q4_K_M/model-Q4_K_M-00001-of-00002.gguf", "Q4_K_M/model-Q4_K_M-00002-of-00002.gguf",
		"gemma-3-27b-it-q4_0.gguf", "mmproj-F16.gguf", "README.md",
		"model-00001-of-00002.safetensors", "model-00002-of-00002.safetensors", "config.json", "tokenizer.json",
		"4bit/model.safetensors", "4bit/config.json",
		"Q8_0/model-Q8_0-00001-of-00002.gguf", "Q8_0/model-Q8_0-00002-of-00002.gguf",
		"MTP/mtp-model-Q8_0.gguf", "MTP/mtp-model-shared-Q4_K_M.gguf", "draft-model-Q4_K_M.gguf",
		"Qwen3.8-27B-GSQ-RCO-IQ2_S.gguf", "Qwen3.8-27B-GSQ-RCO-IQ2_S-mtp.gguf", "imatrix-qwen3.8-27b.gguf", "Meta-Llama-3-8B.Q5_K_S.gguf",
		"Qwen3.8-27B-NEO-CODER-MAX-IQ4_XS.gguf", "Qwen3.8-27B-NEO-CODER-MAX-LOW-MTP-IQ4_XS.gguf", "Qwen3.8-27B-NEO-CODER-MAX-MTP-IQ4_XS.gguf",
		"Big-70B-Q6_K-00001-of-00002.gguf", "Big-70B-Q6_K-00002-of-00002.gguf", "Small-7B-Q6_K.gguf",
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
		// Three models at one quant, told apart by the words before it
		"Qwen3.8-27B-NEO-CODER-MAX-IQ4_XS.gguf":         {"gguf", "weights", "IQ4_XS"},
		"Qwen3.8-27B-NEO-CODER-MAX-LOW-MTP-IQ4_XS.gguf": {"gguf", "weights", "LOW-MTP-IQ4_XS"},
		"Qwen3.8-27B-NEO-CODER-MAX-MTP-IQ4_XS.gguf":     {"gguf", "weights", "MTP-IQ4_XS"},
		// Shards stay one file while another model at the same quant is its own
		"Big-70B-Q6_K-00001-of-00002.gguf": {"gguf", "weights", "Big-70B-Q6_K"},
		"Big-70B-Q6_K-00002-of-00002.gguf": {"gguf", "weights", "Big-70B-Q6_K"},
		"Small-7B-Q6_K.gguf":               {"gguf", "weights", "Small-7B-Q6_K"},
		"model.safetensors.index.json":     {"safetensors", "index", ""},
		"generation_config.json":           {"safetensors", "config", ""},
		"preprocessor_config.json":         {"safetensors", "config", ""},
		"special_tokens_map.json":          {"safetensors", "tokenizer", ""},
		"merges.txt":                       {"safetensors", "tokenizer", ""},
		"modeling_deepseek.py":             {"safetensors", "code", ""},
		"chat_template.jinja":              {"safetensors", "template", ""},
		".gitattributes":                   {"", "other", ""},
	}
	for _, a := range m.GetArtifacts() {
		w, ok := want[a.GetPath()]
		if !ok {
			continue
		}
		role := text.Enum(a.GetRole())
		if a.GetFormatId() != w[0] || role != w[1] || a.GetGroup() != w[2] {
			t.Errorf("%s: got %s/%s/%q want %v", a.GetPath(), a.GetFormatId(), role, a.GetGroup(), w)
		}
	}
	groups := c.Groups(m)
	byName := map[string]*formats.Group{}
	for _, g := range groups {
		byName[g.FormatID+":"+g.Name] = g
	}
	if g := byName["gguf:Q4_K_M"]; g == nil || len(g.Weights) != 2 || g.Weights[0].GetShardIndex() != 1 || g.Weights[1].GetShardCount() != 2 || g.Root != "Q4_K_M" {
		t.Fatalf("shard group %+v", g)
	}
	if g := byName["gguf:Big-70B-Q6_K"]; g == nil || len(g.Weights) != 2 || byName["gguf:Small-7B-Q6_K"] == nil || len(byName["gguf:Small-7B-Q6_K"].Weights) != 1 {
		t.Fatalf("two models at one quant must not merge: %+v %+v", g, byName["gguf:Small-7B-Q6_K"])
	}
	for _, name := range []string{"IQ4_XS", "LOW-MTP-IQ4_XS", "MTP-IQ4_XS"} {
		if g := byName["gguf:"+name]; g == nil || len(g.Weights) != 1 {
			t.Fatalf("variant %s should be one file: %+v", name, g)
		}
	}
	// A quant with a suffix is a second model, not a second file of the first
	if g := byName["gguf:IQ2_S"]; g == nil || len(g.Weights) != 1 || byName["gguf:IQ2_S-mtp"] == nil || len(byName["gguf:IQ2_S-mtp"].Weights) != 1 {
		t.Fatalf("suffixed quant should be its own group: %+v %+v", g, byName["gguf:IQ2_S-mtp"])
	}
	// MTP heads are drafts despite matching weight quantization tokens.
	if g := byName["gguf:Q8_0"]; g == nil || len(g.Weights) != 2 {
		t.Fatalf("draft heads must not join the group of their quant token: %+v", g)
	}
	if len(byName["gguf:Q4_K_M"].Weights) != 2 {
		t.Fatalf("draft heads must not join the group of their quant token: %+v", byName["gguf:Q4_K_M"])
	}
	if g := byName["gguf:Q2_K"]; g == nil || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR]) != 1 || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_DRAFT]) != 1 {
		t.Fatalf("projector and root draft should attach to root gguf groups: %+v", g)
	}
	if g := byName["gguf:Q4_K_M"]; len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR]) != 0 {
		t.Fatal("projector must not attach across directories")
	}
	if g := byName["safetensors:default"]; g == nil || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG]) != 3 || g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG][0].GetPath() != "config.json" {
		t.Fatalf("root config attach %+v", g)
	}
	// Attach auxiliary files so pulls include them.
	if g := byName["safetensors:default"]; len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_INDEX]) != 1 || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CODE]) != 1 || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER]) != 3 || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_TEMPLATE]) != 1 {
		t.Fatalf("index, code, tokenizer, and template attach: %+v", g.Files)
	}
	if g := byName["safetensors:4bit"]; g == nil || g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG][0].GetPath() != "4bit/config.json" {
		t.Fatalf("subdir config attach %+v", g)
	}
	if _, err := formats.FindGroup(groups, "nope"); err == nil {
		t.Fatal("unknown group should fail")
	}
	if _, err := formats.FindGroup(groups, ""); err == nil {
		t.Fatal("no name among many groups should fail")
	}
	if g, err := formats.FindGroup(groups, "Q2_K"); err != nil || g.FormatID != "gguf" {
		t.Fatalf("find by name %v %v", g, err)
	}
	if len(formats.Missing(c.Get("safetensors"), byName["safetensors:4bit"])) != 0 {
		t.Fatal("4bit has config")
	}
}

func TestClassifyDemotesUnreadableGroups(t *testing.T) {
	c := registry(t)
	m := model("lonely/model.safetensors", "README.md", "split_files/vae/wan_2.1_vae.safetensors", "v1-5-pruned-emaonly.ckpt", "flux/ae.sft")
	c.Classify(m)
	want := map[string][2]string{
		"lonely/model.safetensors":                {"diffusion", "model"},
		"split_files/vae/wan_2.1_vae.safetensors": {"diffusion", "wan_2.1_vae"},
		"v1-5-pruned-emaonly.ckpt":                {"diffusion", "v1-5-pruned-emaonly"},
		"flux/ae.sft":                             {"diffusion", "ae"},
	}
	for _, a := range m.GetArtifacts() {
		w, ok := want[a.GetPath()]
		if !ok {
			continue
		}
		if a.GetRole() != v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS || a.GetFormatId() != w[0] || a.GetGroup() != w[1] {
			t.Fatalf("%s: got %s/%s/%q want %v", a.GetPath(), a.GetFormatId(), text.Enum(a.GetRole()), a.GetGroup(), w)
		}
	}
	if groups := c.Groups(m); len(groups) != 4 {
		t.Fatalf("every checkpoint file is a group of its own: %d", len(groups))
	}
}

func TestStemAndShard(t *testing.T) {
	for in, want := range map[string]string{
		"a/model-00001-of-00003.gguf": "a/model",
		"model-Q4_K_M.gguf":           "model-Q4_K_M",
		"x-1-of-2.gguf":               "x-1-of-2",
	} {
		if got := formats.Stem(in); got != want {
			t.Errorf("Stem(%q) = %q, want %q", in, got, want)
		}
	}
	if stem, i, n, ok := formats.Shard("model-00002-of-00005"); !ok || stem != "model" || i != 2 || n != 5 {
		t.Fatalf("shard %q %d %d %v", stem, i, n, ok)
	}
}

func TestDescribe(t *testing.T) {
	c := registry(t)
	list := c.Describe()
	if len(list) != 7 || list[0].GetId() != "gguf" || list[1].GetId() != "diffusers" || list[4].GetId() != "peft" || list[5].GetId() != "safetensors" || list[6].GetId() != "diffusion" || list[0].GetBlurb() == "" {
		t.Fatalf("formats in priority order with words: %v", list)
	}
	if c.Get("nope") != nil {
		t.Fatal("unknown format should be nil")
	}
	if _, err := formats.New([]formats.Format{c.Get("gguf"), c.Get("gguf")}); err == nil {
		t.Fatal("duplicate format should fail")
	}
}
