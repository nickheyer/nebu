package sources

import (
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The facets the daemon answers over every source, whatever provider is behind it: the format a model
// is held in, and the runtime it runs on. A provider narrows to them when its API can and the daemon
// keeps only the hits that match, so a page never carries a model the filter rules out.
const (
	FacetFormat  = "format"
	FacetRuntime = "runtime"
)

// The facets every source answers, kept when a search fans out across providers
var SharedFacets = []string{FacetFormat, FacetRuntime}

// Whether a facet is one of the shared ones
func Shared(facet string) bool {
	for _, f := range SharedFacets {
		if f == facet {
			return true
		}
	}
	return false
}

// Tasks a hub publishes under that mean a diffusion model, one that makes images or video
var diffusionTasks = map[string]bool{
	"text-to-image": true, "image-to-image": true, "text-to-video": true, "image-to-video": true, "unconditional-image-generation": true, "image-to-3d": true, "text-to-3d": true, "video-to-video": true,
}

// Tasks that mean a language model, served for chat, completion, or embeddings
var languageTasks = map[string]bool{
	"text-generation": true, "text2text-generation": true, "fill-mask": true, "feature-extraction": true, "sentence-similarity": true, "question-answering": true, "summarization": true, "translation": true,
	"conversational": true, "image-text-to-text": true, "any-to-any": true, "visual-question-answering": true, "document-question-answering": true, "text-classification": true, "token-classification": true,
	"zero-shot-classification": true, "table-question-answering": true, "text-ranking": true, "audio-text-to-text": true, "video-text-to-text": true,
}

// Civitai's model types, by what each is to a runtime
var civitaiKinds = map[string]v1.ModelKind{
	"Checkpoint": v1.ModelKind_MODEL_KIND_DIFFUSION, "UNet": v1.ModelKind_MODEL_KIND_DIFFUSION, "MotionModule": v1.ModelKind_MODEL_KIND_COMPONENT,
	"LORA": v1.ModelKind_MODEL_KIND_COMPONENT, "LoCon": v1.ModelKind_MODEL_KIND_COMPONENT, "DoRA": v1.ModelKind_MODEL_KIND_COMPONENT, "VAE": v1.ModelKind_MODEL_KIND_COMPONENT, "TextEncoder": v1.ModelKind_MODEL_KIND_COMPONENT,
	"Controlnet": v1.ModelKind_MODEL_KIND_COMPONENT, "Upscaler": v1.ModelKind_MODEL_KIND_COMPONENT, "CLIPVision": v1.ModelKind_MODEL_KIND_COMPONENT, "CLIP": v1.ModelKind_MODEL_KIND_COMPONENT,
	"TextualInversion": v1.ModelKind_MODEL_KIND_COMPONENT, "Hypernetwork": v1.ModelKind_MODEL_KIND_COMPONENT, "AestheticGradient": v1.ModelKind_MODEL_KIND_COMPONENT,
	"LLM": v1.ModelKind_MODEL_KIND_LANGUAGE, "VisionLanguage": v1.ModelKind_MODEL_KIND_LANGUAGE,
}

// Kind says what a catalog hit is from what its source published: the task it is listed under, the
// library it was made with, and its tags; unspecified when the source says too little to tell
func Kind(h *v1.SearchHit) v1.ModelKind {
	task := strings.ToLower(strings.TrimSpace(h.GetTask()))
	if k, ok := civitaiKinds[strings.TrimSpace(h.GetTask())]; ok {
		return k
	}
	switch {
	case diffusionTasks[task]:
		return v1.ModelKind_MODEL_KIND_DIFFUSION
	case languageTasks[task]:
		return v1.ModelKind_MODEL_KIND_LANGUAGE
	}
	library := strings.ToLower(h.GetLibrary())
	tags := map[string]bool{}
	adapter := false
	for _, t := range h.GetTags() {
		t = strings.ToLower(t)
		tags[t] = true
		if strings.HasPrefix(t, "base_model:adapter:") {
			adapter = true
		}
	}
	switch {
	case adapter || tags["lora"] || tags["textual-inversion"] || tags["controlnet"]:
		return v1.ModelKind_MODEL_KIND_COMPONENT
	case library == "diffusers" || tags["diffusers"] || tags["stable-diffusion"] || tags["text-to-image"] || tags["text-to-video"] || tags["image-to-video"] || tags["stable-diffusion-xl"] || tags["flux"]:
		return v1.ModelKind_MODEL_KIND_DIFFUSION
	case library == "transformers" || library == "gguf" || library == "vllm" || library == "mlx" || library == "sentence-transformers" || library == "nemo" || library == "llama.cpp":
		return v1.ModelKind_MODEL_KIND_LANGUAGE
	}
	for _, t := range h.GetTags() {
		if languageTasks[strings.ToLower(t)] {
			return v1.ModelKind_MODEL_KIND_LANGUAGE
		}
		if diffusionTasks[strings.ToLower(t)] {
			return v1.ModelKind_MODEL_KIND_DIFFUSION
		}
	}
	// A GGUF with nothing else said is a language model, the format's overwhelming use, and Ollama publishes nothing else
	for _, f := range h.GetFormats() {
		if f == "gguf" {
			return v1.ModelKind_MODEL_KIND_LANGUAGE
		}
	}
	return v1.ModelKind_MODEL_KIND_UNSPECIFIED
}

// Formats names the formats a hit is held in from its tags and library, against the format ids the daemon
// reads: a safetensors checkpoint of a diffusion model or one of its parts is the single file diffusion
// format as well, since a hub tags a lone checkpoint and a diffusers tree alike
func Formats(h *v1.SearchHit, ids []string, kind v1.ModelKind) []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, f := range h.GetFormats() {
		add(f)
	}
	for _, id := range ids {
		for _, t := range append([]string{h.GetLibrary()}, h.GetTags()...) {
			if strings.EqualFold(t, id) {
				add(id)
				break
			}
		}
	}
	if (kind == v1.ModelKind_MODEL_KIND_DIFFUSION || kind == v1.ModelKind_MODEL_KIND_COMPONENT) && seen["safetensors"] && hasID(ids, "diffusion") {
		add("diffusion")
	}
	return out
}

func hasID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
