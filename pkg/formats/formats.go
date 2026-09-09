// Package formats classifies model files and reads their headers.
package formats

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"regexp"
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

// Loadable set of weights plus attached files
//
// Root is the directory prefix every path in the group shares and the store
// strips when it lays the group out: the weights' directory, or the root the
// format's root pattern captured for a group shaped as a tree. Empty for the
// repository root.
type Group struct {
	FormatID string
	Name     string
	Root     string
	Weights  []*v1.Artifact
	Files    map[v1.ArtifactRole][]*v1.Artifact
}

// Reads headers of one group into raw facts
type Reader interface {
	Read(ctx context.Context, open Opener, group *Group) (*v1.RawModel, error)
}

// Builds a reader from its format spec
type Constructor func(spec *v1.FormatSpec) (Reader, error)

// Constructors keyed by format id
type Constructors map[string]Constructor

// Readers keyed by format id
type Readers map[string]Reader

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
	return &v1.TensorInfo{Name: name, Dtype: label, Elements: elements, Bytes: uint64(math.Ceil(float64(elements) * width))}
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

// Builds readers for every spec whose reader has a constructor
//
// A spec names its reader, so two formats can share one parser, and defaults
// to its own id.
func BuildReaders(specs []*v1.FormatSpec, ctors Constructors) (Readers, error) {
	out := Readers{}
	for _, s := range specs {
		ctor, ok := ctors[readerID(s)]
		if !ok {
			continue
		}
		r, err := ctor(s)
		if err != nil {
			return nil, fmt.Errorf("format %s: %w", s.GetId(), err)
		}
		out[s.GetId()] = r
	}
	return out, nil
}

// Names the reader a format spec parses with
func readerID(s *v1.FormatSpec) string {
	if r := s.GetReader(); r != "" {
		return r
	}
	return s.GetId()
}

type fileRule struct {
	re   *regexp.Regexp
	role v1.ArtifactRole
}

type groupRule struct {
	re    *regexp.Regexp
	value string
}

type compiledFormat struct {
	spec   *v1.FormatSpec
	files  []fileRule
	groups []groupRule
	shards *regexp.Regexp
	root   *regexp.Regexp
}

// Assigns format, role, group, and shard to artifacts
type Classifier struct {
	formats  []*compiledFormat
	byID     map[string]*v1.FormatSpec
	compiled map[string]*compiledFormat
}

// Compiles format specs ordered by priority then id
func NewClassifier(specs []*v1.FormatSpec) (*Classifier, error) {
	c := &Classifier{byID: map[string]*v1.FormatSpec{}, compiled: map[string]*compiledFormat{}}
	for _, s := range specs {
		cf := &compiledFormat{spec: s}
		if s.GetRoot() != "" {
			re, err := regexp.Compile(s.GetRoot())
			if err != nil {
				return nil, fmt.Errorf("format %s root: %w", s.GetId(), err)
			}
			if re.SubexpIndex("root") < 0 {
				return nil, fmt.Errorf("format %s root: pattern needs a root group", s.GetId())
			}
			cf.root = re
		}
		for _, f := range s.GetFiles() {
			re, err := regexp.Compile(f.GetMatch())
			if err != nil {
				return nil, fmt.Errorf("format %s file rule: %w", s.GetId(), err)
			}
			cf.files = append(cf.files, fileRule{re: re, role: f.GetRole()})
		}
		for _, g := range s.GetGroups() {
			re, err := regexp.Compile(g.GetMatch())
			if err != nil {
				return nil, fmt.Errorf("format %s group rule: %w", s.GetId(), err)
			}
			cf.groups = append(cf.groups, groupRule{re: re, value: g.GetValue()})
		}
		if s.GetShards() != "" {
			re, err := regexp.Compile(s.GetShards())
			if err != nil {
				return nil, fmt.Errorf("format %s shards: %w", s.GetId(), err)
			}
			cf.shards = re
		}
		c.formats = append(c.formats, cf)
		c.byID[s.GetId()] = s
		c.compiled[s.GetId()] = cf
	}
	sort.SliceStable(c.formats, func(i, j int) bool {
		a, b := c.formats[i].spec, c.formats[j].spec
		if a.GetPriority() != b.GetPriority() {
			return a.GetPriority() > b.GetPriority()
		}
		return a.GetId() < b.GetId()
	})
	return c, nil
}

// Returns a format spec by id
func (c *Classifier) Spec(id string) *v1.FormatSpec { return c.byID[id] }

// Lists format specs in priority order
func (c *Classifier) Specs() []*v1.FormatSpec {
	out := make([]*v1.FormatSpec, 0, len(c.formats))
	for _, f := range c.formats {
		out = append(out, f.spec)
	}
	return out
}

// Fills classification fields on every artifact in place
//
// Formats claim files in priority order. A weight whose format requires files
// the repository does not carry falls through to the next format that claims
// it, so a lone safetensors checkpoint without a config is not left with a
// format that cannot read it.
func (c *Classifier) Classify(m *v1.Model) {
	for _, a := range m.GetArtifacts() {
		c.classify(a, nil)
	}
	excluded := map[*v1.Artifact]map[string]bool{}
	for range c.formats {
		demoted := false
		for _, g := range c.Groups(m) {
			if len(Missing(c.byID[g.FormatID], g)) == 0 {
				continue
			}
			for _, a := range g.Weights {
				ex := excluded[a]
				if ex == nil {
					ex = map[string]bool{}
					excluded[a] = ex
				}
				ex[g.FormatID] = true
				c.classify(a, ex)
				demoted = true
			}
		}
		if !demoted {
			break
		}
	}
	c.disambiguate(m)
}

// Splits a group whose files are different models rather than shards of one: a repository that
// publishes model-IQ4_XS, model-MTP-IQ4_XS, and model-LOW-MTP-IQ4_XS names every one by the quant
// token, so each is named by what sets it apart from the others instead, IQ4_XS, MTP-IQ4_XS, and
// LOW-MTP-IQ4_XS. Shards of one file share a stem and stay together.
func (c *Classifier) disambiguate(m *v1.Model) {
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
		f := c.compiled[k.format]
		if f == nil {
			continue
		}
		stems := map[string][]*v1.Artifact{}
		var distinct []string
		for _, a := range byKey[k] {
			stem := f.stem(a.GetPath())
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

// The path without its shard suffix, or without its extension when it is not a shard
func (f *compiledFormat) stem(p string) string {
	if f.shards != nil {
		if loc := f.shards.FindStringIndex(p); loc != nil {
			return p[:loc[0]]
		}
	}
	return strings.TrimSuffix(p, path.Ext(p))
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

func (c *Classifier) classify(a *v1.Artifact, exclude map[string]bool) {
	a.FormatId, a.Role, a.Group, a.ShardIndex, a.ShardCount = "", v1.ArtifactRole_ARTIFACT_ROLE_OTHER, "", 0, 0
	for _, f := range c.formats {
		if exclude[f.spec.GetId()] {
			continue
		}
		for _, rule := range f.files {
			if !rule.re.MatchString(a.GetPath()) {
				continue
			}
			a.FormatId, a.Role = f.spec.GetId(), rule.role
			if rule.role == v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS {
				a.Group = f.group(a.GetPath())
				a.ShardIndex, a.ShardCount = f.shard(a.GetPath())
			}
			return
		}
	}
}

func (f *compiledFormat) group(p string) string {
	for _, g := range f.groups {
		m := g.re.FindStringSubmatchIndex(p)
		if m == nil {
			continue
		}
		return string(g.re.ExpandString(nil, g.value, p, m))
	}
	return ""
}

func (f *compiledFormat) shard(p string) (uint32, uint32) {
	if f.shards == nil {
		return 0, 0
	}
	m := f.shards.FindStringSubmatch(p)
	if m == nil {
		return 0, 0
	}
	var index, count uint32
	for i, name := range f.shards.SubexpNames() {
		n, err := strconv.ParseUint(m[i], 10, 32)
		if err != nil {
			continue
		}
		switch name {
		case "index":
			index = uint32(n)
		case "count":
			count = uint32(n)
		}
	}
	return index, count
}

// Names the place a file attaches to: its directory, or the root its format's
// root pattern captures for formats laid out as a tree
func (c *Classifier) attachKey(a *v1.Artifact) string {
	if root, ok := c.root(a); ok {
		return "\x00" + root
	}
	return path.Dir(a.GetPath())
}

// Returns the tree root a path sits under when its format lays groups out as trees
func (c *Classifier) root(a *v1.Artifact) (string, bool) {
	if f := c.compiled[a.GetFormatId()]; f != nil && f.root != nil {
		if m := f.root.FindStringSubmatchIndex(a.GetPath()); m != nil {
			return strings.TrimSuffix(string(f.root.ExpandString(nil, "${root}", a.GetPath(), m)), "/"), true
		}
	}
	return "", false
}

// Groups classified artifacts into loadable weight sets
//
// Files with a role other than weights attach to every group of their format
// that shares their directory, or their root for tree shaped formats.
func (c *Classifier) Groups(m *v1.Model) []*Group {
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
		at := c.attachKey(g.Weights[0])
		if root, ok := c.root(g.Weights[0]); ok {
			g.Root = root
		} else if dir := path.Dir(g.Weights[0].GetPath()); dir != "." {
			g.Root = dir
		}
		for _, a := range m.GetArtifacts() {
			if a.GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS || a.GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_OTHER {
				continue
			}
			if a.GetFormatId() == g.FormatID && c.attachKey(a) == at {
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

// Reports roles a group is missing against a spec
func Missing(spec *v1.FormatSpec, g *Group) []v1.ArtifactRole {
	var missing []v1.ArtifactRole
	for _, role := range spec.GetRequires() {
		if len(g.Files[role]) == 0 {
			missing = append(missing, role)
		}
	}
	return missing
}
