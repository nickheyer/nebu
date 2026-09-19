package blueprint

import (
	"math"
	"path"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Candidate is a stored or listed weight group to match against a slot.
type Candidate struct {
	Repo, Group string
	// Repository paths of the group files.
	Paths []string
	// Artifact roles available beside the weights.
	Roles map[v1.ArtifactRole]bool
	// Parsed headers, or nil if unread.
	Descriptor *v1.Descriptor
}

// Target identifies the model that needs a component.
type Target struct {
	Repo   string
	Group  string
	Family *Family
	Bits   uint32
}

func lower(s string) string              { return strings.ToLower(s) }
func indexOf(s, sub string) int          { return strings.Index(s, sub) }
func isDigit(c byte) bool                { return c >= '0' && c <= '9' }
func isAlpha(c byte) bool                { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isAlnum(c byte) bool                { return isDigit(c) || isAlpha(c) }
func sameClass(a, b byte) bool           { return isDigit(a) == isDigit(b) }
func within(v, want, ratio float64) bool { return v >= want/ratio && v <= want*ratio }

// Tokens splits a name into lowercase letter and digit runs. Both wan2.1_vae and Wan_2_1_VAE become
// wan 2 1 vae.
func Tokens(s string) []string {
	var out []string
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isAlnum(c) {
			flush()
			continue
		}
		if len(cur) > 0 && !sameClass(cur[len(cur)-1], c) {
			flush()
		}
		if isAlpha(c) {
			c += 'a' - 'A'
			if c > 'z' {
				c -= 'a' - 'A'
			}
		}
		cur = append(cur, c)
	}
	flush()
	return out
}

// Reports whether the name matches consecutive tokens.
func hasRun(tokens []string, name string) bool {
	want := Tokens(name)
	if len(want) == 0 {
		return false
	}
	for i := 0; i+len(want) <= len(tokens); i++ {
		match := true
		for j := range want {
			if tokens[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// Combines repository, group, and file names for matching.
func (c Candidate) words() []string {
	parts := []string{c.Repo, c.Group}
	for _, p := range c.Paths {
		parts = append(parts, path.Base(p))
	}
	return Tokens(strings.Join(parts, "/"))
}

// Named reports whether the candidate matches an accepted component name.
func (p Part) Named(c Candidate) bool {
	words := c.words()
	for _, n := range p.Names {
		if hasRun(words, n) {
			return true
		}
	}
	return false
}

// Architecture names that identify only a component kind.
var genericArch = map[string]bool{"llm": true, "t5": true, "text_encoder": true, "vae": true, "clip_l": true, "clip_g": true, "clip_h": true, "clip_vision": true, "audio_encoder": true, "audio_vae": true, "taesd": true, "embeddings_connectors": true, "tokenizer": true, "diffusion": true, "projector": true, "": true}

// Reports whether a specific architecture matches the component.
func (p Part) archNamed(c Candidate) bool {
	arch := lower(c.Descriptor.GetArchitecture())
	if genericArch[diffusion.Canonical(arch)] {
		return false
	}
	for _, a := range p.Arch {
		if strings.Contains(arch, lower(a)) {
			return true
		}
	}
	return false
}

// Reports whether a specific architecture conflicts with the component.
func (p Part) archOther(c Candidate) bool {
	arch := lower(c.Descriptor.GetArchitecture())
	return len(p.Arch) > 0 && !genericArch[diffusion.Canonical(arch)] && !p.archNamed(c)
}

// KindOf returns the component kind, llm for language models, or diffusion for denoisers. Unread
// headers return empty.
func KindOf(d *v1.Descriptor) string {
	switch d.GetKind() {
	case v1.ModelKind_MODEL_KIND_COMPONENT:
		return diffusion.PartOf(d)
	case v1.ModelKind_MODEL_KIND_LANGUAGE:
		return "llm"
	case v1.ModelKind_MODEL_KIND_DIFFUSION:
		return "diffusion"
	}
	return ""
}

// Reports whether the candidate has a projector file or vision tensors.
func hasVision(c Candidate) bool {
	if c.Roles[v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR] {
		return true
	}
	for _, g := range c.Descriptor.GetGroups() {
		if g.GetKind() == v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION {
			return true
		}
	}
	return false
}

// Reports whether the candidate has tokenizer.json.
func hasTokenizer(c Candidate) bool {
	for _, p := range c.Paths {
		if path.Base(p) == "tokenizer.json" {
			return true
		}
	}
	return false
}

// Fits matches component kind, header constraints, and identity from the name, architecture, or
// source repository.
func Fits(f Fill, t Target, c Candidate) bool {
	d := c.Descriptor
	kind := KindOf(d)
	switch f.Kind {
	case "diffusion":
		if kind != "diffusion" || t.Family == nil || diffusion.Canonical(diffusion.ProfileOf(d).Family) != t.Family.ID {
			return false
		}
		g := lower(c.Group)
		switch f.Slot {
		case SlotDenoiserHighNoise:
			return g == HighOf(t.Group)
		case SlotDenoiserUncond:
			return strings.Contains(g, "uncond") && !strings.Contains(lower(t.Group), "uncond")
		case SlotRefiner:
			return strings.Contains(g, "refiner") && !strings.Contains(lower(t.Group), "refiner")
		}
		return f.Named(c) && !(c.Repo == t.Repo && c.Group == t.Group)
	case "projector":
		if kind != "llm" || !hasVision(c) {
			return false
		}
	case "tokenizer":
		if !hasTokenizer(c) {
			return false
		}
		if kind != "llm" && kind != "tokenizer" {
			return false
		}
	case "weights", "config":
		return false
	default:
		if kind != f.Kind {
			return false
		}
	}
	if d != nil {
		params := formatsParams(d)
		if len(f.Params) > 0 && d.GetParameterCount() > 0 {
			ok := false
			for _, want := range f.Params {
				if within(float64(d.GetParameterCount()), want, 1.5) {
					ok = true
				}
			}
			if !ok {
				return false
			}
		}
		if f.Vocab > 0 && params["n_vocab"] > 0 && !within(params["n_vocab"], f.Vocab, 1.01) {
			return false
		}
		if f.Width > 0 && params["n_embd"] > 0 && math.Abs(params["n_embd"]-f.Width) > 0.5 {
			return false
		}
		if f.Channels > 0 {
			if ch := params[diffusion.KeyLatentChannels]; ch > 0 && math.Abs(ch-f.Channels) > 0.5 {
				return false
			}
		}
		if v, known := d.GetMetadata()[diffusion.KeyVideoVAE]; known && f.Kind == "vae" && (v == "true") != f.Video {
			return false
		}
		if f.archOther(c) && !f.Named(c) {
			return false
		}
	}
	if len(f.Names) == 0 && len(f.Arch) == 0 {
		return true
	}
	return f.Named(c) || f.archNamed(c) || Provenance(f, t, c.Repo)
}

// Provenance matches the target repository or a source listed for the family or component.
func Provenance(f Fill, t Target, repo string) bool {
	if repo == "" {
		return false
	}
	if repo == t.Repo {
		return true
	}
	for _, r := range f.Repos {
		if r == repo {
			return true
		}
	}
	if t.Family != nil {
		for _, r := range t.Family.Canonical {
			if r == repo {
				return true
			}
		}
	}
	return false
}

// Descriptor parameters, including latent channels from diffusion metadata.
func formatsParams(d *v1.Descriptor) map[string]float64 {
	out := map[string]float64{}
	for k, v := range d.GetParams() {
		out[k] = v
	}
	if s := d.GetMetadata()[diffusion.KeyLatentChannels]; s != "" {
		var n float64
		for _, c := range s {
			if c < '0' || c > '9' {
				n = 0
				break
			}
			n = n*10 + float64(c-'0')
		}
		out[diffusion.KeyLatentChannels] = n
	}
	return out
}

// Rank prefers the target repository, blueprint sources, name matches, then architecture matches.
// Lower ranks come first.
func Rank(f Fill, t Target, c Candidate) int {
	switch {
	case c.Repo == t.Repo:
		return 0
	case Provenance(f, t, c.Repo):
		return 1
	case f.Named(c):
		return 2
	}
	return 3
}

// Of returns the diffusion or language blueprint, or nil for standalone components.
func Of(d *v1.Descriptor) *Family {
	switch d.GetKind() {
	case v1.ModelKind_MODEL_KIND_DIFFUSION:
		return Get(diffusion.Canonical(diffusion.FamilyOf(d)))
	case v1.ModelKind_MODEL_KIND_LANGUAGE:
		return Get(Language)
	}
	return nil
}
