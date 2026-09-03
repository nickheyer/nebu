// Package spec loads data driven definitions into proto messages.
package spec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/yaml"
)

// Every spec kind nebu ships or a site adds
type Catalog struct {
	Probes   []*v1.ProbeSpec
	Formats  []*v1.FormatSpec
	Archs    []*v1.ArchSpec
	Runtimes []*v1.RuntimeManifest
	Triage   []*v1.TriageSpec
}

// Message with a stable id
type identified interface {
	proto.Message
	GetId() string
}

// Loads layered spec filesystems, later layers override by id
func Load(layers ...fs.FS) (*Catalog, error) {
	c := &Catalog{}
	for _, fsys := range layers {
		var err error
		if c.Probes, err = loadDir(fsys, "probes", c.Probes, func() *v1.ProbeSpec { return &v1.ProbeSpec{} }); err != nil {
			return nil, err
		}
		if c.Formats, err = loadDir(fsys, "formats", c.Formats, func() *v1.FormatSpec { return &v1.FormatSpec{} }); err != nil {
			return nil, err
		}
		if c.Archs, err = loadDir(fsys, "archs", c.Archs, func() *v1.ArchSpec { return &v1.ArchSpec{} }); err != nil {
			return nil, err
		}
		if c.Runtimes, err = loadDir(fsys, "runtimes", c.Runtimes, func() *v1.RuntimeManifest { return &v1.RuntimeManifest{} }); err != nil {
			return nil, err
		}
		if c.Triage, err = loadDir(fsys, "triage", c.Triage, func() *v1.TriageSpec { return &v1.TriageSpec{} }); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Returns directory layers that exist on disk
func Dirs(paths []string) []fs.FS {
	var out []fs.FS
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			out = append(out, os.DirFS(p))
		}
	}
	return out
}

// Decodes YAML or JSON into a proto message
func Decode(data []byte, msg proto.Message) error {
	js, err := yaml.YAMLToJSON(data)
	if err != nil {
		return fmt.Errorf("yaml: %w", err)
	}
	if err := protojson.Unmarshal(js, msg); err != nil {
		return fmt.Errorf("decode %s: %w", msg.ProtoReflect().Descriptor().Name(), err)
	}
	return nil
}

// Encodes a proto message as YAML
func Encode(msg proto.Message) ([]byte, error) {
	js, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return yaml.JSONToYAML(js)
}

func loadDir[T identified](fsys fs.FS, dir string, into []T, newT func() T) ([]T, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return into, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !(strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
			continue
		}
		file := path.Join(dir, name)
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, err
		}
		msg := newT()
		if err := Decode(data, msg); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if msg.GetId() == "" {
			return nil, fmt.Errorf("%s: missing id", file)
		}
		into = upsert(into, msg)
	}
	return into, nil
}

func upsert[T identified](list []T, msg T) []T {
	for i, existing := range list {
		if existing.GetId() == msg.GetId() {
			list[i] = msg
			return list
		}
	}
	return append(list, msg)
}
