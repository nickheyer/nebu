package runtimes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/blueprint"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

func sdDescriptor(family string, bundled bool, image bool) *v1.Descriptor {
	d := &v1.Descriptor{FormatId: "diffusion", Group: "wan2.2_t2v_low_noise_14B_fp8", Architecture: family, Kind: v1.ModelKind_MODEL_KIND_DIFFUSION, Metadata: map[string]string{diffusion.KeyFamily: family, diffusion.KeyVAE: "false", diffusion.KeyTextEncoder: "false"}, Params: map[string]float64{"n_embd": 5120}}
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "diffusion", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION, Layer: -1, Bytes: 14 << 30})
	if bundled {
		d.Metadata[diffusion.KeyVAE], d.Metadata[diffusion.KeyTextEncoder] = "true", "true"
		d.Groups = append(d.Groups, &v1.TensorGroup{Id: "vae", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE, Layer: -1, Bytes: 160 << 20})
		d.Groups = append(d.Groups, &v1.TensorGroup{Id: "text_encoder", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER, Layer: -1, Bytes: 1 << 30})
	}
	if image {
		d.Metadata[diffusion.KeyImageInput] = "true"
	}
	return d
}

func estimateModel(d *v1.Descriptor) formats.Params { return formats.ParamsOf(d.GetParams()) }

// Megabytes as a byte count, for fractions the log prints
func megabytes(n float64) uint64 { return uint64(n * float64(1<<20)) }

func stored(repo, group, arch string, kind v1.ModelKind, paths ...string) *v1.StoredModel {
	m := &v1.StoredModel{SourceId: "hf", Repo: repo, Group: group, Descriptor_: &v1.Descriptor{Architecture: arch, Kind: kind}}
	for i, p := range paths {
		role := v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS
		if i > 0 {
			role = v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR
		}
		if filepath.Base(p) == "tokenizer.json" {
			role = v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER
		}
		m.Artifacts = append(m.Artifacts, &v1.StoredArtifact{Artifact: &v1.Artifact{Path: p, Role: role}, Path: "/store/" + p})
	}
	return m
}

func TestSDCppLaunch(t *testing.T) {
	rt := SDCpp{}
	prepared := t.TempDir()
	lora := stored("Kijai/WanVideo_comfy", "lightx2v_4steps", "lora", v1.ModelKind_MODEL_KIND_COMPONENT, "loras/lightx2v_4steps.safetensors")
	os.WriteFile(filepath.Join(prepared, "lora.safetensors"), nil, 0o644)
	lora.Artifacts[0].Path = filepath.Join(prepared, "lora.safetensors")
	// Root adapters use repository names because their group is default.
	adapter := stored("TaoLiveAIGC/TaoMate-H3", "default", "lora", v1.ModelKind_MODEL_KIND_COMPONENT, "adapter_model.safetensors")
	os.WriteFile(filepath.Join(prepared, "adapter.safetensors"), nil, 0o644)
	adapter.Artifacts[0].Path = filepath.Join(prepared, "adapter.safetensors")
	params, err := Resolve(rt, map[string]string{"vae": "/store/vae.safetensors", "t5xxl": "/store/umt5.gguf", "on_device": "3", "width": "832", "height": "480"})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := rt.Launch(Launch{Name: "wan", Params: params, Artifacts: map[string]string{"weights": "/store/wan.gguf", "prepared_dir": prepared}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/sd-server"}, Descriptor: sdDescriptor("wan", false, false), Devices: []*v1.Device{{Id: "GPU-1", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia", Facts: map[string]string{"index": "0"}}}, Stored: []*v1.StoredModel{lora, adapter}})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd.Args, " ")
	if cmd.Command != "/bin/sd-server" || !strings.HasPrefix(line, "--listen-ip 127.0.0.1 --listen-port 9 --diffusion-model /store/wan.gguf") {
		t.Fatalf("launch %s", line)
	}
	for _, want := range []string{"--vae /store/vae.safetensors", "--t5xxl /store/umt5.gguf", "--width 832", "--height 480", "--diffusion-fa", "--log-level debug", "--auto-fit on", "--lora-model-dir " + filepath.Join(prepared, "loras")} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %s in %s", want, line)
		}
	}
	// Link stored LoRAs by group name.
	if target, err := os.Readlink(filepath.Join(prepared, "loras", "lightx2v_4steps.safetensors")); err != nil || target != lora.Artifacts[0].Path {
		t.Fatalf("lora link %q %v", target, err)
	}
	if target, err := os.Readlink(filepath.Join(prepared, "loras", "TaoMate-H3.safetensors")); err != nil || target != adapter.Artifacts[0].Path {
		t.Fatalf("adapter link %q %v", target, err)
	}
	// Full device placement disables offload. Unresolved file parameters emit no flag.
	if strings.Contains(line, "--offload-to-cpu") || strings.Contains(line, "--clip_l") || strings.Contains(line, "--offload ") || cmd.Params["offload"] != "off" {
		t.Fatalf("offload %s %v", line, cmd.Params)
	}
	if cmd.Env["CUDA_VISIBLE_DEVICES"] != "GPU-1" {
		t.Fatalf("env %v", cmd.Env)
	}
	// Partial device placement or an explicit request enables offload.
	params["on_device"] = "0"
	cmd, err = rt.Launch(Launch{Name: "wan", Params: params, Artifacts: map[string]string{"weights": "/store/wan.gguf", "prepared_dir": prepared}, Install: Install{Path: "/bin/sd-server"}, Descriptor: sdDescriptor("wan", false, false)})
	if err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--offload-to-cpu") || cmd.Params["offload"] != "on" {
		t.Fatalf("offload from the plan %v %v", cmd, err)
	}
	// Bundled checkpoints use --model.
	params, _ = Resolve(rt, nil)
	cmd, err = rt.Launch(Launch{Name: "sdxl", Params: params, Artifacts: map[string]string{"weights": "/store/sd_xl_base_1.0.safetensors", "prepared_dir": prepared}, Install: Install{Path: "/bin/sd-server"}, Descriptor: sdDescriptor("sdxl", true, false), Placement: v1.Placement_PLACEMENT_HOST})
	if err != nil {
		t.Fatal(err)
	}
	line = strings.Join(cmd.Args, " ")
	if !strings.Contains(line, "--model /store/sd_xl_base_1.0.safetensors") || strings.Contains(line, "--diffusion-model") || !strings.Contains(line, "--backend cpu") || cmd.Env["CUDA_VISIBLE_DEVICES"] != "" {
		t.Fatalf("bundled launch %s %v", line, cmd.Env)
	}
	// Reject unsupported layouts and placement.
	if _, err := rt.Launch(Launch{Params: params, Artifacts: map[string]string{"weights": "/store/model-00001-of-00003.safetensors", "prepared_dir": prepared}, Descriptor: sdDescriptor("flux", false, false)}); err == nil {
		t.Fatal("shards should be refused")
	}
	part := sdDescriptor("vae", false, false)
	part.Kind = v1.ModelKind_MODEL_KIND_COMPONENT
	if _, err := rt.Launch(Launch{Name: "ae", Params: params, Artifacts: map[string]string{"weights": "/store/ae.safetensors", "prepared_dir": prepared}, Descriptor: part}); err == nil || !strings.Contains(err.Error(), "a VAE") {
		t.Fatalf("a component should be refused: %v", err)
	}
	params["backend"] = "cuda0"
	if _, err := rt.Launch(Launch{Params: params, Artifacts: map[string]string{"weights": "/w.gguf", "prepared_dir": prepared}, Descriptor: sdDescriptor("flux", false, false), Placement: v1.Placement_PLACEMENT_HOST}); err == nil {
		t.Fatal("an accelerator backend in host memory should be refused")
	}
	if _, err := rt.Launch(Launch{Params: params, Artifacts: map[string]string{"weights": "/w.gguf"}, Descriptor: sdDescriptor("flux", false, false)}); err == nil {
		t.Fatal("no prepared directory should be refused")
	}
}

func TestSDCppLaunchPipeline(t *testing.T) {
	rt := SDCpp{}
	prepared := t.TempDir()
	d := &v1.Descriptor{FormatId: "diffusers", Group: "transformer", Architecture: "MiniMaxH3Transformer3DModel", Kind: v1.ModelKind_MODEL_KIND_DIFFUSION, Metadata: map[string]string{
		diffusion.KeyFamily: "minimax_h3", diffusion.KeyVAE: "true", diffusion.KeyTextEncoder: "true", diffusion.KeySlots: "denoiser,text_encoder.llm,text_encoder.llm.vision,tokenizer,vae,vae.audio",
		"pipeline.class": "MiniMaxH3ModularPipeline", "pipeline.transformer": "transformer", "pipeline.transformer.class": "MiniMaxH3Transformer3DModel",
		"pipeline.vae": "vae", "pipeline.vae.class": "AutoencoderKLMiniMaxH3", "pipeline.audio_vae": "audio_vae", "pipeline.audio_vae.class": "AutoencoderKLMiniMaxH3Audio",
		"pipeline.text_encoder": "text_encoder", "pipeline.text_encoder.class": "Qwen3VLForConditionalGeneration", "pipeline.text_encoder.vision": "true",
		"pipeline.tokenizer": "tokenizer", "pipeline.tokenizer.class": "Qwen2TokenizerFast",
	}}
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "diffusion", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION, Layer: -1, Bytes: 66 << 30})
	file := func(p string, role v1.ArtifactRole) *v1.StoredArtifact {
		return &v1.StoredArtifact{Artifact: &v1.Artifact{Path: p, Role: role}, Path: "/store/transformer/" + p}
	}
	model := &v1.StoredModel{Repo: "MiniMaxAI/MiniMax-H3", Group: "transformer", FormatId: "diffusers", Artifacts: []*v1.StoredArtifact{
		file("model_index.json", v1.ArtifactRole_ARTIFACT_ROLE_CONFIG),
		file("transformer/diffusion_pytorch_model-00001-of-00002.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("transformer/diffusion_pytorch_model-00002-of-00002.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("transformer/diffusion_pytorch_model.safetensors.index.json", v1.ArtifactRole_ARTIFACT_ROLE_INDEX),
		file("vae/diffusion_pytorch_model.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("audio_vae/diffusion_pytorch_model.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("text_encoder/model-00001-of-00002.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("text_encoder/model-00002-of-00002.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS),
		file("text_encoder/model.safetensors.index.json", v1.ArtifactRole_ARTIFACT_ROLE_INDEX),
		file("tokenizer/tokenizer.json", v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER),
	}}
	params, err := Resolve(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := rt.Launch(Launch{Name: "h3", Params: params, Artifacts: map[string]string{"weights": "/store/transformer/audio_vae/diffusion_pytorch_model.safetensors", "prepared_dir": prepared}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/sd-server"}, Descriptor: d, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd.Args, " ")
	for _, want := range []string{"--diffusion-model /store/transformer/transformer/diffusion_pytorch_model.safetensors.index.json", "--vae /store/transformer/vae/diffusion_pytorch_model.safetensors", "--audio-vae /store/transformer/audio_vae/diffusion_pytorch_model.safetensors", "--llm /store/transformer/text_encoder/model.safetensors.index.json"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %s in %s", want, line)
		}
	}
	for _, unwanted := range []string{"--llm_vision", "--tokenizer", "--model "} {
		if strings.Contains(line, unwanted) {
			t.Errorf("unwanted %s in %s", unwanted, line)
		}
	}
	params["vae"] = "/elsewhere/vae.safetensors"
	cmd, err = rt.Launch(Launch{Name: "h3", Params: params, Artifacts: map[string]string{"weights": "/store/transformer/audio_vae/diffusion_pytorch_model.safetensors", "prepared_dir": prepared}, Install: Install{Path: "/bin/sd-server"}, Descriptor: d, Model: model})
	if err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--vae /elsewhere/vae.safetensors") {
		t.Fatalf("explicit file path changed: %v %v", cmd, err)
	}
	// Reject pipelines without stored files.
	if _, err := rt.Launch(Launch{Name: "h3", Params: params, Artifacts: map[string]string{"weights": "/w", "prepared_dir": prepared}, Install: Install{Path: "/bin/sd-server"}, Descriptor: d}); err == nil {
		t.Fatal("no stored model should be refused")
	}
}

func TestSDCppPolicySolvesCompanions(t *testing.T) {
	rt := SDCpp{}
	params, _ := Resolve(rt, nil)
	companions := []*v1.StoredModel{
		stored("black-forest-labs/FLUX.1-dev", "ae", "vae", v1.ModelKind_MODEL_KIND_COMPONENT, "ae.safetensors"),
		stored("Comfy-Org/Wan_2.2", "wan_2.1_vae", "vae", v1.ModelKind_MODEL_KIND_COMPONENT, "vae/wan_2.1_vae.safetensors"),
		stored("city96/umt5-xxl-encoder-gguf", "umt5-xxl-encoder-Q8_0", "t5", v1.ModelKind_MODEL_KIND_COMPONENT, "umt5-xxl-encoder-Q8_0.gguf"),
		stored("Comfy-Org/Wan_2.2", "clip_vision_h", "clip_vision", v1.ModelKind_MODEL_KIND_COMPONENT, "clip_vision/clip_vision_h.safetensors"),
		stored("Comfy-Org/Wan_2.2", "wan2.2_t2v_high_noise_14B_fp8", "wan", v1.ModelKind_MODEL_KIND_DIFFUSION, "diffusion_models/wan2.2_t2v_high_noise_14B_fp8.safetensors"),
		stored("unsloth/Qwen2.5-VL-7B-GGUF", "Q8_0", "qwen2vl", v1.ModelKind_MODEL_KIND_LANGUAGE, "Qwen2.5-VL-7B-Q8_0.gguf", "mmproj-Qwen2.5-VL-7B-F16.gguf"),
	}
	s := &estimate.Scope{Descriptor: sdDescriptor("wan", false, true), Params: params.Clone(), Companions: companions, Repo: "Comfy-Org/Wan_2.2"}
	rt.Policy().Solve(s)
	want := map[string]string{
		"vae":              "/store/vae/wan_2.1_vae.safetensors",
		"t5xxl":            "/store/umt5-xxl-encoder-Q8_0.gguf",
		"clip_vision":      "/store/clip_vision/clip_vision_h.safetensors",
		"high_noise_model": "/store/diffusion_models/wan2.2_t2v_high_noise_14B_fp8.safetensors",
	}
	for k, v := range want {
		if s.Params.Str(k) != v {
			t.Errorf("%s = %q, want %q", k, s.Params.Str(k), v)
		}
	}
	// Unlisted slots remain auto.
	if !s.Params.IsAuto("clip_l") || !s.Params.IsAuto("audio_encoder") || !s.Params.IsAuto("tokenizer") || !s.Params.IsAuto("llm") || !s.Params.IsAuto("llm_vision") {
		t.Fatal("parts the blueprint does not list stay auto")
	}
	if _, refusal := rt.Policy().States(s); refusal != "" {
		t.Fatalf("wan with its parts should run: %s", refusal)
	}
	// Select the required language encoder and its projector.
	qwen := sdDescriptor("qwen_image", false, true)
	qwen.Group = "qwen_image_fp8_e4m3fn"
	qs := &estimate.Scope{Descriptor: qwen, Params: params.Clone(), Companions: companions, Repo: "Comfy-Org/Qwen-Image_ComfyUI"}
	rt.Policy().Solve(qs)
	if qs.Params.Str("llm") != "/store/Qwen2.5-VL-7B-Q8_0.gguf" || qs.Params.Str("llm_vision") != "/store/mmproj-Qwen2.5-VL-7B-F16.gguf" {
		t.Fatalf("qwen image llm %q vision %q", qs.Params.Str("llm"), qs.Params.Str("llm_vision"))
	}
	if !qs.Params.IsAuto("vae") {
		t.Fatal("wan's vae is not qwen image's")
	}
	// Missing encoders reject launch with source information.
	bare := &estimate.Scope{Descriptor: sdDescriptor("wan", false, false), Params: params.Clone(), Companions: companions[:2], Repo: "Comfy-Org/Wan_2.2"}
	rt.Policy().Solve(bare)
	if _, refusal := rt.Policy().States(bare); !strings.Contains(refusal, "t5xxl") || !strings.Contains(refusal, "UMT5-XXL") || !strings.Contains(refusal, "city96/umt5-xxl-encoder-gguf") || strings.Contains(refusal, "vae (") || strings.Contains(refusal, "high_noise_model") {
		t.Fatalf("refusal %q", refusal)
	}
	if missing := sdMissing(bare); len(missing) != 1 || missing[0].Param != "t5xxl" || missing[0].Slot != "text_encoder.t5" {
		t.Fatalf("missing %+v", missing)
	}
	// Bundled checkpoints need no companions. Standalone components cannot run.
	if _, refusal := rt.Policy().States(&estimate.Scope{Descriptor: sdDescriptor("sdxl", true, false), Params: params.Clone()}); refusal != "" {
		t.Fatalf("sdxl bundles everything: %s", refusal)
	}
	part := sdDescriptor("t5", false, false)
	part.Kind = v1.ModelKind_MODEL_KIND_COMPONENT
	if _, refusal := rt.Policy().States(&estimate.Scope{Descriptor: part, Params: params.Clone()}); !strings.Contains(refusal, "T5 text encoder") {
		t.Fatalf("component refusal %q", refusal)
	}
}

// A denoiser whose tensor names nebu did not identify launches, sd-server judging it, and the
// plan says so.
func TestSDCppLaunchForce(t *testing.T) {
	rt := SDCpp{}
	prepared := t.TempDir()
	params, err := Resolve(rt, map[string]string{"vae": "/store/qwen_image_2.1_vae_bf16.safetensors", "llm": "/store/qwen3vl_8b_bf16.safetensors"})
	if err != nil {
		t.Fatal(err)
	}
	d := &v1.Descriptor{FormatId: "gguf", Group: "Q8_0", Architecture: "qwen_image21_future", Kind: v1.ModelKind_MODEL_KIND_DIFFUSION, Metadata: map[string]string{diffusion.KeyDetected: "false"}, Params: map[string]float64{"n_embd": 4096}}
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "diffusion", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION, Layer: -1, Bytes: 7 << 30})
	launch := Launch{Name: "q", Params: params, Artifacts: map[string]string{"weights": "/store/q8.gguf", "prepared_dir": prepared}, Install: Install{Path: "/bin/sd-server"}, Descriptor: d}
	if _, refusal := rt.Policy().States(&estimate.Scope{Descriptor: d, Params: params.Clone()}); !strings.Contains(refusal, "Q8_0") || !strings.Contains(refusal, "sd-server will try it") {
		t.Fatalf("the plan says sd-server judges it: %q", refusal)
	}
	cmd, err := rt.Launch(launch)
	if err != nil {
		t.Fatalf("force launches: %v", err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "--diffusion-model /store/q8.gguf") || !strings.Contains(joined, "--vae /store/qwen_image_2.1_vae_bf16.safetensors") || !strings.Contains(joined, "--llm /store/qwen3vl_8b_bf16.safetensors") {
		t.Fatalf("forced args %q", joined)
	}
}

// Qwen-Image 2.1 resolves its parts and sampling defaults.
func TestSDCppQwenImage21(t *testing.T) {
	rt := SDCpp{}
	params, err := Resolve(rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := sdDescriptor("qwen_image21", false, true)
	d.Group = "Q8_0"
	d.Metadata[diffusion.KeyDetected] = "true"
	vae := stored("abenzerps/Qwen-Image-2.1-Uncensored-GGUF", "qwen_image_2.1_vae_bf16", "vae", v1.ModelKind_MODEL_KIND_COMPONENT, "vae/qwen_image_2.1_vae_bf16.safetensors")
	vae.Descriptor_.Metadata = map[string]string{diffusion.KeyLatentChannels: "64", diffusion.KeyVideoVAE: "true"}
	llm := stored("abenzerps/Qwen-Image-2.1-Uncensored-GGUF", "qwen3vl_8b_bf16", "llm", v1.ModelKind_MODEL_KIND_COMPONENT, "text_encoders/qwen3vl_8b_bf16.safetensors")
	llm.Descriptor_.ParameterCount = 8_800_000_000
	s := &estimate.Scope{Descriptor: d, Params: params.Clone(), Companions: []*v1.StoredModel{vae, llm}, Repo: "abenzerps/Qwen-Image-2.1-Uncensored-GGUF"}
	rt.Policy().Solve(s)
	if s.Params.Str("vae") != "/store/vae/qwen_image_2.1_vae_bf16.safetensors" || s.Params.Str("llm") != "/store/text_encoders/qwen3vl_8b_bf16.safetensors" {
		t.Fatalf("vae %q llm %q", s.Params.Str("vae"), s.Params.Str("llm"))
	}
	if _, refusal := rt.Policy().States(s); refusal != "" {
		t.Fatalf("qwen image 2.1 with its parts should run: %s", refusal)
	}
	if s.Params.Int("steps") != 40 || s.Params.Str("scheduler") != "flux" || s.Params.Str("sampling_method") != "euler" {
		t.Fatalf("defaults steps %d scheduler %q sampler %q", s.Params.Int("steps"), s.Params.Str("scheduler"), s.Params.Str("sampling_method"))
	}
}

func TestSDCppHiresUpscaler(t *testing.T) {
	rt := SDCpp{}
	esrgan := stored("ai-forever/Real-ESRGAN", "RealESRGAN_x4plus", "upscaler", v1.ModelKind_MODEL_KIND_COMPONENT, "RealESRGAN_x4plus.pth")
	for _, name := range []string{"Latent", "Lanczos", "RealESRGAN_x4plus"} {
		params, err := Resolve(rt, map[string]string{"hires_upscaler": name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, refusal := rt.Policy().States(&estimate.Scope{Descriptor: sdDescriptor("sdxl", true, false), Params: params, Companions: []*v1.StoredModel{esrgan}}); refusal != "" {
			t.Fatalf("%s: %s", name, refusal)
		}
	}
	params, err := Resolve(rt, map[string]string{"hires_upscaler": "RealESRGAN_x4plus"})
	if err != nil {
		t.Fatal(err)
	}
	if _, refusal := rt.Policy().States(&estimate.Scope{Descriptor: sdDescriptor("sdxl", true, false), Params: params}); !strings.Contains(refusal, "RealESRGAN_x4plus") {
		t.Fatalf("refusal %q", refusal)
	}
	launch := Launch{Name: "sdxl", Params: params, Artifacts: map[string]string{"weights": "/store/sd_xl_base_1.0.safetensors", "prepared_dir": t.TempDir()}, Install: Install{Path: "/bin/sd-server"}, Descriptor: sdDescriptor("sdxl", true, false)}
	if _, err := rt.Launch(launch); err == nil || !strings.Contains(err.Error(), "RealESRGAN_x4plus") {
		t.Fatalf("launch without the upscaler stored: %v", err)
	}
	launch.Stored = []*v1.StoredModel{esrgan}
	cmd, err := rt.Launch(launch)
	if err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--hires-upscaler RealESRGAN_x4plus") {
		t.Fatalf("launch with the upscaler stored: %v %v", cmd, err)
	}
}

func TestSDCppPlan(t *testing.T) {
	rt := SDCpp{}
	params, _ := Resolve(rt, nil)
	d := sdDescriptor("wan", true, false)
	h := &v1.HostProfile{Pools: []*v1.MemoryPool{{Id: "gpu0", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: 24 << 30}, {Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 64 << 30}}}
	plan, err := rt.Policy().Plan(estimate.Input{Descriptor: d, Host: h, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || plan.GetParams()["on_device"] != "3" || plan.GetOverheadBytes() == 0 || plan.GetCacheBytes() != 0 {
		t.Fatalf("plan %+v", plan)
	}
	// Keep the denoiser and VAE on device when the encoder does not fit.
	d.Groups[2].Bytes = 4 << 30
	small := &v1.HostProfile{Pools: []*v1.MemoryPool{{Id: "gpu0", Kind: v1.PoolKind_POOL_KIND_DEVICE, TotalBytes: 20 << 30}, {Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 64 << 30}}}
	plan, err = rt.Policy().Plan(estimate.Input{Descriptor: d, Host: small, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_PARTIAL || plan.GetParams()["on_device"] != "2" {
		t.Fatalf("expected denoiser on device and remaining components offloaded: %+v", plan)
	}
	// Compute memory follows the default size, larger sizes and no flash attention costing more
	big := params.Clone()
	big["width"], big["height"], big["video_frames"] = int64(1920), int64(1088), int64(81)
	if sdOverhead(&estimate.Scope{Descriptor: d, Params: big, Model: estimateModel(d)}) <= sdOverhead(&estimate.Scope{Descriptor: d, Params: params, Model: estimateModel(d)}) {
		t.Fatal("a larger default size needs more compute memory")
	}
	noFA := params.Clone()
	noFA["diffusion_fa"] = false
	if sdOverhead(&estimate.Scope{Descriptor: d, Params: noFA, Model: estimateModel(d)}) <= sdOverhead(&estimate.Scope{Descriptor: d, Params: params, Model: estimateModel(d)}) {
		t.Fatal("attention without flash attention needs more compute memory")
	}
}

func TestSDCppMeasureAndProbes(t *testing.T) {
	rt := SDCpp{}
	m := rt.Measure([]string{
		"[INFO ] stable-diffusion.cpp:1234 - total params memory size = 6702.86MB (VRAM 6500.00MB, RAM 202.86MB): text_encoders 1595.65MB(VRAM), diffusion_model 4947.47MB(VRAM), vae 159.68MB(RAM), controlnet 0.00MB(VRAM), extensions 0.00MB(VRAM)",
		"[DEBUG] ggml_runner.cpp:280 - flux compute buffer size: 1234.50 MB(VRAM) on CUDA0 (peak across 1 segment)",
		"[DEBUG] ggml_runner.cpp:280 - vae compute buffer size: 512.00 MB(RAM) on CPU (peak across 1 segment)",
		"nothing here",
	})
	byKey := map[string]uint64{}
	for _, x := range m {
		byKey[x.GetKey()] = x.GetBytes()
	}
	if byKey["device.weights"] != 6500<<20 || byKey["host.weights"] != megabytes(202.86) || byKey["device.compute"] != megabytes(1234.5) || byKey["host.compute"] != 512<<20 || len(m) != 4 {
		t.Fatalf("measurements %v", byKey)
	}
	version := rt.Probes()[0]
	if v, ok := version.Parse("stable-diffusion.cpp version unknown, commit cc515a0\n"); !ok || v != "cc515a0" {
		t.Fatalf("commit %q %v", v, ok)
	}
	if v, ok := version.Parse("stable-diffusion.cpp version 1.2.0, commit abc\n"); !ok || v != "1.2.0" {
		t.Fatalf("version %q %v", v, ok)
	}
	devices := rt.Probes()[1]
	if v, ok := devices.Parse("load_backend: loaded CPU backend\nCUDA0\tNVIDIA GeForce RTX 4090\nCPU\t12th Gen Intel\n"); !ok || v != "CUDA0,CPU" {
		t.Fatalf("devices %q %v", v, ok)
	}
	if rt.Health().Path != "/sdcpp/v1/capabilities" || rt.Kind() != v1.ModelKind_MODEL_KIND_DIFFUSION || rt.API() != v1.ApiFlavor_API_FLAVOR_SDCPP {
		t.Fatal("identity")
	}
	release, err := MethodOf(rt, "release")
	if err != nil {
		t.Fatal(err)
	}
	assets := map[string]string{
		"linux-rocm":     "sd-master-cc515a0-bin-Linux-Ubuntu-24.04-x86_64-rocm-7.14.0.zip",
		"linux-vulkan":   "sd-master-cc515a0-bin-Linux-Ubuntu-24.04-x86_64-vulkan.zip",
		"linux-cpu":      "sd-master-cc515a0-bin-Linux-Ubuntu-24.04-x86_64.zip",
		"macos-arm64":    "sd-master-cc515a0-bin-Darwin-macOS-26.6.2-arm64.zip",
		"windows-cuda12": "sd-master-cc515a0-bin-win-cuda12-x64.zip",
		"windows-rocm":   "sd-master-cc515a0-bin-win-rocm-7.14.0-x64.zip",
		"windows-vulkan": "sd-master-cc515a0-bin-win-vulkan-x64.zip",
		"windows-cpu":    "sd-master-cc515a0-bin-win-cpu-x64.zip",
	}
	for id, name := range assets {
		rule, err := release.Rule(id)
		if err != nil {
			t.Fatal(err)
		}
		if !rule.Assets[0].Matches(name) {
			t.Errorf("%s should match %s", id, name)
		}
		for other, otherName := range assets {
			if other != id && rule.Assets[0].Matches(otherName) {
				t.Errorf("%s must not match %s", id, otherName)
			}
		}
	}
}

func TestRuntimeBits(t *testing.T) {
	r := registry(t)
	seen := map[uint32]bool{}
	for _, rt := range r.List() {
		bit := r.Bit(rt.ID())
		if bit == 0 || seen[bit] {
			t.Fatalf("%s bit %d", rt.ID(), bit)
		}
		seen[bit] = true
	}
	if m := r.Mask("gguf", v1.ModelKind_MODEL_KIND_LANGUAGE); m != r.Bit("llamacpp") {
		t.Fatalf("gguf language %b", m)
	}
	if m := r.Mask("gguf", v1.ModelKind_MODEL_KIND_DIFFUSION); m != r.Bit("sdcpp") {
		t.Fatalf("gguf diffusion %b", m)
	}
	if m := r.MaskOf([]string{"safetensors", "gguf"}, v1.ModelKind_MODEL_KIND_LANGUAGE); m != r.Bit("llamacpp")|r.Bit("vllm")|r.Bit("sglang") {
		t.Fatalf("safetensors and gguf language %b", m)
	}
	if m := r.Mask("safetensors", v1.ModelKind_MODEL_KIND_COMPONENT); m != 0 {
		t.Fatalf("a component runs nowhere: %b", m)
	}
	if named := r.Named(r.Bit("sdcpp") | r.Bit("llamacpp")); len(named) != 2 || named[0].ID() != "llamacpp" || named[1].ID() != "sdcpp" {
		t.Fatalf("named %v", named)
	}
	if r.Bit("nope") != 0 {
		t.Fatal("an unknown runtime has no bit")
	}
}

// Sharded companions load through their index and count every shard's bytes. Shards without an
// index cannot load, and the refusal says so instead of loading one shard.
func TestSDCppShardedCompanions(t *testing.T) {
	rt := SDCpp{}
	params, _ := Resolve(rt, nil)
	file := func(p string, role v1.ArtifactRole, size uint64) *v1.StoredArtifact {
		return &v1.StoredArtifact{Artifact: &v1.Artifact{Path: p, Role: role, SizeBytes: size}, Path: "/store/" + p}
	}
	vae := &v1.StoredModel{SourceId: "hf", Repo: "Comfy-Org/Wan_2.2", Group: "wan_2.1_vae", Bytes: 3, Descriptor_: &v1.Descriptor{Architecture: "vae", Kind: v1.ModelKind_MODEL_KIND_COMPONENT}, Artifacts: []*v1.StoredArtifact{
		file("vae/config.json", v1.ArtifactRole_ARTIFACT_ROLE_CONFIG, 1<<10),
		file("vae/diffusion_pytorch_model-00002-of-00002.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, 1<<20),
		file("vae/diffusion_pytorch_model-00001-of-00002.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, 3<<20),
		file("vae/diffusion_pytorch_model.safetensors.index.json", v1.ArtifactRole_ARTIFACT_ROLE_INDEX, 1<<10),
	}}
	t5 := stored("city96/umt5-xxl-encoder-gguf", "umt5-xxl-encoder-Q8_0", "t5", v1.ModelKind_MODEL_KIND_COMPONENT, "umt5-xxl-encoder-Q8_0.gguf")
	s := &estimate.Scope{Descriptor: sdDescriptor("wan", false, false), Params: params.Clone(), Companions: []*v1.StoredModel{vae, t5}, Repo: "Comfy-Org/Wan_2.2"}
	rt.Policy().Solve(s)
	if s.Params.Str("vae") != "/store/vae/diffusion_pytorch_model.safetensors.index.json" {
		t.Fatalf("vae %q", s.Params.Str("vae"))
	}
	var planned uint64
	for _, g := range s.Descriptor.GetGroups() {
		if g.GetId() == "vae" {
			planned = g.GetBytes()
		}
	}
	if planned != 4<<20 {
		t.Fatalf("the plan counts every shard, got %d", planned)
	}
	if _, refusal := rt.Policy().States(s); refusal != "" {
		t.Fatalf("sharded parts with an index run: %s", refusal)
	}
	if p, err := companionWeights(vae); err != nil || p != "/store/vae/diffusion_pytorch_model.safetensors.index.json" {
		t.Fatalf("weights %q %v", p, err)
	}
	// Without the index the shards cannot load.
	bare := proto.Clone(vae).(*v1.StoredModel)
	bare.Artifacts = bare.Artifacts[:3]
	s = &estimate.Scope{Descriptor: sdDescriptor("wan", false, false), Params: params.Clone(), Companions: []*v1.StoredModel{bare, t5}, Repo: "Comfy-Org/Wan_2.2"}
	rt.Policy().Solve(s)
	if !s.Params.IsAuto("vae") {
		t.Fatalf("no shard loads alone, got %q", s.Params.Str("vae"))
	}
	_, refusal := rt.Policy().States(s)
	if !strings.Contains(refusal, "vae (") || !strings.Contains(refusal, "Comfy-Org/Wan_2.2/wan_2.1_vae is split into 2 shards and holds no index") {
		t.Fatalf("refusal %q", refusal)
	}
	// Two whole files are two models, not shards.
	pair := proto.Clone(vae).(*v1.StoredModel)
	pair.Artifacts[1].Artifact.Path, pair.Artifacts[1].Path = "vae/wan_2.1_vae.safetensors", "/store/vae/wan_2.1_vae.safetensors"
	pair.Artifacts[2].Artifact.Path, pair.Artifacts[2].Path = "vae/wan_2.1_vae.ckpt", "/store/vae/wan_2.1_vae.ckpt"
	if _, err := companionWeights(pair); err == nil || !strings.Contains(err.Error(), "holds 2 weight files, wan_2.1_vae.ckpt, wan_2.1_vae.safetensors") {
		t.Fatalf("pair %v", err)
	}
	if n := companionBytes(blueprint.SlotVAE, pair); n != pair.GetBytes() {
		t.Fatalf("unloadable groups count their stored size, got %d", n)
	}
	// Sharded adapters cannot lay out as one link.
	lora := &v1.StoredModel{Repo: "x/loras", Group: "split", Descriptor_: &v1.Descriptor{Architecture: "lora", Kind: v1.ModelKind_MODEL_KIND_COMPONENT}, Artifacts: []*v1.StoredArtifact{
		file("split-00001-of-00002.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, 1),
		file("split-00002-of-00002.safetensors", v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, 1),
	}}
	if _, err := linkDir(t.TempDir(), "loras", []*v1.StoredModel{lora}, "lora"); err == nil || !strings.Contains(err.Error(), "x/loras/split is split into 2 shards") {
		t.Fatalf("link %v", err)
	}
}

// A denoiser whose tensor names identify no family launches anyway, the plan naming where the
// layout stable-diffusion.cpp loads is published.
func TestSDCppUndetected(t *testing.T) {
	rt := SDCpp{}
	params, _ := Resolve(rt, nil)
	d := sdDescriptor("minimax_h3", false, true)
	d.Group = "transformer"
	d.Metadata[diffusion.KeyDetected] = "false"
	_, refusal := rt.Policy().States(&estimate.Scope{Descriptor: d, Params: params.Clone(), Repo: "FastVideo/FastVideo-FastH3-8-Step-V2"})
	for _, want := range []string{"transformer's tensor names are not the MiniMax-H3 layout", "Comfy-Org/MiniMax-H3 or leejet/MiniMax-H3-GGUF", "sd-server will try it"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal %q lacks %q", refusal, want)
		}
	}
	if _, err := rt.Launch(Launch{Name: "h3", Params: params, Artifacts: map[string]string{"weights": "/w.safetensors", "prepared_dir": t.TempDir()}, Install: Install{Path: "/bin/sd-server"}, Descriptor: d}); err != nil {
		t.Fatalf("launch %v", err)
	}
	d.Metadata[diffusion.KeyDetected] = "true"
	if _, refusal := rt.Policy().States(&estimate.Scope{Descriptor: d, Params: params.Clone()}); strings.Contains(refusal, "tensor names") {
		t.Fatalf("identified names pass to the missing parts check: %q", refusal)
	}
	if _, refusal := rt.Policy().States(&estimate.Scope{Descriptor: sdDescriptor("wan", false, false), Params: params.Clone()}); strings.Contains(refusal, "tensor names") {
		t.Fatalf("no record refuses nothing: %q", refusal)
	}
}

// The pipeline's scheduler config sets the flow shift over the family default.
func TestSDCppFlowShift(t *testing.T) {
	rt := SDCpp{}
	params, _ := Resolve(rt, nil)
	d := sdDescriptor("minimax_h3", false, true)
	s := &estimate.Scope{Descriptor: d, Params: params.Clone(), Repo: "FastVideo/FastVideo-FastH3-8-Step-V2"}
	rt.Policy().Solve(s)
	if s.Params.Float("flow_shift") != 12 || s.Params.Int("steps") != 8 || s.Params.Float("cfg_scale") != 1 {
		t.Fatalf("family defaults: shift %v steps %v cfg %v", s.Params.Float("flow_shift"), s.Params.Int("steps"), s.Params.Float("cfg_scale"))
	}
	d.Metadata[diffusion.KeyFlowShift] = "10.0"
	s = &estimate.Scope{Descriptor: d, Params: params.Clone(), Repo: "FastVideo/FastVideo-FastH3-8-Step-V2"}
	rt.Policy().Solve(s)
	if s.Params.Float("flow_shift") != 10 {
		t.Fatalf("scheduler shift %v", s.Params.Float("flow_shift"))
	}
	s.Params["flow_shift"] = 7.0
	rt.Policy().Solve(s)
	if s.Params.Float("flow_shift") != 7 {
		t.Fatal("an explicit shift stays")
	}
}
