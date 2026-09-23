package sources

import (
	"fmt"
	"slices"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

// Shared filters. The daemon turns a runtime into the formats and kind it serves, providers apply
// formats and kinds through their own APIs where those offer a filter, and the daemon keeps only
// the hits that match, fetching further pages until a page is full.
const (
	FacetFormat  = "format"
	FacetRuntime = "runtime"
	// Model kinds by name: language, diffusion, or component
	FacetKind = "kind"
)

// Facets shared across providers.
var SharedFacets = []string{FacetFormat, FacetRuntime, FacetKind}

// KindName names a kind the way the kind facet spells it.
func KindName(kind v1.ModelKind) string { return text.Enum(kind) }

// Kinds returns the model kinds a request asks for, none when it names none.
func Kinds(req *v1.SearchRequest) ([]v1.ModelKind, error) {
	var out []v1.ModelKind
	for _, name := range Filter(req, FacetKind) {
		n, ok := v1.ModelKind_value["MODEL_KIND_"+strings.ToUpper(name)]
		if !ok || n == 0 {
			return nil, fmt.Errorf("%w: no model kind %q, one of language, diffusion, or component", ErrSource, name)
		}
		if !slices.Contains(out, v1.ModelKind(n)) {
			out = append(out, v1.ModelKind(n))
		}
	}
	return out, nil
}

// AdmitsKind reports whether the request asks for no kind or for this one.
func AdmitsKind(req *v1.SearchRequest, kind v1.ModelKind) bool {
	kinds, err := Kinds(req)
	return err == nil && (len(kinds) == 0 || slices.Contains(kinds, kind))
}

// AdmitsFormats reports whether the request asks for no format or for one the model holds.
func AdmitsFormats(req *v1.SearchRequest, have []string) bool {
	want := Filter(req, FacetFormat)
	if len(want) == 0 {
		return true
	}
	for _, f := range have {
		if slices.Contains(want, f) {
			return true
		}
	}
	return false
}

// Whether a facet is one of the shared ones
func Shared(facet string) bool {
	for _, f := range SharedFacets {
		if f == facet {
			return true
		}
	}
	return false
}

// Tasks identifying image and video diffusion models.
var diffusionTasks = map[string]bool{
	"text-to-image": true, "image-to-image": true, "text-to-video": true, "image-to-video": true, "unconditional-image-generation": true, "image-to-3d": true, "text-to-3d": true, "video-to-video": true,
}

// Tasks identifying language models for chat, completion, and embeddings.
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

// Kind infers model kind from catalog task, library, and tags. Insufficient metadata returns
// unspecified.
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
	// Default unclassified GGUF entries to language models, including Ollama entries.
	for _, f := range h.GetFormats() {
		if f == "gguf" {
			return v1.ModelKind_MODEL_KIND_LANGUAGE
		}
	}
	return v1.ModelKind_MODEL_KIND_UNSPECIFIED
}

// Formats infers format IDs from catalog tags and library. Diffusion safetensors also match the
// standalone diffusion format.
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
