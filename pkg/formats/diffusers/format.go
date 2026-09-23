// Package diffusers reads pipelines declared in model_index.json or modular_model_index.json.
package diffusers

import (
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/blueprint"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	"github.com/nickheyer/nebu/pkg/formats/torch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
	"golang.org/x/sync/errgroup"
)

// Format groups each declared denoiser with its pipeline components.
type Format struct{}

func (Format) ID() string          { return "diffusers" }
func (Format) Description() string { return "Diffusers pipeline directory" }
func (Format) Blurb() string {
	return "A directory holding model_index.json or modular_model_index.json, which names the subfolder of every part of the pipeline: the denoiser, the autoencoders, the text encoders, their tokenizers, and the scheduler"
}

// Claim declared pipelines before safetensors groups their components separately.
func (Format) Priority() int { return 7 }
func (Format) Requires() []v1.ArtifactRole {
	return []v1.ArtifactRole{v1.ArtifactRole_ARTIFACT_ROLE_CONFIG}
}

// Metadata keys for pipeline classes, component subfolders, and vision towers.
const (
	KeyClass = "pipeline.class"
	prefix   = "pipeline."

	readers   = 8
	maxConfig = 16 << 20
)

// Component configs flattened under the component key.
var configNames = []string{"config.json", "scheduler_config.json"}

// Auxiliary files recognized by name.
var (
	weightExts     = []string{".safetensors", ".bin", ".pt", ".pth", ".ckpt"}
	tokenizerNames = []string{"tokenizer.json", "tokenizer.model", "tokenizer_config.json", "special_tokens_map.json", "added_tokens.json", "vocab.json", "vocab.txt", "merges.txt", "spiece.model", "sentencepiece.bpe.model", "chat_template.json", "chat_template.jinja"}
)

// Files are classified through declared trees only.
func (Format) Classify(string) (formats.Claim, bool) { return formats.Claim{}, false }

// Names the group by its tree root.
func Name(t *v1.Tree) string {
	if t.GetRoot() == "" {
		return "default"
	}
	return t.GetRoot()
}

// A path relative to a tree's root
func within(t *v1.Tree, p string) string {
	if t.GetRoot() == "" {
		return p
	}
	return strings.TrimPrefix(p, t.GetRoot()+"/")
}

// Classifies weights and auxiliary files in declared component subfolders. The pipeline index is
// the pipeline config.
func (Format) ClassifyIn(t *v1.Tree, rel string) (formats.Claim, bool) {
	claim := formats.Claim{Group: Name(t)}
	if formats.PipelineIndex(rel) {
		claim.Role = v1.ArtifactRole_ARTIFACT_ROLE_CONFIG
		return claim, true
	}
	if keyOf(t.GetParts(), rel) == "" {
		return formats.Claim{}, false
	}
	_, base := formats.Split(rel)
	lower := strings.ToLower(base)
	for _, ext := range weightExts {
		if strings.HasSuffix(lower, ext) {
			claim.Role = v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS
			_, claim.ShardIndex, claim.ShardCount, _ = formats.Shard(base[:len(base)-len(ext)])
			return claim, true
		}
	}
	switch {
	case strings.HasSuffix(lower, ".safetensors.index.json") || strings.HasSuffix(lower, ".bin.index.json"):
		claim.Role = v1.ArtifactRole_ARTIFACT_ROLE_INDEX
	case oneOf(base, tokenizerNames):
		claim.Role = v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER
	case strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml"):
		claim.Role = v1.ArtifactRole_ARTIFACT_ROLE_CONFIG
	case strings.HasSuffix(lower, ".py"):
		claim.Role = v1.ArtifactRole_ARTIFACT_ROLE_CODE
	default:
		return formats.Claim{}, false
	}
	return claim, true
}

func oneOf(s string, names []string) bool {
	for _, n := range names {
		if s == n {
			return true
		}
	}
	return false
}

// Returns the component with the longest matching subfolder prefix, or empty.
func keyOf(parts map[string]string, rel string) string {
	best, bestLen := "", -1
	for key, sub := range parts {
		if strings.HasPrefix(rel, sub+"/") && len(sub) > bestLen {
			best, bestLen = key, len(sub)
		}
	}
	return best
}

// Matches the component directory nearest the file. Longer subfolders win ties.
func keyOfPath(parts map[string]string, p string) string {
	best, bestAt, bestLen := "", -1, -1
	slashed := "/" + strings.TrimPrefix(p, "/")
	for key, sub := range parts {
		at := strings.LastIndex(slashed, "/"+sub+"/")
		if at < 0 {
			continue
		}
		if at > bestAt || at == bestAt && len(sub) > bestLen {
			best, bestAt, bestLen = key, at, len(sub)
		}
	}
	return best
}

// Split creates a group per independent denoiser, retaining its weights and shared components.
// Trees with one denoiser remain unchanged.
func (Format) Split(t *v1.Tree, g *formats.Group) []*formats.Group {
	primaries := primaryKeys(t.GetParts(), t.GetClasses())
	if len(primaries) < 2 {
		return []*formats.Group{g}
	}
	// Skip denoisers with no listed weights.
	held := map[string]bool{}
	for _, a := range g.Weights {
		held[keyOf(t.GetParts(), within(t, a.GetPath()))] = true
	}
	var out []*formats.Group
	for _, key := range primaries {
		if !held[key] {
			continue
		}
		name := key
		if t.GetRoot() != "" {
			name = t.GetRoot() + "/" + key
		}
		v := &formats.Group{FormatID: g.FormatID, Name: name, Root: g.Root, Files: map[v1.ArtifactRole][]*v1.Artifact{}}
		mine := func(a *v1.Artifact) bool {
			k := keyOf(t.GetParts(), within(t, a.GetPath()))
			return k == "" || k == key || !isPrimary(k, t.GetClasses()[k])
		}
		for _, a := range g.Weights {
			if mine(a) {
				v.Weights = append(v.Weights, a)
			}
		}
		for role, files := range g.Files {
			for _, a := range files {
				if mine(a) {
					v.Files[role] = append(v.Files[role], a)
				}
			}
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return []*formats.Group{g}
	}
	return out
}

// Under returns a subfolder's weights and auxiliary files.
func Under(g *formats.Group, sub string) []*v1.Artifact {
	parts := map[string]string{sub: sub}
	var out []*v1.Artifact
	for _, a := range g.Weights {
		if keyOfPath(parts, a.GetPath()) != "" {
			out = append(out, a)
		}
	}
	for _, role := range []v1.ArtifactRole{v1.ArtifactRole_ARTIFACT_ROLE_CONFIG, v1.ArtifactRole_ARTIFACT_ROLE_INDEX, v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER, v1.ArtifactRole_ARTIFACT_ROLE_CODE} {
		for _, a := range g.Files[role] {
			if keyOfPath(parts, a.GetPath()) != "" {
				out = append(out, a)
			}
		}
	}
	return out
}

// Returns the group's pipeline index, preferring model_index.json.
func indexOf(g *formats.Group) *v1.Artifact {
	var best *v1.Artifact
	for _, a := range g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG] {
		base := path.Base(a.GetPath())
		if formats.PipelineIndex(base) && (best == nil || formats.PreferredPipelineIndex(base, path.Base(best.GetPath()))) {
			best = a
		}
	}
	return best
}

// Reads component configs and weight headers under component names. Excludes denoisers assigned to
// other variants and components whose files the tree does not hold, such as ones a modular
// pipeline takes from another repository.
func (Format) Read(ctx context.Context, open formats.Opener, g *formats.Group) (*v1.RawModel, error) {
	raw := &v1.RawModel{FormatId: g.FormatID, Group: g.Name, Metadata: map[string]string{}}
	a := indexOf(g)
	if a == nil {
		return nil, fmt.Errorf("%s: the group carries no pipeline index", g.Name)
	}
	data, err := formats.ReadAll(ctx, open, a, maxConfig)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
	}
	tree, err := formats.ParseTree(g.Root, data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
	}
	held := map[string]bool{}
	for _, a := range g.Weights {
		held[keyOfPath(tree.GetParts(), a.GetPath())] = true
	}
	raw.Metadata[KeyClass] = tree.GetClassName()
	for key, sub := range tree.GetParts() {
		if isPrimary(key, tree.GetClasses()[key]) && !held[key] || len(Under(g, sub)) == 0 {
			continue
		}
		raw.Metadata[prefix+key] = sub
		raw.Metadata[prefix+key+".class"] = tree.GetClasses()[key]
	}
	for _, a := range g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG] {
		if !oneOf(path.Base(a.GetPath()), configNames) {
			continue
		}
		key := keyOfPath(tree.GetParts(), a.GetPath())
		if key == "" || raw.Metadata[prefix+key] == "" {
			continue
		}
		data, err := formats.ReadAll(ctx, open, a, maxConfig)
		if err == nil {
			err = text.FlattenJSON(data, key, raw.Metadata)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
		}
	}
	// A text encoder with a vision config also handles image input.
	for key := range tree.GetParts() {
		if raw.Metadata[prefix+key] == "" {
			continue
		}
		for k := range raw.Metadata {
			if strings.HasPrefix(k, key+".vision_config.") {
				raw.Metadata[prefix+key+".vision"] = "true"
				break
			}
		}
	}
	shards := make([][]*v1.TensorInfo, len(g.Weights))
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(readers)
	for i, a := range g.Weights {
		eg.Go(func() error {
			key := keyOfPath(tree.GetParts(), a.GetPath())
			tensors, err := header(gctx, open, a)
			if err != nil {
				return fmt.Errorf("%s: %w", a.GetPath(), err)
			}
			for _, t := range tensors {
				t.Name = key + "/" + t.GetName()
			}
			shards[i] = tensors
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	for _, tensors := range shards {
		raw.Tensors = append(raw.Tensors, tensors...)
	}
	return raw, nil
}

// Reads a safetensors or torch tensor table.
func header(ctx context.Context, open formats.Opener, a *v1.Artifact) ([]*v1.TensorInfo, error) {
	blob, err := open(ctx, a)
	if err != nil {
		return nil, err
	}
	defer blob.Close()
	if strings.HasSuffix(strings.ToLower(a.GetPath()), ".safetensors") {
		_, tensors, err := formats.SafetensorsHeader(blob, blob.Size())
		return tensors, err
	}
	ck, err := torch.ReadZip(io.ReaderAt(blob), blob.Size())
	if err != nil {
		return nil, err
	}
	return ck.Tensors, nil
}

// Identifies denoisers by component key or model class.
func isDenoiser(key, class string) bool {
	k := strings.ToLower(key)
	return strings.HasPrefix(k, "transformer") || strings.HasPrefix(k, "unet") || strings.HasPrefix(k, "denoiser") || strings.HasPrefix(k, "prior") || k == "decoder" || diffusion.Denoiser(class)
}

// Identifies independent denoisers, excluding paired experts, refiners, and cascade stages.
func isPrimary(key, class string) bool {
	return slotOfKey(key, class) == blueprint.SlotDenoiser
}

// Lists independent denoisers, transformer and unet first, then by name.
func primaryKeys(parts, classes map[string]string) []string {
	var out []string
	for key := range parts {
		if isPrimary(key, classes[key]) {
			out = append(out, key)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := primaryRank(out[i]), primaryRank(out[j])
		if ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

func primaryRank(key string) int {
	switch strings.ToLower(key) {
	case "transformer":
		return 0
	case "unet":
		return 1
	}
	return 2
}

// Maps a component key and class to a blueprint slot. Returns empty for unrecognized components,
// schedulers, and processors.
func slotOfKey(key, class string) string {
	k, c := strings.ToLower(key), strings.ToLower(class)
	switch {
	case k == "transformer_2" || k == "unet_2":
		return blueprint.SlotDenoiserHighNoise
	case strings.HasPrefix(k, "prior"):
		return blueprint.SlotStagePrior
	case k == "decoder":
		return blueprint.SlotStageDecoder
	case isDenoiser(key, class):
		switch {
		case strings.Contains(k, "uncond"):
			return blueprint.SlotDenoiserUncond
		case strings.Contains(k, "refiner"):
			return blueprint.SlotRefiner
		}
		return blueprint.SlotDenoiser
	case strings.Contains(k, "vae") || k == "movq" || k == "vqvae" || strings.Contains(c, "autoencoder"):
		switch {
		case strings.Contains(k, "audio") || strings.Contains(c, "audio"):
			return blueprint.SlotVAEAudio
		case strings.Contains(c, "tiny"):
			return blueprint.SlotVAETiny
		}
		return blueprint.SlotVAE
	case strings.HasPrefix(k, "text_encoder"):
		switch diffusion.Canonical(class) {
		case "t5":
			return blueprint.SlotTextEncoderT5
		case "clip_l":
			return blueprint.SlotTextEncoderClipL
		case "clip_g":
			return blueprint.SlotTextEncoderClipG
		case "clip_h":
			return blueprint.SlotTextEncoderClipH
		}
		return blueprint.SlotTextEncoderLLM
	case strings.HasPrefix(k, "image_encoder"):
		return blueprint.SlotImageClipVision
	case strings.HasPrefix(k, "audio_encoder"), strings.HasPrefix(k, "speech_encoder"):
		return blueprint.SlotAudioEncoder
	case strings.HasPrefix(k, "tokenizer"):
		return blueprint.SlotTokenizer
	case k == "safety_checker":
		return blueprint.SlotSafety
	}
	return ""
}

// Returns the pipeline's denoiser key, or empty if absent.
func denoiserKey(m map[string]string) string {
	keys := primaryKeys(parts(m), classes(m))
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

// Returns the denoiser class, falling back to the pipeline class.
func (Format) Architecture(raw *v1.RawModel) string {
	m := raw.GetMetadata()
	if k := denoiserKey(m); k != "" {
		if class := m[prefix+k+".class"]; class != "" {
			return class
		}
	}
	return m[KeyClass]
}

// Reads denoiser block count and width for activation estimates.
func (Format) Params(raw *v1.RawModel) formats.Params {
	m := raw.GetMetadata()
	k := denoiserKey(m)
	if k == "" {
		return formats.Params{}
	}
	return formats.Params{
		Layers:    formats.Num(m, k+".num_layers", k+".num_hidden_layers", k+".depth", k+".num_single_layers"),
		Embedding: formats.Num(m, k+".hidden_size", k+".inner_dim", k+".dim", k+".hidden_dim", k+".caption_channels"),
	}
}

// Classifies tensors by component key, including vision towers within text encoders.
func (Format) Tensor(name string) (v1.TensorGroupKind, int32) {
	key, rest, _ := strings.Cut(name, "/")
	kind := kindOfKey(key)
	if kind == v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER && formats.HasSegment(formats.Segments(rest), "visual", "vision_tower", "vision_model", "vision_encoder") {
		kind = v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION
	}
	return kind, -1
}

// Maps pipeline component names to placement kinds.
func kindOfKey(key string) v1.TensorGroupKind {
	k := strings.ToLower(key)
	switch {
	case strings.HasPrefix(k, "transformer"), strings.HasPrefix(k, "unet"), strings.HasPrefix(k, "prior"), k == "decoder", strings.HasPrefix(k, "denoiser"):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION
	case strings.Contains(k, "vae"), k == "movq", k == "vqvae":
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE
	case strings.HasPrefix(k, "text_encoder"):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER
	case strings.HasPrefix(k, "image_encoder"):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_VISION
	case strings.HasPrefix(k, "audio_encoder"), strings.HasPrefix(k, "speech_encoder"):
		return v1.TensorGroupKind_TENSOR_GROUP_KIND_AUDIO
	}
	return v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER
}

func (Format) DraftFrom(formats.Params) int32                   { return -1 }
func (Format) Elements(t *v1.TensorInfo, _ *v1.RawModel) uint64 { return t.GetElements() }

// Uses the dtype with the largest share of denoiser bytes.
func (Format) Precision(raw *v1.RawModel, _ string) formats.Words {
	k := denoiserKey(raw.GetMetadata())
	bytesBy := map[string]uint64{}
	for _, t := range raw.GetTensors() {
		if key, _, _ := strings.Cut(t.GetName(), "/"); key == k {
			bytesBy[strings.ToUpper(t.GetDtype())] += t.GetBytes()
		}
	}
	var top string
	for dtype, n := range bytesBy {
		if top == "" || n > bytesBy[top] {
			top = dtype
		}
	}
	var w formats.Words
	switch top {
	case "BF16":
		w.Bits, w.Labels, w.Notes = 16, []string{"bfloat16"}, []string{"bfloat16, the training format on modern GPUs"}
	case "F16":
		w.Bits, w.Labels, w.Notes = 16, []string{"float16"}, []string{"float16, the training format on older GPUs"}
	case "F32":
		w.Bits, w.Labels = 32, []string{"float32"}
	case "F8_E4M3", "F8_E5M2", "FLOAT8_E4M3FN", "FLOAT8_E5M2":
		w.Bits, w.Labels, w.Notes = 8, []string{"float8"}, []string{"8-bit floating point, half the size of 16-bit with little lost"}
	}
	return w
}

// Builds metadata from pipeline declarations. Infers the family from the denoiser class, then its
// tensors, and records whether the tensor names identify it, since stable-diffusion.cpp reads a
// checkpoint by its names rather than its config. Records bundled slots and the scheduler's flow
// shift.
func (f Format) Metadata(raw *v1.RawModel) map[string]string {
	m := raw.GetMetadata()
	out := map[string]string{}
	for k, v := range m {
		if strings.HasPrefix(k, prefix) {
			out[k] = v
		}
	}
	family := diffusion.Canonical(f.Architecture(raw))
	if k := denoiserKey(m); k != "" {
		var bare []*v1.TensorInfo
		for _, t := range raw.GetTensors() {
			if key, rest, ok := strings.Cut(t.GetName(), "/"); ok && key == k {
				bare = append(bare, &v1.TensorInfo{Name: rest, Dtype: t.GetDtype(), Bytes: t.GetBytes(), Elements: t.GetElements(), Shape: t.GetShape()})
			}
		}
		scanned := diffusion.Scan(bare, k).Family
		out[diffusion.KeyDetected] = strconv.FormatBool(scanned != "")
		if !diffusion.Denoiser(family) {
			family = scanned
		}
	}
	if family != "" {
		out[diffusion.KeyFamily] = family
		out[diffusion.KeyGenerates] = strings.Join(diffusion.Generates(family), ",")
	}
	if shift, ok := flowShift(m); ok {
		out[diffusion.KeyFlowShift] = shift
	}
	slots := slotsOf(m, family)
	ids := make([]string, 0, len(slots))
	for slot := range slots {
		ids = append(ids, slot)
	}
	sort.Strings(ids)
	out[diffusion.KeySlots] = strings.Join(ids, ",")
	has := func(slot string) string {
		_, ok := slots[slot]
		return fmt.Sprint(ok)
	}
	out[diffusion.KeyVAE] = has(blueprint.SlotVAE)
	out[diffusion.KeyTextEncoder] = fmt.Sprint(anyText(slots))
	out[diffusion.KeyClipVision] = has(blueprint.SlotImageClipVision)
	return out
}

// Returns the fixed flow shift the pipeline's scheduler config declares. Schedulers that shift
// dynamically by resolution declare none.
func flowShift(m map[string]string) (string, bool) {
	key := schedulerKey(m)
	if key == "" || m[key+".use_dynamic_shifting"] == "true" {
		return "", false
	}
	for _, name := range []string{"flow_shift", "shift"} {
		if v, err := strconv.ParseFloat(m[key+"."+name], 64); err == nil && v > 0 {
			return m[key+"."+name], true
		}
	}
	return "", false
}

// Returns the component key of the scheduler, preferring one named scheduler.
func schedulerKey(m map[string]string) string {
	parts, classes := parts(m), classes(m)
	if parts["scheduler"] != "" {
		return "scheduler"
	}
	keys := make([]string, 0, len(parts))
	for k := range parts {
		if strings.Contains(strings.ToLower(classes[k]), "scheduler") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func anyText(slots map[string]string) bool {
	for _, s := range []string{blueprint.SlotTextEncoderClipL, blueprint.SlotTextEncoderClipG, blueprint.SlotTextEncoderClipH, blueprint.SlotTextEncoderT5, blueprint.SlotTextEncoderLLM, blueprint.SlotTextEncoderGlyph} {
		if _, ok := slots[s]; ok {
			return true
		}
	}
	return false
}

// Maps component keys to subfolders from descriptor metadata.
func parts(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if strings.HasPrefix(k, prefix) && !strings.HasSuffix(k, ".class") && !strings.HasSuffix(k, ".vision") && k != KeyClass {
			out[strings.TrimPrefix(k, prefix)] = v
		}
	}
	return out
}

// Maps component keys to classes.
func classes(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, ".class") {
			out[strings.TrimSuffix(strings.TrimPrefix(k, prefix), ".class")] = v
		}
	}
	return out
}

// Pipeline reports whether the descriptor has a declared pipeline.
func Pipeline(d *v1.Descriptor) bool { return d.GetMetadata()[KeyClass] != "" }

// Parts maps component keys to declared subfolders.
func Parts(d *v1.Descriptor) map[string]string { return parts(d.GetMetadata()) }

// Slots maps blueprint slots to their component subfolders.
func Slots(d *v1.Descriptor) map[string]string {
	m := d.GetMetadata()
	return slotsOf(m, diffusion.Canonical(m[diffusion.KeyFamily]))
}

// Uses the first component declared for each slot. Wan 2.2 uses transformer for high noise and
// transformer_2 for low noise, reversing other pairs. Text encoders with vision towers fill both
// slots.
func slotsOf(m map[string]string, family string) map[string]string {
	parts, classes := parts(m), classes(m)
	out := map[string]string{}
	keys := make([]string, 0, len(parts))
	for k := range parts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		slot := slotOfKey(key, classes[key])
		if slot == "" {
			continue
		}
		if family == "wan" && parts["transformer_2"] != "" {
			switch strings.ToLower(key) {
			case "transformer":
				slot = blueprint.SlotDenoiserHighNoise
			case "transformer_2":
				slot = blueprint.SlotDenoiser
			}
		}
		if _, taken := out[slot]; !taken {
			out[slot] = parts[key]
		}
		if slot == blueprint.SlotTextEncoderLLM && m[prefix+key+".vision"] == "true" {
			if _, taken := out[blueprint.SlotTextEncoderVision]; !taken {
				out[blueprint.SlotTextEncoderVision] = parts[key]
			}
		}
	}
	return out
}

// SlotOfPath returns the slot of the declared subfolder holding a file, or empty. A language
// model that also fills the vision slot reports the language slot.
func SlotOfPath(d *v1.Descriptor, p string) string {
	var found []string
	for slot, sub := range Slots(d) {
		if keyOfPath(map[string]string{sub: sub}, p) != "" {
			found = append(found, slot)
		}
	}
	sort.Strings(found)
	for _, slot := range found {
		if slot != blueprint.SlotTextEncoderVision || len(found) == 1 {
			return slot
		}
	}
	return ""
}

// Files returns a subfolder's weights in shard order, its shard index, and tokenizer.json.
func Files(m *v1.StoredModel, sub string) (weights []string, index, tokenizer string) {
	parts := map[string]string{sub: sub}
	for _, sa := range m.GetArtifacts() {
		a := sa.GetArtifact()
		if keyOfPath(parts, a.GetPath()) == "" {
			continue
		}
		switch a.GetRole() {
		case v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS:
			weights = append(weights, sa.GetPath())
		case v1.ArtifactRole_ARTIFACT_ROLE_INDEX:
			index = sa.GetPath()
		case v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER:
			if path.Base(a.GetPath()) == "tokenizer.json" {
				tokenizer = sa.GetPath()
			}
		}
	}
	sort.Strings(weights)
	return weights, index, tokenizer
}
