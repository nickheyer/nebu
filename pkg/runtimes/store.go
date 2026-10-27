package runtimes

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nickheyer/nebu/pkg/blueprint"
	"github.com/nickheyer/nebu/pkg/formats/diffusers"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
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

// Replaces store references in path parameters with the file each loads, other paths passing as given
func ResolveStore(rt Runtime, overrides map[string]string, models []*v1.StoredModel) (map[string]string, error) {
	out := Merge(overrides)
	for _, p := range rt.Params() {
		v := strings.TrimSpace(out[p.GetName()])
		if p.GetType() != v1.ParamType_PARAM_TYPE_PATH || !strings.HasPrefix(v, StoreScheme) {
			continue
		}
		m, err := storeRef(models, v)
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", p.GetName(), err)
		}
		file, err := PartFile(m, p)
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", p.GetName(), err)
		}
		out[p.GetName()] = file
	}
	return out, nil
}

// The file a parameter loads from a group, its picked part when the group holds one, else its weights
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
			weights, index, tokenizer := diffusers.Files(m, sub)
			if slot == blueprint.SlotTokenizer {
				if tokenizer == "" {
					return "", fmt.Errorf("%w: %s holds no tokenizer.json under %s", ErrParam, name, sub)
				}
				return tokenizer, nil
			}
			return within(name+" "+sub, weights, index, p.GetDirectory())
		}
		return "", fmt.Errorf("%w: %s declares no %s", ErrParam, name, strings.Join(slots, ", "))
	}
	switch picks {
	case "tokenizer":
		if files := tokenizers(m); len(files) > 0 {
			return files[0], nil
		}
		return "", fmt.Errorf("%w: %s holds no tokenizer.json", ErrParam, name)
	case "projector":
		if f := companionFile(m, v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR); f != "" {
			return f, nil
		}
		return "", fmt.Errorf("%w: %s holds no projector file", ErrParam, name)
	case "draft":
		if files := roleFiles(m, v1.ArtifactRole_ARTIFACT_ROLE_DRAFT); len(files) > 0 {
			return files[0], nil
		}
	case "checkpoint":
		if m.GetPath() != "" {
			return m.GetPath(), nil
		}
	}
	weights, index := storedWeights(m)
	return within(name, weights, index, p.GetDirectory())
}

// Names a stored group for messages.
func storedName(m *v1.StoredModel) string { return m.GetRepo() + " " + m.GetGroup() }

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

// Every stored file of a role, in stored order
func roleFiles(m *v1.StoredModel, role v1.ArtifactRole) []string {
	var out []string
	for _, sa := range m.GetArtifacts() {
		if sa.GetArtifact().GetRole() == role {
			out = append(out, sa.GetPath())
		}
	}
	return out
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
