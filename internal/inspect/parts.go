package inspect

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nickheyer/nebu/pkg/blueprint"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusers"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

// Maximum groups inspected per repository for a slot.
const candidateMax = 48

// A required blueprint slot could not be resolved.
var ErrUnfilled = errors.New("required blueprint part unavailable")

// Pick records the stored, bundled, or downloadable part for a blueprint slot.
type Pick struct {
	Fill blueprint.Fill
	// Already stored.
	Stored *v1.StoredModel
	// Included in the model's group.
	Bundled bool
	// Subfolder within a declared pipeline.
	Subfolder string
	// Download source for an unstored part.
	Source sources.Source
	Model  *v1.Model
	Group  *formats.Group
	// Selected files, such as a tokenizer. Nil selects the whole group.
	Files      []*v1.Artifact
	Descriptor *v1.Descriptor
	// Resolution error, or nil for a resolved slot.
	Err error
}

// Pulls reports whether the part needs downloading.
func (p Pick) Pulls() bool { return p.Stored == nil && !p.Bundled && p.Err == nil && p.Group != nil }

// Artifacts returns selected files or all files in the group.
func (p Pick) Artifacts() []*v1.Artifact {
	if p.Files != nil {
		return p.Files
	}
	return GroupArtifacts(p.Group)
}

// Bytes returns the download size.
func (p Pick) Bytes() uint64 {
	if !p.Pulls() {
		return 0
	}
	var n uint64
	for _, a := range p.Artifacts() {
		n += a.GetSizeBytes()
	}
	return n
}

func (p Pick) Proto() *v1.Part {
	out := &v1.Part{Slot: p.Fill.Slot, Label: blueprint.Label(p.Fill.Slot), Name: p.Fill.Name, Required: p.Fill.Required, Bundled: p.Bundled}
	switch {
	case p.Err != nil:
		out.Error = p.Err.Error()
	case p.Bundled:
		out.Subfolder = p.Subfolder
		for _, a := range p.Files {
			out.SizeBytes += a.GetSizeBytes()
		}
	case p.Stored != nil:
		out.Stored = true
		out.SourceId, out.Repo, out.Revision, out.Group = p.Stored.GetSourceId(), p.Stored.GetRepo(), p.Stored.GetRevision(), p.Stored.GetGroup()
		out.SizeBytes = p.Stored.GetBytes()
	case p.Group != nil:
		out.SourceId, out.Repo, out.Revision, out.Group = p.Source.Spec().GetId(), p.Model.GetRepo(), p.Model.GetRevision(), p.Group.Name
		out.SizeBytes = p.Bytes()
		for _, a := range p.Files {
			out.Paths = append(out.Paths, a.GetPath())
		}
	}
	return out
}

// Plan contains the resolved parts for a group.
type Plan struct {
	Family     *blueprint.Family
	Descriptor *v1.Descriptor
	Picks      []Pick
}

// Pulls returns parts that need downloading.
func (p *Plan) Pulls() []Pick {
	var out []Pick
	for _, pk := range p.Picks {
		if pk.Pulls() {
			out = append(out, pk)
		}
	}
	return out
}

// Bytes returns the total download size.
func (p *Plan) Bytes() uint64 {
	var n uint64
	for _, pk := range p.Picks {
		n += pk.Bytes()
	}
	return n
}

// Unfilled returns the first unresolved required slot, or nil.
func (p *Plan) Unfilled() error {
	for _, pk := range p.Picks {
		if pk.Err != nil && pk.Fill.Required {
			return fmt.Errorf("%w: %s, %s: %v", ErrUnfilled, pk.Fill.Slot, pk.Fill.Name, pk.Err)
		}
	}
	return nil
}

func (p *Plan) Proto() *v1.PartsResponse {
	out := &v1.PartsResponse{Descriptor_: p.Descriptor, PullBytes: p.Bytes()}
	if p.Family != nil {
		out.Family, out.FamilyName = p.Family.ID, p.Family.Name
	}
	for _, pk := range p.Picks {
		out.Parts = append(out.Parts, pk.Proto())
	}
	return out
}

// GroupArtifacts returns the group's weights and supporting files.
func GroupArtifacts(g *formats.Group) []*v1.Artifact {
	var out []*v1.Artifact
	out = append(out, g.Weights...)
	for _, role := range []v1.ArtifactRole{v1.ArtifactRole_ARTIFACT_ROLE_CONFIG, v1.ArtifactRole_ARTIFACT_ROLE_INDEX, v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER, v1.ArtifactRole_ARTIFACT_ROLE_TEMPLATE, v1.ArtifactRole_ARTIFACT_ROLE_CODE, v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR} {
		out = append(out, g.Files[role]...)
	}
	return out
}

func listed(repo string, g *formats.Group, d *v1.Descriptor) blueprint.Candidate {
	c := blueprint.Candidate{Repo: repo, Group: g.Name, Roles: map[v1.ArtifactRole]bool{}, Descriptor: d}
	for _, a := range GroupArtifacts(g) {
		c.Paths = append(c.Paths, a.GetPath())
		c.Roles[a.GetRole()] = true
	}
	return c
}

func storedCandidate(m *v1.StoredModel) blueprint.Candidate {
	c := blueprint.Candidate{Repo: m.GetRepo(), Group: m.GetGroup(), Roles: map[v1.ArtifactRole]bool{}, Descriptor: m.GetDescriptor_()}
	for _, sa := range m.GetArtifacts() {
		c.Paths = append(c.Paths, sa.GetArtifact().GetPath())
		c.Roles[sa.GetArtifact().GetRole()] = true
	}
	return c
}

// Parts resolves blueprint slots from bundled files, the store, the model's
// repository, then blueprint repositories. It prefers matching precision.
// Declared pipelines identify bundled parts by subfolder.
func (i *Inspector) Parts(ctx context.Context, src sources.Source, model *v1.Model, g *formats.Group, d *v1.Descriptor) (*Plan, error) {
	plan := &Plan{Family: blueprint.Of(d), Descriptor: d}
	if plan.Family == nil {
		return plan, nil
	}
	if plan.Family.ID == blueprint.Language {
		plan.Picks = languagePicks(plan.Family, g)
		return plan, nil
	}
	profile := diffusion.ProfileOf(d)
	target := blueprint.Target{Repo: model.GetRepo(), Group: g.Name, Family: plan.Family, Bits: d.GetPrecision().GetBits()}
	stored, err := i.Companions(model.GetSourceId(), model.GetRepo(), g.Name)
	if err != nil {
		return nil, err
	}
	fills := blueprint.Needs(profile, g.Name)
	// Declared pipelines map slots to subfolders in model_index.json.
	declared := diffusers.Slots(d)
	if diffusers.Pipeline(d) {
		fills = blueprint.Fills(profile, g.Name)
	}
	var llm *Pick
	for _, fill := range fills {
		pick := Pick{Fill: fill}
		switch {
		case declared[fill.Slot] != "":
			pick.Bundled, pick.Subfolder = true, declared[fill.Slot]
			pick.Files = diffusers.Under(g, declared[fill.Slot])
		case fill.Slot == blueprint.SlotTextEncoderVision && llm != nil && llm.carries(fill, target):
			// Reuse the selected language model's vision tower.
			pick = *llm
			pick.Fill, pick.Files = fill, nil
		default:
			pick = i.resolve(ctx, src, model, g, target, fill, stored)
		}
		if fill.Slot == blueprint.SlotTextEncoderLLM {
			p := pick
			llm = &p
		}
		plan.Picks = append(plan.Picks, pick)
	}
	return plan, nil
}

// PartsOf resolves and describes a group, then resolves its parts.
func (i *Inspector) PartsOf(ctx context.Context, sourceID, repo, revision, group string) (*Plan, error) {
	src, model, err := i.Resolve(ctx, sourceID, repo, revision)
	if err != nil {
		return nil, err
	}
	g, err := formats.FindGroup(i.Formats.Groups(model), group)
	if err != nil {
		return nil, err
	}
	d, err := i.Describe(ctx, src, model, g)
	if err != nil {
		return nil, err
	}
	return i.Parts(ctx, src, model, g, d)
}

// Reports whether the selected language model includes the required vision tower.
func (p *Pick) carries(fill blueprint.Fill, t blueprint.Target) bool {
	switch {
	case p.Stored != nil:
		return blueprint.Fits(fill, t, storedCandidate(p.Stored))
	case p.Group != nil:
		return blueprint.Fits(fill, t, listed(p.Model.GetRepo(), p.Group, p.Descriptor))
	}
	return false
}

// Language model parts are bundled in the group. GGUF and NeMo embed config
// and tokenizer data. A projector is included when present.
func languagePicks(f *blueprint.Family, g *formats.Group) []Pick {
	var out []Pick
	for _, fill := range f.Fills {
		pick := Pick{Fill: fill}
		switch fill.Slot {
		case blueprint.SlotWeights:
			pick.Bundled = len(g.Weights) > 0
		case blueprint.SlotConfig:
			pick.Bundled = g.FormatID == "gguf" || g.FormatID == "nemo" || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG]) > 0
		case blueprint.SlotTokenizer:
			pick.Bundled = g.FormatID == "gguf" || g.FormatID == "nemo" || len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER]) > 0
		case blueprint.SlotProjector:
			if len(g.Files[v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR]) == 0 {
				continue
			}
			pick.Bundled = true
		}
		if !pick.Bundled {
			pick.Err = fmt.Errorf("group has no %s", blueprint.Label(fill.Slot))
		}
		out = append(out, pick)
	}
	return out
}

// Resolves a slot from the store, the model's repository, then blueprint
// repositories. Reports repository errors if no match is found.
func (i *Inspector) resolve(ctx context.Context, src sources.Source, model *v1.Model, g *formats.Group, t blueprint.Target, fill blueprint.Fill, stored []*v1.StoredModel) Pick {
	pick := Pick{Fill: fill}
	if m := i.storedFit(fill, t, stored); m != nil {
		pick.Stored = m
		return pick
	}
	var failures []string
	// Search the model's repository before blueprint repositories.
	repos := append([]string{model.GetRepo()}, blueprint.Published(&blueprint.Family{Canonical: t.Family.Canonical, Fills: []blueprint.Fill{fill}})...)
	seen := map[string]bool{}
	for _, repo := range repos {
		if seen[repo] {
			continue
		}
		seen[repo] = true
		rsrc, rmodel := src, model
		if repo != model.GetRepo() {
			var err error
			rsrc, rmodel, err = i.Resolve(ctx, hubSource(i, src), repo, "")
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", repo, err))
				continue
			}
		}
		found, err := i.fitIn(ctx, rsrc, rmodel, g, t, fill)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", repo, err))
			continue
		}
		if found != nil {
			return *found
		}
	}
	msg := fmt.Sprintf("no matching part in %s", strings.Join(repos, ", "))
	if len(failures) > 0 {
		msg += ". " + strings.Join(failures, ". ")
	}
	pick.Err = errors.New(msg)
	return pick
}

// Blueprint repositories use the model's Hugging Face source, or the first
// configured Hugging Face source.
func hubSource(i *Inspector, own sources.Source) string {
	if own.Spec().GetKind() == v1.SourceKind_SOURCE_KIND_HUGGINGFACE {
		return own.Spec().GetId()
	}
	for _, spec := range i.Sources.List() {
		if spec.GetKind() == v1.SourceKind_SOURCE_KIND_HUGGINGFACE {
			return spec.GetId()
		}
	}
	return own.Spec().GetId()
}

// Returns the highest ranked stored match, or nil.
func (i *Inspector) storedFit(fill blueprint.Fill, t blueprint.Target, stored []*v1.StoredModel) *v1.StoredModel {
	var fits []*v1.StoredModel
	for _, m := range stored {
		if blueprint.Fits(fill, t, storedCandidate(m)) {
			fits = append(fits, m)
		}
	}
	if len(fits) == 0 {
		return nil
	}
	sort.SliceStable(fits, func(a, b int) bool {
		return blueprint.Rank(fill, t, storedCandidate(fits[a])) < blueprint.Rank(fill, t, storedCandidate(fits[b]))
	})
	return fits[0]
}

// Path hints used to inspect likely matches first.
var hints = map[string][]string{
	"vae":                   {"vae", "ae", "autoencoder", "first_stage"},
	"audio_vae":             {"audio", "vae"},
	"taesd":                 {"tae"},
	"t5":                    {"text_encoder", "t5", "umt5", "byt5", "mt5", "ul2", "encoder"},
	"clip_l":                {"text_encoder", "clip", "clip_l"},
	"clip_g":                {"text_encoder", "clip", "clip_g"},
	"clip_h":                {"text_encoder", "clip", "clip_h"},
	"llm":                   {"text_encoder", "llm", "qwen", "mistral", "gemma", "llama", "glm", "gpt"},
	"projector":             {"mmproj", "text_encoder", "llm", "qwen", "mistral", "gemma", "llama"},
	"clip_vision":           {"clip_vision", "image_encoder", "vision", "siglip", "sigclip"},
	"audio_encoder":         {"audio_encoder", "wav2vec", "audio"},
	"embeddings_connectors": {"connector"},
	"tokenizer":             {"tokenizer"},
	"diffusion":             {"diffusion_model", "transformer", "unet", "prior", "decoder", "stage"},
}

// Reports whether group paths match a part's hints.
func hinted(kind string, g *formats.Group) bool {
	words := hints[kind]
	for _, a := range GroupArtifacts(g) {
		tokens := blueprint.Tokens(a.GetPath())
		for _, w := range words {
			for _, t := range tokens {
				if t == w || strings.HasPrefix(t, w) {
					return true
				}
			}
		}
	}
	return false
}

// Inspects up to candidateMax groups, checking path hints first. Matches are
// ranked by directory and precision. Tokenizer picks contain only tokenizer files.
func (i *Inspector) fitIn(ctx context.Context, src sources.Source, model *v1.Model, own *formats.Group, t blueprint.Target, fill blueprint.Fill) (*Pick, error) {
	groups := i.Formats.Groups(model)
	var first, rest []*formats.Group
	for _, g := range groups {
		if model.GetRepo() == t.Repo && g.Name == own.Name {
			continue
		}
		if hinted(fill.Kind, g) {
			first = append(first, g)
		} else {
			rest = append(rest, g)
		}
	}
	type found struct {
		g *formats.Group
		d *v1.Descriptor
	}
	var fits []found
	var lastErr error
	read := 0
	// Only inspect other groups if no hinted group matches.
	for _, batch := range [][]*formats.Group{first, rest} {
		for _, g := range batch {
			if read >= candidateMax {
				break
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			read++
			d, err := i.Describe(ctx, src, model, g)
			if err != nil {
				lastErr = err
				continue
			}
			if blueprint.Fits(fill, t, listed(model.GetRepo(), g, d)) {
				fits = append(fits, found{g, d})
			}
		}
		if len(fits) > 0 {
			break
		}
	}
	if len(fits) == 0 {
		if lastErr != nil && read > 0 {
			return nil, lastErr
		}
		return nil, nil
	}
	want := t.Bits
	// Prefer the same repository directory, then the closest precision.
	sort.SliceStable(fits, func(a, b int) bool {
		ba, bb := sameBranch(t, model.GetRepo(), fits[a].g.Name), sameBranch(t, model.GetRepo(), fits[b].g.Name)
		if ba != bb {
			return ba
		}
		return distance(fits[a].d, want) < distance(fits[b].d, want)
	})
	best := fits[0]
	pick := &Pick{Fill: fill, Source: src, Model: model, Group: best.g, Descriptor: best.d}
	if fill.Kind == "tokenizer" {
		for _, a := range best.g.Files[v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER] {
			pick.Files = append(pick.Files, a)
		}
		if len(pick.Files) == 0 {
			return nil, fmt.Errorf("%s %s has no tokenizer files", model.GetRepo(), best.g.Name)
		}
	}
	return pick, nil
}

// Reports whether groups share a repository and first directory, including root.
func sameBranch(t blueprint.Target, repo, group string) bool {
	if repo != t.Repo {
		return false
	}
	branch := func(name string) string {
		if i := strings.Index(name, "/"); i >= 0 {
			return name[:i]
		}
		return ""
	}
	return branch(group) == branch(t.Group)
}

// Precision difference in bits. Zero target bits prefers the widest precision.
func distance(d *v1.Descriptor, want uint32) int {
	have := d.GetPrecision().GetBits()
	if want == 0 {
		return -int(have)
	}
	if have > want {
		return int(have - want)
	}
	return int(want - have)
}
