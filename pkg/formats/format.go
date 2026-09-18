// Package formats knows every weight file format: which files a repository's listing holds, how they
// group into loadable sets, how their headers are read, and what those headers say.
package formats

import (
	"context"
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

// What a format makes of one file in a listing
type Claim struct {
	Role v1.ArtifactRole
	// For weights, the loadable set the file belongs to
	Group string
	// For a file that is one shard of many, its place, one based
	ShardIndex, ShardCount uint32
	// Whether the format lays a group out as a tree, every file attaching to the tree's root rather than its own directory
	Tree bool
	// The tree root, the directory holding the group, empty for the repository root
	Root string
}

// How a weight group's precision reads from its headers or name
type Words struct {
	Bits   uint32
	Labels []string
	Notes  []string
}

// One weight file format, from a repository listing to the facts the planner reads
type Format interface {
	ID() string
	Description() string
	// What the format is, for people who have not met it
	Blurb() string
	// Formats claim files in priority order, highest first
	Priority() int
	// Classifies one path, false when the format does not know the file
	Classify(path string) (Claim, bool)
	// Roles a group must carry files of for its headers to be readable
	Requires() []v1.ArtifactRole
	// Reads the headers of one group, weight bytes never fetched
	Read(ctx context.Context, open Opener, g *Group) (*v1.RawModel, error)
	// The architecture the headers name
	Architecture(raw *v1.RawModel) string
	// The architecture parameters the headers hold, zero for what they lack
	Params(raw *v1.RawModel) Params
	// The placement class of a tensor and its layer, -1 for a tensor of no layer
	Tensor(name string) (v1.TensorGroupKind, int32)
	// The first layer index that is a prediction head rather than a main layer, -1 when layers are never drafts by index
	DraftFrom(p Params) int32
	// The weights a tensor holds, more than its elements when several weights pack into each
	Elements(t *v1.TensorInfo, raw *v1.RawModel) uint64
	// The precision of a group as its name or headers put it
	Precision(raw *v1.RawModel, group string) Words
	// The header keys worth keeping on the descriptor
	Metadata(raw *v1.RawModel) map[string]string
}

// Loadable set of weights plus attached files
//
// Root is the directory prefix every path in the group shares and the store strips when it lays
// the group out: the weights' directory, or the root of a group shaped as a tree. Empty for the
// repository root.
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

// Fills classification fields on every artifact in place
//
// Formats claim files in priority order. A weight whose format requires files the repository
// does not carry falls through to the next format that claims it, so a lone safetensors
// checkpoint without a config is not left with a format that cannot read it.
func (r *Registry) Classify(m *v1.Model) {
	for _, a := range m.GetArtifacts() {
		r.classify(a, nil)
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
				r.classify(a, ex)
				demoted = true
			}
		}
		if !demoted {
			break
		}
	}
	r.disambiguate(m)
}

func (r *Registry) classify(a *v1.Artifact, exclude map[string]bool) {
	a.FormatId, a.Role, a.Group, a.ShardIndex, a.ShardCount = "", v1.ArtifactRole_ARTIFACT_ROLE_OTHER, "", 0, 0
	for _, f := range r.list {
		if exclude[f.ID()] {
			continue
		}
		c, ok := f.Classify(a.GetPath())
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

// Splits a group whose files are different models rather than shards of one: a repository that
// publishes model-IQ4_XS, model-MTP-IQ4_XS, and model-LOW-MTP-IQ4_XS names every one by the quant
// token, so each is named by what sets it apart from the others instead, IQ4_XS, MTP-IQ4_XS, and
// LOW-MTP-IQ4_XS. Shards of one file share a stem and stay together.
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

// The path without its extension and, for one shard of many, without the shard suffix
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

// The longest prefix every stem shares, cut back to a word boundary so a name never starts mid token
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

// Names the place a file attaches to: the root its format's tree layout gives it, else its directory
func (r *Registry) attachKey(a *v1.Artifact) string {
	if root, ok := r.root(a); ok {
		return "\x00" + root
	}
	return path.Dir(a.GetPath())
}

// Returns the tree root a path sits under when its format lays groups out as trees
func (r *Registry) root(a *v1.Artifact) (string, bool) {
	f := r.byID[a.GetFormatId()]
	if f == nil {
		return "", false
	}
	c, ok := f.Classify(a.GetPath())
	if !ok || !c.Tree {
		return "", false
	}
	return strings.TrimSuffix(c.Root, "/"), true
}

// Groups classified artifacts into loadable weight sets
//
// Files with a role other than weights attach to every group of their format that shares their
// directory, or their root for tree shaped formats.
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
		at := r.attachKey(g.Weights[0])
		if root, ok := r.root(g.Weights[0]); ok {
			g.Root = root
		} else if dir := path.Dir(g.Weights[0].GetPath()); dir != "." {
			g.Root = dir
		}
		for _, a := range m.GetArtifacts() {
			if a.GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS || a.GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_OTHER {
				continue
			}
			if a.GetFormatId() == g.FormatID && r.attachKey(a) == at {
				g.Files[a.GetRole()] = append(g.Files[a.GetRole()], a)
			}
		}
		for role := range g.Files {
			sort.Slice(g.Files[role], func(i, j int) bool { return g.Files[role][i].GetPath() < g.Files[role][j].GetPath() })
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

// Reads each weight through parse, first metadata value winning across shards
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

// Counts the elements a shape holds
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

// The first metadata value found under any of the keys, trimmed, empty when none is set
func First(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(m[k]); v != "" {
			return v
		}
	}
	return ""
}

// The first metadata value under any of the keys that reads as a number, zero when none does
func Num(m map[string]string, keys ...string) float64 {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return n
		}
		// A list of numbers, such as a per layer sliding window pattern, reads as its largest
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
