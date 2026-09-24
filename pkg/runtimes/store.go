package runtimes

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nickheyer/nebu/pkg/blueprint"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusers"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

// StoreScheme prefixes a path parameter value that names a stored group instead of a file, as
// store://source/repo#group. The daemon resolves it to the file the parameter loads.
const StoreScheme = "store://"

// StoreRef names a stored group for a path parameter.
func StoreRef(m *v1.StoredModel) string {
	return StoreScheme + m.GetSourceId() + "/" + m.GetRepo() + "#" + m.GetGroup()
}

// Finds the stored group a reference names.
func storeRef(models []*v1.StoredModel, ref string) (*v1.StoredModel, error) {
	rest, isRef := strings.CutPrefix(ref, StoreScheme)
	repoPath, group, hasGroup := strings.Cut(rest, "#")
	source, repo, hasRepo := strings.Cut(repoPath, "/")
	if !isRef || !hasGroup || !hasRepo || source == "" || repo == "" || group == "" {
		return nil, fmt.Errorf("%w: %q is not a store reference of the form store://source/repo#group", ErrParam, ref)
	}
	for _, m := range models {
		if m.GetSourceId() == source && m.GetRepo() == repo && m.GetGroup() == group {
			return m, nil
		}
	}
	return nil, fmt.Errorf("%w: %s %s is not in the store", ErrParam, repo, group)
}

// ResolveStore replaces store references in path parameters with the file or directory the
// parameter loads from the named group. Explicit paths inside the store are refused when they
// hold a different part than the parameter takes, or one shard of a group that loads through its
// index. Paths outside the store pass through.
func ResolveStore(rt Runtime, overrides map[string]string, models []*v1.StoredModel) (map[string]string, error) {
	out := Merge(overrides)
	for _, p := range rt.Params() {
		v := strings.TrimSpace(out[p.GetName()])
		if p.GetType() != v1.ParamType_PARAM_TYPE_PATH || v == "" || strings.EqualFold(v, None) || p.GetSolved() && strings.EqualFold(v, Auto) {
			continue
		}
		if strings.HasPrefix(v, StoreScheme) {
			m, err := storeRef(models, v)
			if err != nil {
				return nil, fmt.Errorf("param %s: %w", p.GetName(), err)
			}
			file, err := PartFile(m, p)
			if err != nil {
				return nil, fmt.Errorf("param %s: %w", p.GetName(), err)
			}
			out[p.GetName()] = file
			continue
		}
		if err := checkStorePath(v, p, models); err != nil {
			return nil, fmt.Errorf("param %s: %w", p.GetName(), err)
		}
	}
	return out, nil
}

// PartFile returns what a parameter loads from a stored group: the part in its declared subfolder
// for a pipeline, its projector or tokenizer, or its weights, through the index when they are
// sharded. Directory parameters get the directory holding the weights.
func PartFile(m *v1.StoredModel, p *v1.Param) (string, error) {
	d := m.GetDescriptor_()
	picks, name := p.GetPicks(), storedName(m)
	if diffusers.Pipeline(d) {
		declared := diffusers.Slots(d)
		slots := p.GetSlots()
		if picks == "" || picks == "model" || picks == "diffusion" {
			slots = append(slots, blueprint.SlotDenoiser)
		}
		for _, slot := range slots {
			sub := declared[slot]
			if sub == "" {
				continue
			}
			if slot == blueprint.SlotTextEncoderVision && sub == declared[blueprint.SlotTextEncoderLLM] {
				return "", fmt.Errorf("%w: %s keeps its vision tower inside the language model under %s, which loads it, so there is no separate file to name", ErrParam, name, sub)
			}
			weights, index, tokenizer := diffusers.Files(m, sub)
			if slot == blueprint.SlotTokenizer {
				if tokenizer == "" {
					return "", fmt.Errorf("%w: %s holds no tokenizer.json under %s", ErrParam, name, sub)
				}
				return tokenizer, nil
			}
			return within(name+" "+sub, weights, index, p.GetDirectory())
		}
		return "", fmt.Errorf("%w: %s bundles no %s", ErrParam, name, picksWord(picks))
	}
	kind := d.GetKind()
	switch picks {
	case "tokenizer":
		files := tokenizers(m)
		switch len(files) {
		case 0:
			return "", fmt.Errorf("%w: %s holds no tokenizer.json", ErrParam, name)
		case 1:
			return files[0], nil
		}
		return "", fmt.Errorf("%w: %s holds %d tokenizer.json files, %s. Set the path of one", ErrParam, name, len(files), strings.Join(files, ", "))
	case "projector":
		if f := companionFile(m, v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR); f != "" {
			return f, nil
		}
		return "", fmt.Errorf("%w: %s holds no projector file", ErrParam, name)
	case "checkpoint":
		if kind != v1.ModelKind_MODEL_KIND_LANGUAGE || m.GetFormatId() != "safetensors" || m.GetPath() == "" {
			return "", fmt.Errorf("%w: %s is %s, not a transformers checkpoint", ErrParam, name, what(m))
		}
		return m.GetPath(), nil
	case "", "model":
	case "diffusion":
		if kind != v1.ModelKind_MODEL_KIND_DIFFUSION {
			return "", fmt.Errorf("%w: %s is %s, not a denoiser", ErrParam, name, what(m))
		}
	case "llm":
		if kind != v1.ModelKind_MODEL_KIND_LANGUAGE && !(kind == v1.ModelKind_MODEL_KIND_COMPONENT && diffusion.PartOf(d) == "llm") {
			return "", fmt.Errorf("%w: %s is %s, not a language model", ErrParam, name, what(m))
		}
	default:
		if kind != v1.ModelKind_MODEL_KIND_COMPONENT || diffusion.PartOf(d) != picks {
			return "", fmt.Errorf("%w: %s is %s, not %s", ErrParam, name, what(m), picksWord(picks))
		}
	}
	weights, index := storedWeights(m)
	return within(name, weights, index, p.GetDirectory())
}

// Refuses an explicit path inside the store that is not what the parameter takes.
func checkStorePath(value string, p *v1.Param, models []*v1.StoredModel) error {
	if p.GetDirectory() {
		return nil
	}
	m, sa := storedArtifact(value, models)
	if m == nil {
		return nil
	}
	a, name := sa.GetArtifact(), storedName(m)
	var part string
	switch a.GetRole() {
	case v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER:
		part = "tokenizer"
	case v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR:
		part = "projector"
	case v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, v1.ArtifactRole_ARTIFACT_ROLE_INDEX:
		base := filepath.Base(value)
		if _, i, n, ok := formats.Shard(strings.TrimSuffix(base, filepath.Ext(base))); a.GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS && ok && n > 1 {
			if _, index := storedWeights(m); index != "" {
				return fmt.Errorf("%w: %s is shard %d of %d of %s. Set %s, the index every shard loads through", ErrParam, value, i, n, name, index)
			}
			return fmt.Errorf("%w: %s is shard %d of %d of %s, which holds no index naming them. Pull it again", ErrParam, value, i, n, name)
		}
		part = partOfPath(m, a.GetPath())
	default:
		return fmt.Errorf("%w: %s is a %s file of %s, not weights", ErrParam, value, text.Enum(a.GetRole()), name)
	}
	if accepts(p, part) {
		return nil
	}
	return fmt.Errorf("%w: %s is %s of %s, not %s", ErrParam, value, partWord(part), name, picksWord(p.GetPicks()))
}

// Finds the stored group and artifact at a path.
func storedArtifact(value string, models []*v1.StoredModel) (*v1.StoredModel, *v1.StoredArtifact) {
	clean := filepath.Clean(value)
	for _, m := range models {
		for _, sa := range m.GetArtifacts() {
			if filepath.Clean(sa.GetPath()) == clean {
				return m, sa
			}
		}
	}
	return nil, nil
}

// The part a stored file is: the slot of its declared subfolder in a pipeline, otherwise what the
// group is.
func partOfPath(m *v1.StoredModel, rel string) string {
	d := m.GetDescriptor_()
	if diffusers.Pipeline(d) {
		return diffusers.SlotOfPath(d, rel)
	}
	switch d.GetKind() {
	case v1.ModelKind_MODEL_KIND_DIFFUSION:
		return "diffusion"
	case v1.ModelKind_MODEL_KIND_LANGUAGE:
		return "llm"
	}
	return diffusion.PartOf(d)
}

// Reports whether a parameter takes a part, by its picks or the slots it fills.
func accepts(p *v1.Param, part string) bool {
	picks := p.GetPicks()
	switch {
	case picks == "" || picks == "model":
		return true
	case part == picks || slices.Contains(p.GetSlots(), part):
		return true
	case picks == "diffusion":
		return part == blueprint.SlotDenoiser
	case picks == "llm":
		return part == blueprint.SlotTextEncoderLLM
	}
	return false
}

// Names a stored group for messages.
func storedName(m *v1.StoredModel) string { return m.GetRepo() + " " + m.GetGroup() }

// Describes a stored group: a VAE, a denoiser, a language model.
func what(m *v1.StoredModel) string {
	d := m.GetDescriptor_()
	switch d.GetKind() {
	case v1.ModelKind_MODEL_KIND_DIFFUSION:
		if f := blueprint.Of(d); f != nil {
			return "a " + f.Name + " denoiser"
		}
		return "a denoiser"
	case v1.ModelKind_MODEL_KIND_LANGUAGE:
		return "a language model"
	case v1.ModelKind_MODEL_KIND_COMPONENT:
		return diffusion.Describe(diffusion.PartOf(d))
	}
	return "of unknown kind"
}

// Words for what a parameter takes, by picks.
func picksWord(picks string) string {
	switch picks {
	case "", "model":
		return "weights"
	case "diffusion":
		return "a denoiser"
	case "llm":
		return "a language model"
	case "projector":
		return "a vision projector"
	case "tokenizer":
		return "a tokenizer"
	case "checkpoint":
		return "a transformers checkpoint"
	}
	return diffusion.Describe(picks)
}

// Words for a stored file's part, a slot or a component kind.
func partWord(part string) string {
	switch {
	case blueprint.IsSlot(part):
		return "the " + strings.ToLower(blueprint.Label(part))
	case part == "diffusion":
		return "the denoiser"
	case part == "llm":
		return "the language model"
	case part == "":
		return "a file outside any declared part"
	}
	return diffusion.Describe(part)
}

// The file or directory a group's weights load from.
func within(name string, weights []string, index string, dir bool) (string, error) {
	if !dir {
		file, err := loadable(name, weights, index)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrParam, err)
		}
		return file, nil
	}
	if len(weights) == 0 {
		return "", fmt.Errorf("%w: %s holds no weights", ErrParam, name)
	}
	d := filepath.Dir(weights[0])
	for _, w := range weights[1:] {
		if filepath.Dir(w) != d {
			return "", fmt.Errorf("%w: %s spreads its weights over %s and %s, so no one directory holds them", ErrParam, name, d, filepath.Dir(w))
		}
	}
	return d, nil
}

// Every stored tokenizer.json.
func tokenizers(m *v1.StoredModel) []string {
	var out []string
	for _, sa := range m.GetArtifacts() {
		if sa.GetArtifact().GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER && filepath.Base(sa.GetArtifact().GetPath()) == "tokenizer.json" {
			out = append(out, sa.GetPath())
		}
	}
	return out
}
