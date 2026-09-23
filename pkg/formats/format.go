// Package formats groups model files, reads headers, and extracts model metadata.
package formats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

// Returned when a weight group cannot be found
var ErrUnknownGroup = errors.New("unknown weight group")

// Opens one artifact for random access
type Opener func(ctx context.Context, a *v1.Artifact) (sources.Blob, error)

// Classification of a repository file.
type Claim struct {
	Role v1.ArtifactRole
	// Loadable weight group.
	Group string
	// Shard position, starting at one.
	ShardIndex, ShardCount uint32
	// Whether files attach to a shared tree root instead of their directories.
	Tree bool
	// Group directory, empty for the repository root.
	Root string
}

// Precision labels and bit width.
type Words struct {
	Bits   uint32
	Labels []string
	Notes  []string
}

// A format that groups files by pipelines declared in a pipeline index.
type TreeFormat interface {
	Format
	// Classifies a path relative to the tree root.
	ClassifyIn(t *v1.Tree, rel string) (Claim, bool)
	// Splits independent denoisers into groups with their shared components.
	Split(t *v1.Tree, g *Group) []*Group
}

// Maximum pipeline index size.
const maxModelIndex = 1 << 20

// Pipeline index file names, in order of preference when a directory holds several. Diffusers
// writes model_index.json for a pipeline and modular_model_index.json for a modular pipeline.
var pipelineIndexes = []string{"model_index.json", "modular_model_index.json"}

// PipelineIndex reports whether a file name is a pipeline index.
func PipelineIndex(base string) bool { return pipelineIndexRank(base) >= 0 }

// PreferredPipelineIndex reports whether a is a better pipeline index than b for the same directory.
func PreferredPipelineIndex(a, b string) bool { return pipelineIndexRank(a) < pipelineIndexRank(b) }

func pipelineIndexRank(base string) int {
	for i, name := range pipelineIndexes {
		if base == name {
			return i
		}
	}
	return -1
}

// A model format reader and classifier.
type Format interface {
	ID() string
	Description() string
	// Description of the format.
	Blurb() string
	// Formats claim files in descending priority order.
	Priority() int
	// Classifies a path relative to the tree root.
	Classify(path string) (Claim, bool)
	// Artifact roles required to read headers.
	Requires() []v1.ArtifactRole
	// Reads headers without fetching weight data.
	Read(ctx context.Context, open Opener, g *Group) (*v1.RawModel, error)
	// Architecture from the headers.
	Architecture(raw *v1.RawModel) string
	// Architecture parameters, zero when absent.
	Params(raw *v1.RawModel) Params
	// Tensor placement and layer index, or -1 for tensors outside layers.
	Tensor(name string) (v1.TensorGroupKind, int32)
	// First prediction head layer index, or -1 if not defined by index.
	DraftFrom(p Params) int32
	// Weight count, accounting for packed elements.
	Elements(t *v1.TensorInfo, raw *v1.RawModel) uint64
	// Precision from the group name or headers.
	Precision(raw *v1.RawModel, group string) Words
	// Metadata retained on the descriptor.
	Metadata(raw *v1.RawModel) map[string]string
}

// Loadable weights and associated files. Root is the shared path prefix removed when storing the
// group. Empty means the repository root.
type Group struct {
	FormatID string
	Name     string
	Root     string
	Weights  []*v1.Artifact
	Files    map[v1.ArtifactRole][]*v1.Artifact
}

// Every format, in priority order
type Registry struct {
	list []Format
	byID map[string]Format
}

// Orders formats by priority then id
func New(formats []Format) (*Registry, error) {
	r := &Registry{byID: map[string]Format{}}
	for _, f := range formats {
		if f.ID() == "" {
			return nil, fmt.Errorf("format without id")
		}
		if _, dup := r.byID[f.ID()]; dup {
			return nil, fmt.Errorf("format %s: duplicate id", f.ID())
		}
		r.byID[f.ID()] = f
		r.list = append(r.list, f)
	}
	sort.SliceStable(r.list, func(i, j int) bool {
		if r.list[i].Priority() != r.list[j].Priority() {
			return r.list[i].Priority() > r.list[j].Priority()
		}
		return r.list[i].ID() < r.list[j].ID()
	})
	return r, nil
}

// Lists formats in priority order
func (r *Registry) List() []Format { return r.list }

// Returns a format by id, nil when unknown
func (r *Registry) Get(id string) Format { return r.byID[id] }

// Every format as the API describes it
func (r *Registry) Describe() []*v1.Format {
	out := make([]*v1.Format, 0, len(r.list))
	for _, f := range r.list {
		out = append(out, &v1.Format{Id: f.ID(), Description: f.Description(), Blurb: f.Blurb()})
	}
	return out
}

// Lay reads each pipeline index and records declared trees, deepest first. A directory holding
// both index names uses model_index.json. Unreadable or invalid indexes fail the listing to avoid
// incomplete pulls.
func (r *Registry) Lay(ctx context.Context, open Opener, m *v1.Model) error {
	m.Trees = nil
	indexes := map[string]*v1.Artifact{}
	var dirs []string
	for _, a := range m.GetArtifacts() {
		dir, base := Split(a.GetPath())
		if !PipelineIndex(base) {
			continue
		}
		if held, ok := indexes[dir]; !ok {
			dirs = append(dirs, dir)
			indexes[dir] = a
		} else if _, heldBase := Split(held.GetPath()); PreferredPipelineIndex(base, heldBase) {
			indexes[dir] = a
		}
	}
	for _, dir := range dirs {
		a := indexes[dir]
		data, err := ReadAll(ctx, open, a, maxModelIndex)
		if err != nil {
			return fmt.Errorf("%s: %w", a.GetPath(), err)
		}
		t, err := ParseTree(dir, data)
		if err != nil {
			return fmt.Errorf("%s: %w", a.GetPath(), err)
		}
		m.Trees = append(m.Trees, t)
	}
	sort.SliceStable(m.Trees, func(i, j int) bool { return len(m.Trees[i].GetRoot()) > len(m.Trees[j].GetRoot()) })
	return nil
}

// ParseTree reads the pipeline class and each component's library, class, and subfolder from a
// pipeline index. Modular pipelines name the subfolder in each component's options.
func ParseTree(root string, data []byte) (*v1.Tree, error) {
	var index map[string]json.RawMessage
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	t := &v1.Tree{Root: root, Parts: map[string]string{}, Classes: map[string]string{}}
	if raw, ok := index["_class_name"]; ok {
		json.Unmarshal(raw, &t.ClassName)
	}
	for key, raw := range index {
		if strings.HasPrefix(key, "_") {
			continue
		}
		var entry []json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil || len(entry) < 2 {
			continue
		}
		var lib, class string
		json.Unmarshal(entry[0], &lib)
		json.Unmarshal(entry[1], &class)
		if class == "" {
			continue
		}
		sub := key
		if len(entry) > 2 {
			var opts struct {
				Subfolder string `json:"subfolder"`
			}
			if json.Unmarshal(entry[2], &opts) == nil && opts.Subfolder != "" {
				sub = strings.Trim(opts.Subfolder, "/")
			}
		}
		t.Parts[key], t.Classes[key] = sub, class
	}
	return t, nil
}

// Returns the deepest tree containing the path, or nil.
func TreeOf(m *v1.Model, p string) *v1.Tree {
	for _, t := range m.GetTrees() {
		if t.GetRoot() == "" || strings.HasPrefix(p, t.GetRoot()+"/") {
			return t
		}
	}
	return nil
}

// A path relative to a tree's root
func within(t *v1.Tree, p string) string {
	if t.GetRoot() == "" {
		return p
	}
	return strings.TrimPrefix(p, t.GetRoot()+"/")
}

// Classifies artifacts in place. Declared pipelines take priority. Other formats must have their
// required files present to claim weights.
func (r *Registry) Classify(m *v1.Model) {
	for _, a := range m.GetArtifacts() {
		r.classify(m, a, nil)
	}
	excluded := map[*v1.Artifact]map[string]bool{}
	for range r.list {
		demoted := false
		for _, g := range r.Groups(m) {
			if len(Missing(r.byID[g.FormatID], g)) == 0 {
				continue
			}
			for _, a := range g.Weights {
				ex := excluded[a]
				if ex == nil {
					ex = map[string]bool{}
					excluded[a] = ex
				}
				ex[g.FormatID] = true
				r.classify(m, a, ex)
				demoted = true
			}
		}
		if !demoted {
			break
		}
	}
	r.disambiguate(m)
}

func (r *Registry) classify(m *v1.Model, a *v1.Artifact, exclude map[string]bool) {
	a.FormatId, a.Role, a.Group, a.ShardIndex, a.ShardCount = "", v1.ArtifactRole_ARTIFACT_ROLE_OTHER, "", 0, 0
	for _, f := range r.list {
		if exclude[f.ID()] {
			continue
		}
		c, ok := r.claim(m, f, a.GetPath())
		if !ok {
			continue
		}
		a.FormatId, a.Role = f.ID(), c.Role
		if c.Role == v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS {
			a.Group, a.ShardIndex, a.ShardCount = c.Group, c.ShardIndex, c.ShardCount
		}
		return
	}
}

// Classifies a path using its declared tree when available.
func (r *Registry) claim(m *v1.Model, f Format, p string) (Claim, bool) {
	tf, isTree := f.(TreeFormat)
	t := TreeOf(m, p)
	switch {
	case isTree && t != nil:
		c, ok := tf.ClassifyIn(t, within(t, p))
		if ok {
			c.Tree, c.Root = true, t.GetRoot()
		}
		return c, ok
	case isTree:
		return Claim{}, false
	}
	return f.Classify(p)
}

// Splits distinct models with the same quantization token by their unique name suffixes. Shards
// with a shared stem remain together.
func (r *Registry) disambiguate(m *v1.Model) {
	type key struct{ format, group string }
	byKey := map[key][]*v1.Artifact{}
	var order []key
	for _, a := range m.GetArtifacts() {
		if a.GetRole() != v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS {
			continue
		}
		k := key{a.GetFormatId(), a.GetGroup()}
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], a)
	}
	for _, k := range order {
		// Keep a declared pipeline's component models in one group.
		if _, tree := r.byID[k.format].(TreeFormat); tree {
			continue
		}
		stems := map[string][]*v1.Artifact{}
		var distinct []string
		for _, a := range byKey[k] {
			stem := Stem(a.GetPath())
			if _, seen := stems[stem]; !seen {
				distinct = append(distinct, stem)
			}
			stems[stem] = append(stems[stem], a)
		}
		if len(distinct) < 2 {
			continue
		}
		prefix := sharedPrefix(distinct)
		for _, stem := range distinct {
			for _, a := range stems[stem] {
				a.Group = stem[len(prefix):]
			}
		}
	}
}

// Removes the extension and shard suffix.
func Stem(p string) string {
	base := strings.TrimSuffix(p, path.Ext(p))
	if stem, _, _, ok := Shard(base); ok {
		return stem
	}
	return base
}

// Reads a -00001-of-00003 shard suffix off a name without its extension
func Shard(base string) (stem string, index, count uint32, ok bool) {
	i := strings.LastIndex(base, "-of-")
	if i < 6 || len(base) < i+9 {
		return base, 0, 0, false
	}
	idx, cnt := base[i-5:i], base[i+4:]
	if base[i-6] != '-' || len(cnt) != 5 || !digits(idx) || !digits(cnt) {
		return base, 0, 0, false
	}
	n, _ := strconv.ParseUint(idx, 10, 32)
	c, _ := strconv.ParseUint(cnt, 10, 32)
	return base[:i-6], uint32(n), uint32(c), true
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Returns the shared stem prefix, truncated to a word boundary.
func sharedPrefix(stems []string) string {
	prefix := stems[0]
	for _, s := range stems[1:] {
		n := 0
		for n < len(prefix) && n < len(s) && prefix[n] == s[n] {
			n++
		}
		prefix = prefix[:n]
	}
	cut := strings.LastIndexAny(prefix, "-_./")
	if cut < 0 {
		return ""
	}
	return prefix[:cut+1]
}

// Returns the tree root or file directory used for grouping.
func (r *Registry) attachKey(m *v1.Model, a *v1.Artifact) string {
	if root, ok := r.root(m, a); ok {
		return "\x00" + root
	}
	return path.Dir(a.GetPath())
}

// Returns the containing tree root for tree formats.
func (r *Registry) root(m *v1.Model, a *v1.Artifact) (string, bool) {
	f := r.byID[a.GetFormatId()]
	if f == nil {
		return "", false
	}
	c, ok := r.claim(m, f, a.GetPath())
	if !ok || !c.Tree {
		return "", false
	}
	return strings.TrimSuffix(c.Root, "/"), true
}

// Groups weights and attaches other artifacts with the same format and directory or tree root.
func (r *Registry) Groups(m *v1.Model) []*Group {
	byKey := map[string]*Group{}
	var order []string
	for _, a := range m.GetArtifacts() {
		if a.GetRole() != v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS {
			continue
		}
		key := a.GetFormatId() + "\x00" + a.GetGroup()
		g, ok := byKey[key]
		if !ok {
			g = &Group{FormatID: a.GetFormatId(), Name: a.GetGroup(), Files: map[v1.ArtifactRole][]*v1.Artifact{}}
			byKey[key] = g
			order = append(order, key)
		}
		g.Weights = append(g.Weights, a)
	}
	sort.Strings(order)
	groups := make([]*Group, 0, len(order))
	for _, key := range order {
		g := byKey[key]
		sort.Slice(g.Weights, func(i, j int) bool {
			if g.Weights[i].GetShardIndex() != g.Weights[j].GetShardIndex() {
				return g.Weights[i].GetShardIndex() < g.Weights[j].GetShardIndex()
			}
			return g.Weights[i].GetPath() < g.Weights[j].GetPath()
		})
		at := r.attachKey(m, g.Weights[0])
		if root, ok := r.root(m, g.Weights[0]); ok {
			g.Root = root
		} else if dir := path.Dir(g.Weights[0].GetPath()); dir != "." {
			g.Root = dir
		}
		for _, a := range m.GetArtifacts() {
			if a.GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS || a.GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_OTHER {
				continue
			}
			if a.GetFormatId() == g.FormatID && r.attachKey(m, a) == at {
				g.Files[a.GetRole()] = append(g.Files[a.GetRole()], a)
			}
		}
		for role := range g.Files {
			sort.Slice(g.Files[role], func(i, j int) bool { return g.Files[role][i].GetPath() < g.Files[role][j].GetPath() })
		}
		// Each independent denoiser gets a group.
		if tf, ok := r.byID[g.FormatID].(TreeFormat); ok {
			if t := TreeOf(m, g.Weights[0].GetPath()); t != nil {
				groups = append(groups, tf.Split(t, g)...)
				continue
			}
		}
		groups = append(groups, g)
	}
	return groups
}

// Finds one group by name, or the only one
func FindGroup(groups []*Group, name string) (*Group, error) {
	if name == "" {
		if len(groups) == 1 {
			return groups[0], nil
		}
		names := make([]string, 0, len(groups))
		for _, g := range groups {
			names = append(names, g.Name)
		}
		return nil, fmt.Errorf("%w: choose one of %s", ErrUnknownGroup, strings.Join(names, ", "))
	}
	for _, g := range groups {
		if g.Name == name {
			return g, nil
		}
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownGroup, name)
}

// Reports roles a group is missing against what its format requires
func Missing(f Format, g *Group) []v1.ArtifactRole {
	var missing []v1.ArtifactRole
	if f == nil {
		return nil
	}
	for _, role := range f.Requires() {
		if len(g.Files[role]) == 0 {
			missing = append(missing, role)
		}
	}
	return missing
}

// Reads a whole artifact, refusing one declared larger than limit
func ReadAll(ctx context.Context, open Opener, a *v1.Artifact, limit int64) ([]byte, error) {
	if a.GetSizeBytes() > uint64(limit) {
		return nil, fmt.Errorf("%d bytes is too large to read whole", a.GetSizeBytes())
	}
	blob, err := open(ctx, a)
	if err != nil {
		return nil, err
	}
	defer blob.Close()
	buf := make([]byte, blob.Size())
	if _, err := blob.ReadAt(buf, 0); err != nil && err != io.EOF {
		return nil, err
	}
	return buf, nil
}

// Parses weight headers, retaining the first metadata value across shards.
func EachWeight(ctx context.Context, open Opener, g *Group, parse func(ra io.ReaderAt, size int64) (map[string]string, []*v1.TensorInfo, error)) (*v1.RawModel, error) {
	raw := &v1.RawModel{FormatId: g.FormatID, Group: g.Name, Metadata: map[string]string{}}
	for _, a := range g.Weights {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		blob, err := open(ctx, a)
		if err != nil {
			return nil, err
		}
		metadata, tensors, err := parse(blob, blob.Size())
		blob.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
		}
		for k, v := range metadata {
			if _, exists := raw.Metadata[k]; !exists {
				raw.Metadata[k] = v
			}
		}
		raw.Tensors = append(raw.Tensors, tensors...)
	}
	return raw, nil
}

func Elements(shape []uint64) uint64 {
	n := uint64(1)
	for _, d := range shape {
		n *= d
	}
	return n
}

// Describes a tensor from its shape and a dtype name Dtype knows
func Tensor(name, dtype string, shape []uint64) *v1.TensorInfo {
	label, width, _ := Dtype(dtype)
	elements := Elements(shape)
	return &v1.TensorInfo{Name: name, Dtype: label, Elements: elements, Bytes: uint64(math.Ceil(float64(elements) * width)), Shape: shape}
}

// Maps a torch storage class, dtype, or numpy type string to label and width
func Dtype(name string) (label string, width float64, ok bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimPrefix(n, "torch.")
	n = strings.TrimSuffix(n, "storage")
	n = strings.TrimLeft(n, "<>|=")
	switch n {
	case "float", "float32", "f4":
		return "F32", 4, true
	case "half", "float16", "f2":
		return "F16", 2, true
	case "bfloat16", "bf16":
		return "BF16", 2, true
	case "double", "float64", "f8":
		return "F64", 8, true
	case "long", "int64", "i8":
		return "I64", 8, true
	case "int", "int32", "i4":
		return "I32", 4, true
	case "short", "int16", "i2":
		return "I16", 2, true
	case "char", "int8", "i1":
		return "I8", 1, true
	case "byte", "uint8", "u1":
		return "U8", 1, true
	case "bool", "b1":
		return "BOOL", 1, true
	case "float8_e4m3fn", "float8_e4m3fnuz":
		return "F8_E4M3", 1, true
	case "float8_e5m2", "float8_e5m2fnuz":
		return "F8_E5M2", 1, true
	case "complexfloat", "complex64", "c8":
		return "C64", 8, true
	case "complexdouble", "complex128", "c16":
		return "C128", 16, true
	case "quint8", "qint8":
		return "Q8", 1, true
	case "qint32":
		return "Q32", 4, true
	}
	return "", 0, false
}

// Returns the first nonempty trimmed metadata value.
func First(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(m[k]); v != "" {
			return v
		}
	}
	return ""
}

// Returns the first numeric metadata value, or zero.
func Num(m map[string]string, keys ...string) float64 {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return n
		}
		// Use the maximum for per-layer values such as sliding windows.
		if strings.Contains(v, ",") {
			best, any := 0.0, false
			for _, part := range strings.Split(v, ",") {
				n, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
				if err != nil {
					any = false
					break
				}
				if !any || n > best {
					best, any = n, true
				}
			}
			if any {
				return best
			}
		}
		switch strings.TrimSpace(v) {
		case "true":
			return 1
		case "false":
			return 0
		}
	}
	return 0
}

// The dotted segments of a tensor name
func Segments(name string) []string { return strings.Split(name, ".") }

// The layer number that follows the first of the given segment names, -1 when there is none
func LayerAfter(segments []string, names ...string) int32 {
	for i, s := range segments {
		for _, n := range names {
			if s == n && i+1 < len(segments) {
				if v, err := strconv.Atoi(segments[i+1]); err == nil {
					return int32(v)
				}
			}
		}
	}
	return -1
}

// Whether any segment is one of the names
func HasSegment(segments []string, names ...string) bool {
	for _, s := range segments {
		for _, n := range names {
			if s == n {
				return true
			}
		}
	}
	return false
}

// The base name and directory of a repository path
func Split(p string) (dir, base string) {
	dir, base = path.Split(p)
	return strings.TrimSuffix(dir, "/"), base
}
