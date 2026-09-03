// Package formats classifies model files and reads their headers.
package formats

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

const (
	firstChunk = 1 << 20
	maxChunk   = 32 << 20
)

// Reads a blob sequentially in chunks that double in size
type chunkReader struct {
	ra    io.ReaderAt
	size  int64
	off   int64
	chunk int64
	buf   []byte
	pos   int
}

// Wraps a blob so header parsers issue few range reads
func NewChunkReader(ra io.ReaderAt, size int64) io.Reader {
	return &chunkReader{ra: ra, size: size, chunk: firstChunk}
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.buf) {
		if c.off >= c.size {
			return 0, io.EOF
		}
		n := min(c.chunk, c.size-c.off)
		if int64(cap(c.buf)) < n {
			c.buf = make([]byte, n)
		}
		c.buf = c.buf[:n]
		read, err := c.ra.ReadAt(c.buf, c.off)
		if err != nil && err != io.EOF {
			return 0, err
		}
		if read == 0 {
			return 0, io.EOF
		}
		c.buf = c.buf[:read]
		c.off += int64(read)
		c.pos = 0
		c.chunk = min(c.chunk*2, maxChunk)
	}
	n := copy(p, c.buf[c.pos:])
	c.pos += n
	return n, nil
}

// Returned when a weight group cannot be found
var ErrUnknownGroup = errors.New("unknown weight group")

// Opens one artifact for random access
type Opener func(ctx context.Context, a *v1.Artifact) (sources.Blob, error)

// Loadable set of weights plus attached files
type Group struct {
	FormatID string
	Name     string
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

// Builds readers for every spec that has a constructor
func BuildReaders(specs []*v1.FormatSpec, ctors Constructors) (Readers, error) {
	out := Readers{}
	for _, s := range specs {
		ctor, ok := ctors[s.GetId()]
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
}

// Assigns format, role, group, and shard to artifacts
type Classifier struct {
	formats []*compiledFormat
	byID    map[string]*v1.FormatSpec
}

// Compiles format specs ordered by priority then id
func NewClassifier(specs []*v1.FormatSpec) (*Classifier, error) {
	c := &Classifier{byID: map[string]*v1.FormatSpec{}}
	for _, s := range specs {
		cf := &compiledFormat{spec: s}
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

// Fills classification fields on every artifact in place
func (c *Classifier) Classify(m *v1.Model) {
	for _, a := range m.GetArtifacts() {
		c.classify(a)
	}
}

func (c *Classifier) classify(a *v1.Artifact) {
	a.FormatId, a.Role, a.Group, a.ShardIndex, a.ShardCount = "", v1.ArtifactRole_ARTIFACT_ROLE_OTHER, "", 0, 0
	for _, f := range c.formats {
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

// Groups classified artifacts into loadable weight sets
func Groups(m *v1.Model) []*Group {
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
		dir := path.Dir(g.Weights[0].GetPath())
		for _, a := range m.GetArtifacts() {
			if a.GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS || a.GetRole() == v1.ArtifactRole_ARTIFACT_ROLE_OTHER {
				continue
			}
			if a.GetFormatId() == g.FormatID && path.Dir(a.GetPath()) == dir {
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
