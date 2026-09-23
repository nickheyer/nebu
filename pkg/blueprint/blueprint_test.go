package blueprint

import (
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestTableIsClosed(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Families() {
		if seen[f.ID] {
			t.Errorf("%s listed twice", f.ID)
		}
		seen[f.ID] = true
		if f.ID != Language && !diffusion.Denoiser(f.ID) {
			t.Errorf("%s is not a recognized denoiser family", f.ID)
		}
		if f.Name == "" || f.Pipeline == "" || f.Makes == "" {
			t.Errorf("%s is missing a name, pipeline, or output description", f.ID)
		}
		for _, fill := range f.Fills {
			if !IsSlot(fill.Slot) {
				t.Errorf("%s references unknown slot %q", f.ID, fill.Slot)
			}
			switch fill.Kind {
			case "weights", "config", "projector", "tokenizer", "diffusion":
			default:
				if !diffusion.Component(fill.Kind) {
					t.Errorf("%s %s uses unknown component kind %q", f.ID, fill.Slot, fill.Kind)
				}
			}
			if fill.Name == "" {
				t.Errorf("%s %s has no component label", f.ID, fill.Slot)
			}
			for _, r := range fill.Repos {
				if strings.Count(r, "/") != 1 {
					t.Errorf("%s %s repository %q is not owner/name", f.ID, fill.Slot, r)
				}
			}
		}
		for _, r := range f.Canonical {
			if strings.Count(r, "/") != 1 {
				t.Errorf("%s canonical %q is not owner/name", f.ID, r)
			}
		}
	}
	for _, id := range diffusion.Families() {
		if !seen[id] {
			t.Errorf("recognized family %s has no blueprint", id)
		}
	}
	for _, s := range Slots() {
		if Label(s) == "" || !IsSlot(s) {
			t.Errorf("%s has no label", s)
		}
	}
	if Label("nope") != "nope" || IsSlot("nope") {
		t.Fatal("unknown slot must retain its ID label and return false")
	}
}

func slots(fills []Fill) string {
	var out []string
	for _, f := range fills {
		s := f.Slot
		if !f.Required {
			s += "?"
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func TestNeeds(t *testing.T) {
	if got := slots(Needs(diffusion.Profile{Family: "flux"}, "flux1-dev")); got != "vae,text_encoder.clip_l,text_encoder.t5" {
		t.Fatalf("flux %s", got)
	}
	if got := slots(Needs(diffusion.Profile{Family: "wan", ImageInput: true}, "wan2.1_i2v_480p_14B")); got != "vae,text_encoder.t5,image_encoder.clip_vision" {
		t.Fatalf("wan i2v %s", got)
	}
	if got := slots(Needs(diffusion.Profile{Family: "wan", Variant: "s2v", ImageInput: true, AudioInput: true}, "wan2.2_s2v_14B")); got != "vae,text_encoder.t5,audio_encoder" {
		t.Fatalf("wan s2v %s", got)
	}
	if got := slots(Needs(diffusion.Profile{Family: "wan"}, "wan2.2_t2v_low_noise_14B_fp8")); got != "vae,text_encoder.t5,denoiser.high_noise?" {
		t.Fatalf("wan low noise %s", got)
	}
	if got := Needs(diffusion.Profile{Family: "wan", Variant: "ti2v"}, "wan2.2_ti2v_5B")[0]; got.Channels != 48 {
		t.Fatalf("wan ti2v requires the Wan 2.2 VAE, got %+v", got.Part)
	}
	if got := slots(Needs(diffusion.Profile{Family: "sdxl", VAE: true, TextEncoder: true}, "sd_xl_base_1.0")); got != "" {
		t.Fatalf("bundled sdxl %s", got)
	}
	// Declared pipelines need no bundled slots, but Fills still lists them.
	declared := diffusion.Profile{Family: "minimax_h3", VAE: true, TextEncoder: true, Slots: map[string]bool{SlotVAE: true, SlotVAEAudio: true, SlotTextEncoderLLM: true, SlotTextEncoderVision: true}}
	if got := slots(Needs(declared, "transformer")); got != "" {
		t.Fatalf("declared minimax %s", got)
	}
	if got := slots(Fills(declared, "transformer")); got != "vae,vae.audio?,text_encoder.llm,text_encoder.llm.vision?" {
		t.Fatalf("fills of minimax %s", got)
	}
	if got := slots(Needs(diffusion.Profile{Family: "ltx2"}, "ltx-2.3")); got != "vae,vae.audio?,text_encoder.llm,connector?" {
		t.Fatalf("ltx2 %s", got)
	}
	if got := slots(Needs(diffusion.Profile{Family: "FluxTransformer2DModel"}, "transformer")); got != "vae,text_encoder.clip_l,text_encoder.t5" {
		t.Fatalf("diffusers class family mapping: %s", got)
	}
	if got := Needs(diffusion.Profile{Family: "nope"}, "x"); got != nil {
		t.Fatalf("unknown family must return no needs: %v", got)
	}
	if got := slots(Get(Language).Fills); got != "weights,config,tokenizer?,projector?" {
		t.Fatalf("language %s", got)
	}
}

func component(repo, group, arch string, paths ...string) Candidate {
	return Candidate{Repo: repo, Group: group, Paths: paths, Roles: map[v1.ArtifactRole]bool{}, Descriptor: &v1.Descriptor{Architecture: arch, Kind: v1.ModelKind_MODEL_KIND_COMPONENT, Params: map[string]float64{}, Metadata: map[string]string{}}}
}

func language(repo, group, arch string, params uint64, paths ...string) Candidate {
	c := Candidate{Repo: repo, Group: group, Paths: paths, Roles: map[v1.ArtifactRole]bool{}, Descriptor: &v1.Descriptor{Architecture: arch, Kind: v1.ModelKind_MODEL_KIND_LANGUAGE, ParameterCount: params, Params: map[string]float64{}, Metadata: map[string]string{}}}
	for _, p := range paths {
		if strings.HasPrefix(p, "mmproj") {
			c.Roles[v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR] = true
		}
	}
	return c
}

func fillOf(family, slot string, p diffusion.Profile, group string) (Fill, Target) {
	for _, f := range Needs(p, group) {
		if f.Slot == slot {
			return f, Target{Repo: "Comfy-Org/Wan_2.2_ComfyUI_Repackaged", Group: group, Family: Get(family)}
		}
	}
	panic(slot)
}

func TestFits(t *testing.T) {
	wan := diffusion.Profile{Family: "wan"}
	t5, target := fillOf("wan", SlotTextEncoderT5, wan, "wan2.1_t2v_14B_fp8")
	umt5 := component("city96/umt5-xxl-encoder-gguf", "Q8_0", "t5", "umt5-xxl-encoder-Q8_0.gguf")
	umt5.Descriptor.Params["n_vocab"] = 256384
	if !Fits(t5, target, umt5) {
		t.Fatal("UMT5 encoder should match Wan's T5 slot")
	}
	t5xxl := component("comfyanonymous/flux_text_encoders", "t5xxl_fp16", "t5", "t5xxl_fp16.safetensors")
	t5xxl.Descriptor.Params["n_vocab"] = 32128
	if Fits(t5, target, t5xxl) {
		t.Fatal("T5-XXL must not match Wan's UMT5 slot")
	}
	nameless := component("someone/encoders", "text_encoder", "t5", "text_encoder/model.safetensors")
	if Fits(t5, target, nameless) {
		t.Fatal("component kind alone must not identify the encoder")
	}
	nameless.Descriptor.Params["n_vocab"] = 256384
	if Fits(t5, target, nameless) {
		t.Fatal("vocabulary size alone must not identify the encoder")
	}
	own := component("Comfy-Org/Wan_2.2_ComfyUI_Repackaged", "text_encoders", "t5", "split_files/text_encoders/encoder.safetensors")
	if !Fits(t5, target, own) {
		t.Fatal("matching repository component should be accepted")
	}
	fluxT5, fluxTarget := fillOf("flux", SlotTextEncoderT5, diffusion.Profile{Family: "flux"}, "flux1-dev")
	if !Fits(fluxT5, fluxTarget, t5xxl) || Fits(fluxT5, fluxTarget, umt5) {
		t.Fatal("FLUX should accept T5-XXL and reject UMT5")
	}
	vae, _ := fillOf("wan", SlotVAE, wan, "wan2.1_t2v_14B_fp8")
	wanVAE := component("Comfy-Org/Wan_2.1_ComfyUI_repackaged", "wan_2.1_vae", "vae", "split_files/vae/wan_2.1_vae.safetensors")
	wanVAE.Descriptor.Metadata[diffusion.KeyLatentChannels], wanVAE.Descriptor.Metadata[diffusion.KeyVideoVAE] = "16", "true"
	if !Fits(vae, target, wanVAE) {
		t.Fatal("Wan 2.1 VAE should match")
	}
	ae := component("black-forest-labs/FLUX.1-dev", "ae", "vae", "ae.safetensors")
	ae.Descriptor.Metadata[diffusion.KeyLatentChannels], ae.Descriptor.Metadata[diffusion.KeyVideoVAE] = "16", "false"
	if Fits(vae, target, ae) {
		t.Fatal("FLUX AE must not match a video VAE slot")
	}
	wan22VAE := component("Comfy-Org/Wan_2.2_ComfyUI_Repackaged", "wan2.2_vae", "vae", "split_files/vae/wan2.2_vae.safetensors")
	wan22VAE.Descriptor.Metadata[diffusion.KeyLatentChannels], wan22VAE.Descriptor.Metadata[diffusion.KeyVideoVAE] = "48", "true"
	if Fits(vae, target, wan22VAE) {
		t.Fatal("48-channel Wan 2.2 VAE must not match Wan 2.1")
	}
	fluxVAE, _ := fillOf("flux", SlotVAE, diffusion.Profile{Family: "flux"}, "flux1-dev")
	if !Fits(fluxVAE, fluxTarget, ae) || Fits(fluxVAE, fluxTarget, wanVAE) {
		t.Fatal("FLUX should accept its AE and reject Wan VAE")
	}
	// Match language encoders by architecture and size, and projectors to the vision slot.
	qwen := diffusion.Profile{Family: "qwen_image"}
	llm, qTarget := fillOf("qwen_image", SlotTextEncoderLLM, qwen, "qwen_image_fp8")
	qTarget.Repo = "Comfy-Org/Qwen-Image_ComfyUI"
	qwenVL := language("unsloth/Qwen2.5-VL-7B-Instruct-GGUF", "Q8_0", "qwen2vl", 8_300_000_000, "Qwen2.5-VL-7B-Instruct-Q8_0.gguf", "mmproj-F16.gguf")
	if !Fits(llm, qTarget, qwenVL) {
		t.Fatal("Qwen2.5-VL-7B should match Qwen Image's LLM slot")
	}
	mistral := language("unsloth/Mistral-Small-3.2-24B-Instruct-2506-GGUF", "Q4_K_M", "llama", 24_000_000_000, "Mistral-Small-3.2-24B-Instruct-2506-Q4_K_M.gguf")
	if Fits(llm, qTarget, mistral) {
		t.Fatal("Mistral Small must not match Qwen Image's LLM slot")
	}
	qwen3 := language("unsloth/Qwen3-4B-GGUF", "Q8_0", "qwen3", 4_000_000_000, "Qwen3-4B-Q8_0.gguf")
	if Fits(llm, qTarget, qwen3) {
		t.Fatal("Qwen3-4B must not match Qwen2.5-VL-7B")
	}
	tower, _ := fillOf("qwen_image", SlotTextEncoderVision, qwen, "qwen_image_fp8")
	if !Fits(tower, qTarget, qwenVL) {
		t.Fatal("Qwen2.5-VL projector should match the vision slot")
	}
	noTower := language("Comfy-Org/Qwen-Image_ComfyUI", "qwen_2.5_vl_7b_fp8_scaled", "llm", 7_600_000_000, "split_files/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors")
	noTower.Descriptor.Kind = v1.ModelKind_MODEL_KIND_COMPONENT
	if !Fits(llm, qTarget, noTower) || Fits(tower, qTarget, noTower) {
		t.Fatal("encoder without a vision tower should match only the LLM slot")
	}
	// Qwen-Image 2.1 takes its own 64-channel VAE and Qwen3-VL-8B, and neither fits Qwen-Image.
	qwen21 := diffusion.Profile{Family: "qwen_image21"}
	vae21, q21Target := fillOf("qwen_image21", SlotVAE, qwen21, "Q8_0")
	q21Target.Repo = "abenzerps/Qwen-Image-2.1-Uncensored-GGUF"
	vae21File := component("abenzerps/Qwen-Image-2.1-Uncensored-GGUF", "qwen_image_2.1_vae_bf16", "vae", "vae/qwen_image_2.1_vae_bf16.safetensors")
	vae21File.Descriptor.Metadata[diffusion.KeyLatentChannels], vae21File.Descriptor.Metadata[diffusion.KeyVideoVAE] = "64", "true"
	vae1File := component("Comfy-Org/Qwen-Image_ComfyUI", "qwen_image_vae", "vae", "split_files/vae/qwen_image_vae.safetensors")
	vae1File.Descriptor.Metadata[diffusion.KeyLatentChannels], vae1File.Descriptor.Metadata[diffusion.KeyVideoVAE] = "16", "true"
	if !Fits(vae21, q21Target, vae21File) || Fits(vae21, q21Target, vae1File) {
		t.Fatal("Qwen-Image 2.1 should accept its 64-channel VAE and reject the 16-channel Qwen-Image VAE")
	}
	vae1, _ := fillOf("qwen_image", SlotVAE, qwen, "qwen_image_fp8")
	if Fits(vae1, qTarget, vae21File) {
		t.Fatal("Qwen-Image must not take the 64-channel 2.1 VAE")
	}
	llm21, _ := fillOf("qwen_image21", SlotTextEncoderLLM, qwen21, "Q8_0")
	qwen3VL := language("abenzerps/Qwen-Image-2.1-Uncensored-GGUF", "qwen3vl_8b_bf16", "qwen3vl", 8_800_000_000, "text_encoders/qwen3vl_8b_bf16.safetensors")
	qwen3VL.Descriptor.Kind = v1.ModelKind_MODEL_KIND_COMPONENT
	qwen3VL.Descriptor.Architecture = "llm"
	if !Fits(llm21, q21Target, qwen3VL) || Fits(llm21, q21Target, qwenVL) {
		t.Fatal("Qwen-Image 2.1 should take Qwen3-VL-8B and not Qwen2.5-VL-7B")
	}
	fluxLLM, f2Target := fillOf("flux2", SlotTextEncoderLLM, diffusion.Profile{Family: "flux2"}, "flux2-dev")
	if !Fits(fluxLLM, f2Target, mistral) || Fits(fluxLLM, f2Target, qwenVL) {
		t.Fatal("FLUX.2 should match Mistral Small by name")
	}
	// Paired experts must differ only in their noise level.
	high, pairTarget := fillOf("wan", SlotDenoiserHighNoise, wan, "wan2.2_t2v_low_noise_14B_fp8")
	half := Candidate{Repo: "Comfy-Org/Wan_2.2_ComfyUI_Repackaged", Group: "wan2.2_t2v_high_noise_14B_fp8", Descriptor: &v1.Descriptor{Architecture: "wan", Kind: v1.ModelKind_MODEL_KIND_DIFFUSION, Metadata: map[string]string{diffusion.KeyFamily: "wan"}}}
	if !Fits(high, pairTarget, half) {
		t.Fatal("matching high noise expert should be accepted")
	}
	half.Group = "wan2.2_i2v_high_noise_14B_fp8"
	if Fits(high, pairTarget, half) {
		t.Fatal("expert from a different pair must be rejected")
	}
	// Tokenizer candidates must contain tokenizer.json.
	tok, lensTarget := fillOf("lens", SlotTokenizer, diffusion.Profile{Family: "lens"}, "lens_bf16")
	oss := language("openai/gpt-oss-20b", "default", "GptOssForCausalLM", 21_000_000_000, "model-00001-of-00003.safetensors", "tokenizer.json")
	if !Fits(tok, lensTarget, oss) {
		t.Fatal("GPT-OSS tokenizer.json should match Lens")
	}
	if Fits(tok, lensTarget, qwenVL) {
		t.Fatal("GGUF without tokenizer.json must not match")
	}
	if Rank(t5, target, own) != 0 || Rank(t5, target, umt5) != 1 || Rank(fluxT5, fluxTarget, umt5) >= 2 && Rank(t5, target, component("x/y", "umt5", "t5", "umt5.gguf")) != 2 {
		t.Fatal("ranks")
	}
}

func TestTokens(t *testing.T) {
	if got := strings.Join(Tokens("Comfy-Org/Wan_2.1_ComfyUI_repackaged/split_files/vae/wan_2.1_vae.safetensors"), " "); got != "comfy org wan 2 1 comfyui repackaged split files vae wan 2 1 vae safetensors" {
		t.Fatalf("tokens %q", got)
	}
	c := Candidate{Repo: "city96/umt5-xxl-encoder-gguf", Group: "Q8_0", Paths: []string{"umt5-xxl-encoder-Q8_0.gguf"}}
	if !umt5XXL.Named(c) || t5XXL.Named(c) {
		t.Fatal("candidate should match UMT5, not T5-XXL")
	}
	if HighOf("wan2.2_t2v_low_noise_14B_fp8") != "wan2.2_t2v_high_noise_14b_fp8" || HighOf("flux1-dev") != "" {
		t.Fatal("high of")
	}
	if got := Published(Get("chroma")); len(got) < 3 || got[0] != "lodestones/Chroma1-HD" {
		t.Fatalf("published %v", got)
	}
	if got := Where(Get("chroma"), Get("chroma").Fills[1]); len(got) == 0 || got[0] != "comfyanonymous/flux_text_encoders" {
		t.Fatalf("where %v", got)
	}
	if got := Where(Get("wan"), Fill{Part: Part{Kind: "vae"}}); len(got) == 0 || got[0] != "Wan-AI/Wan2.1-T2V-14B" {
		t.Fatalf("missing component sources should use canonical repositories: %v", got)
	}
	if Of(&v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_LANGUAGE}).ID != Language || Of(&v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_COMPONENT}) != nil || Of(&v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_DIFFUSION, Architecture: "wan"}).ID != "wan" {
		t.Fatal("of")
	}
}
