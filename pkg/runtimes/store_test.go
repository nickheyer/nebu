package runtimes

import (
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A stored MiniMax-H3 Ref2VA pipeline, as the hub lays it out
func ref2va() *v1.StoredModel {
	d := &v1.Descriptor{FormatId: "diffusers", Group: "Ref2VA", Architecture: "MiniMaxH3DiTModel", Kind: v1.ModelKind_MODEL_KIND_DIFFUSION, Metadata: map[string]string{
		diffusion.KeyFamily: "minimax_h3", diffusion.KeyVAE: "true", diffusion.KeyTextEncoder: "true", diffusion.KeySlots: "denoiser,text_encoder.llm,text_encoder.llm.vision,tokenizer,vae,vae.audio",
		"pipeline.class": "MiniMaxH3Pipeline", "pipeline.transformer": "transformer", "pipeline.transformer.class": "MiniMaxH3DiTModel",
		"pipeline.video_vae": "video_vae", "pipeline.video_vae.class": "MiniMaxH3VideoVAE", "pipeline.audio_vae": "audio_vae", "pipeline.audio_vae.class": "MiniMaxH3AudioVAE",
		"pipeline.text_encoder": "text_encoder", "pipeline.text_encoder.class": "MiniMaxH3Qwen3VLHFEncoder", "pipeline.text_encoder.vision": "true",
		"pipeline.tokenizer": "tokenizer", "pipeline.tokenizer.class": "Qwen2TokenizerFast",
	}}
	file := func(p string, role v1.ArtifactRole) *v1.StoredArtifact {
		return &v1.StoredArtifact{Artifact: &v1.Artifact{Path: p, Role: role}, Path: "/store/hf/MiniMaxAI/MiniMax-H3/" + p}
	}
	return &v1.StoredModel{SourceId: "hf", Repo: "MiniMaxAI/MiniMax-H3", Group: "Ref2VA", FormatId: "diffusers", Descriptor_: d, Artifacts: []*v1.StoredArtifact{
		file("Ref2VA/model_index.json", v1.ArtifactRole_ARTIFACT_ROLE_CONFIG),
		file("Ref2VA/audio_vae/model.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("Ref2VA/text_encoder/model-00001-of-00002.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("Ref2VA/text_encoder/model-00002-of-00002.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("Ref2VA/text_encoder/model.safetensors.index.json", v1.ArtifactRole_ARTIFACT_ROLE_INDEX),
		file("Ref2VA/transformer/model-00001-of-00002.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("Ref2VA/transformer/model-00002-of-00002.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("Ref2VA/transformer/model.safetensors.index.json", v1.ArtifactRole_ARTIFACT_ROLE_INDEX),
		file("Ref2VA/video_vae/config.json", v1.ArtifactRole_ARTIFACT_ROLE_CONFIG),
		file("Ref2VA/video_vae/source/model.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("Ref2VA/processor/tokenizer.json", v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER),
		file("Ref2VA/tokenizer/tokenizer.json", v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER),
	}}
}

func paramOf(rt Runtime, name string) *v1.Param {
	for _, p := range rt.Params() {
		if p.GetName() == name {
			return p
		}
	}
	return nil
}

// A store reference resolves to the part the parameter loads: a pipeline's declared subfolder,
// through the index when sharded, its tokenizer, or a group's weights whatever the group is
func TestPartFileFromPipelinesAndComponents(t *testing.T) {
	rt := SDCpp{}
	h3 := ref2va()
	for param, want := range map[string]string{
		"vae":              "/store/hf/MiniMaxAI/MiniMax-H3/Ref2VA/video_vae/source/model.safetensors",
		"audio_vae":        "/store/hf/MiniMaxAI/MiniMax-H3/Ref2VA/audio_vae/model.safetensors",
		"llm":              "/store/hf/MiniMaxAI/MiniMax-H3/Ref2VA/text_encoder/model.safetensors.index.json",
		"high_noise_model": "/store/hf/MiniMaxAI/MiniMax-H3/Ref2VA/transformer/model.safetensors.index.json",
		"tokenizer":        "/store/hf/MiniMaxAI/MiniMax-H3/Ref2VA/tokenizer/tokenizer.json",
	} {
		got, err := PartFile(h3, paramOf(rt, param))
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", param, got, err, want)
		}
	}
	for _, param := range []string{"t5xxl", "clip_l", "clip_vision"} {
		if _, err := PartFile(h3, paramOf(rt, param)); err == nil || !strings.Contains(err.Error(), "declares no") {
			t.Errorf("%s: a pipeline supplies only what it declares, got %v", param, err)
		}
	}
	// An embedded vision tower resolves to the file holding it, which sd-server judges
	if got, err := PartFile(h3, paramOf(rt, "llm_vision")); err != nil || got != "/store/hf/MiniMaxAI/MiniMax-H3/Ref2VA/text_encoder/model.safetensors.index.json" {
		t.Fatalf("an embedded vision tower: %q %v", got, err)
	}
	vae := stored("Comfy-Org/Wan_2.2", "wan_2.1_vae", "vae", v1.ModelKind_MODEL_KIND_COMPONENT, "vae/wan_2.1_vae.safetensors")
	for _, param := range []string{"vae", "llm", "high_noise_model"} {
		if got, err := PartFile(vae, paramOf(rt, param)); err != nil || got != "/store/vae/wan_2.1_vae.safetensors" {
			t.Fatalf("%s takes the group's weights whatever the group is: %q %v", param, got, err)
		}
	}
	// Directory parameters take the directory holding the weights.
	lora := stored("Kijai/WanVideo_comfy", "loras", "lora", v1.ModelKind_MODEL_KIND_COMPONENT, "loras/a.safetensors")
	lora.Artifacts = append(lora.Artifacts, &v1.StoredArtifact{Artifact: &v1.Artifact{Path: "loras/b.safetensors", Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS}, Path: "/store/loras/b.safetensors"})
	if got, err := PartFile(lora, paramOf(rt, "lora_dir")); err != nil || got != "/store/loras" {
		t.Fatalf("lora dir %q %v", got, err)
	}
	llama := LlamaCpp{}
	qwen := stored("unsloth/Qwen2.5-VL-7B-GGUF", "Q8_0", "qwen2vl", v1.ModelKind_MODEL_KIND_LANGUAGE, "Qwen2.5-VL-7B-Q8_0.gguf", "mmproj-Qwen2.5-VL-7B-F16.gguf")
	if got, err := PartFile(qwen, paramOf(llama, "mmproj")); err != nil || got != "/store/mmproj-Qwen2.5-VL-7B-F16.gguf" {
		t.Fatalf("projector %q %v", got, err)
	}
	if _, err := PartFile(vae, paramOf(llama, "mmproj")); err == nil || !strings.Contains(err.Error(), "no projector file") {
		t.Fatalf("no projector: %v", err)
	}
}

// References resolve before planning and launch, and every explicit path passes through as given
func TestResolveStoreReferencesAndPassesPaths(t *testing.T) {
	rt := SDCpp{}
	h3 := ref2va()
	fast := &v1.StoredModel{SourceId: "hf", Repo: "FastVideo/FastVideo-FastH3-8-Step-V2", Group: "vae", FormatId: "safetensors", Descriptor_: &v1.Descriptor{Architecture: "AutoencoderKLMiniMaxH3", Kind: v1.ModelKind_MODEL_KIND_COMPONENT, Metadata: map[string]string{diffusion.KeyComponent: "vae"}}}
	for _, p := range []string{"vae/diffusion_pytorch_model-00001-of-00003.safetensors", "vae/diffusion_pytorch_model-00002-of-00003.safetensors", "vae/diffusion_pytorch_model-00003-of-00003.safetensors"} {
		fast.Artifacts = append(fast.Artifacts, &v1.StoredArtifact{Artifact: &v1.Artifact{Path: p, Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS}, Path: "/store/hf/FastVideo/" + p})
	}
	fast.Artifacts = append(fast.Artifacts, &v1.StoredArtifact{Artifact: &v1.Artifact{Path: "vae/diffusion_pytorch_model.safetensors.index.json", Role: v1.ArtifactRole_ARTIFACT_ROLE_INDEX}, Path: "/store/hf/FastVideo/vae/diffusion_pytorch_model.safetensors.index.json"})
	models := []*v1.StoredModel{fast, h3}
	out, err := ResolveStore(rt, map[string]string{
		"vae":       StoreRef(fast),
		"audio_vae": StoreRef(h3),
		"tokenizer": "store://hf/MiniMaxAI/MiniMax-H3#Ref2VA",
		"taesd":     "/elsewhere/taesd.safetensors",
		"steps":     "8",
	}, models)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"vae":       "/store/hf/FastVideo/vae/diffusion_pytorch_model.safetensors.index.json",
		"audio_vae": "/store/hf/MiniMaxAI/MiniMax-H3/Ref2VA/audio_vae/model.safetensors",
		"tokenizer": "/store/hf/MiniMaxAI/MiniMax-H3/Ref2VA/tokenizer/tokenizer.json",
		"taesd":     "/elsewhere/taesd.safetensors",
		"steps":     "8",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %q, want %q", k, out[k], v)
		}
	}
	// A reference to nothing stored, or to no group at all, resolves to no file
	for param, value := range map[string]string{"clip_l": "store://hf/Nobody/Nothing#default", "t5xxl": "store://broken"} {
		_, err := ResolveStore(rt, map[string]string{param: value}, models)
		if err == nil || !strings.Contains(err.Error(), "param "+param) {
			t.Errorf("%s=%s: %v", param, value, err)
		}
	}
	// Any explicit path passes as given, one shard, another part, a config file or the pipeline's own
	paths := map[string]string{
		"vae":              "/store/hf/FastVideo/vae/diffusion_pytorch_model-00001-of-00003.safetensors",
		"high_noise_model": "/store/hf/MiniMaxAI/MiniMax-H3/Ref2VA/audio_vae/model.safetensors",
		"uncond_model":     "/store/hf/MiniMaxAI/MiniMax-H3/Ref2VA/video_vae/source/model.safetensors",
		"llm":              "/store/hf/MiniMaxAI/MiniMax-H3/Ref2VA/model_index.json",
		"lora_dir":         "/store/hf/MiniMaxAI/MiniMax-H3/Ref2VA/audio_vae",
		"t5xxl":            "auto",
	}
	out, err = ResolveStore(rt, paths, models)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range paths {
		if out[k] != v {
			t.Errorf("%s = %q, want %q", k, out[k], v)
		}
	}
}

// A draft pick takes a group's first head file, else the group's weights, and any path passes
func TestDraftPicksHeadFilesAndModels(t *testing.T) {
	rt := LlamaCpp{}
	file := func(p string, role v1.ArtifactRole) *v1.StoredArtifact {
		return &v1.StoredArtifact{Artifact: &v1.Artifact{Path: p, Role: role}, Path: "/store/" + p}
	}
	language := &v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_LANGUAGE}
	gemma := &v1.StoredModel{SourceId: "hf", Repo: "unsloth/gemma-4-31B-it-GGUF", Group: "Q4_K_M", FormatId: "gguf", Descriptor_: language, Artifacts: []*v1.StoredArtifact{
		file("gemma-4-31B-it-Q4_K_M.gguf", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("mtp-gemma-4-31B-it.gguf", v1.ArtifactRole_ARTIFACT_ROLE_DRAFT),
		file("mmproj-F16.gguf", v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR),
	}}
	twin := &v1.StoredModel{SourceId: "hf", Repo: "org/twin", Group: "Q8_0", FormatId: "gguf", Descriptor_: language, Artifacts: []*v1.StoredArtifact{
		file("twin-Q8_0.gguf", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("MTP/mtp-model-Q8_0.gguf", v1.ArtifactRole_ARTIFACT_ROLE_DRAFT),
		file("MTP/mtp-model-shared-Q4_K_M.gguf", v1.ArtifactRole_ARTIFACT_ROLE_DRAFT),
	}}
	small := &v1.StoredModel{SourceId: "hf", Repo: "org/small", Group: "Q4_0", FormatId: "gguf", Descriptor_: language, Artifacts: []*v1.StoredArtifact{file("small-Q4_0.gguf", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS)}}
	vae := &v1.StoredModel{SourceId: "hf", Repo: "org/vae", Group: "vae", FormatId: "gguf", Descriptor_: &v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_COMPONENT}, Artifacts: []*v1.StoredArtifact{file("vae.gguf", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS)}}
	models := []*v1.StoredModel{gemma, twin, small, vae}
	out, err := ResolveStore(rt, map[string]string{"draft_model": StoreRef(gemma)}, models)
	if err != nil || out["draft_model"] != "/store/mtp-gemma-4-31B-it.gguf" {
		t.Fatalf("the one head file: %v %v", out, err)
	}
	if out, err = ResolveStore(rt, map[string]string{"draft_model": StoreRef(small)}, models); err != nil || out["draft_model"] != "/store/small-Q4_0.gguf" {
		t.Fatalf("a draft model's weights: %v %v", out, err)
	}
	if out, err = ResolveStore(rt, map[string]string{"draft_model": StoreRef(twin)}, models); err != nil || out["draft_model"] != "/store/MTP/mtp-model-Q8_0.gguf" {
		t.Fatalf("the first of several head files: %v %v", out, err)
	}
	if out, err = ResolveStore(rt, map[string]string{"draft_model": StoreRef(vae)}, models); err != nil || out["draft_model"] != "/store/vae.gguf" {
		t.Fatalf("any group's weights: %v %v", out, err)
	}
	// Every explicit path passes, llama-server judging what it loads
	for param, path := range map[string]string{"draft_model": "/store/MTP/mtp-model-shared-Q4_K_M.gguf", "mmproj": "/store/mtp-gemma-4-31B-it.gguf"} {
		if out, err := ResolveStore(rt, map[string]string{param: path}, models); err != nil || out[param] != path {
			t.Fatalf("%s=%s: %v %v", param, path, out, err)
		}
	}
	if out, err := ResolveStore(rt, map[string]string{"mmproj": StoreRef(gemma)}, models); err != nil || out["mmproj"] != "/store/mmproj-F16.gguf" {
		t.Fatalf("the projector of a group: %v %v", out, err)
	}
}

// An empty number is unset, so the runtime keeps its default and no flag carries a zero
func TestUnsetNumbersCarryNoFlag(t *testing.T) {
	params, err := Resolve(SGLang{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if params["speculative_num_steps"] != nil {
		t.Fatalf("unset steps %v", params["speculative_num_steps"])
	}
	args, _, emitted := Flags(SGLang{}.Params(), params)
	if line := strings.Join(args, " "); strings.Contains(line, "--speculative-num-steps") || strings.Contains(line, "--speculative-eagle-topk") || emitted["speculative_num_draft_tokens"] != "" {
		t.Fatalf("unset numbers emitted: %s", line)
	}
	if params, err = Resolve(SGLang{}, map[string]string{"speculative_num_steps": "3"}); err != nil || params.Int("speculative_num_steps") != 3 {
		t.Fatalf("a set number %v %v", params, err)
	}
}
