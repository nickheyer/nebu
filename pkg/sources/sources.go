// Package sources defines where models come from.
package sources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Returned when a source id is not configured
var ErrUnknownSource = errors.New("unknown source")

// Random access handle on one artifact
type Blob interface {
	io.ReaderAt
	io.Closer
	Size() int64
}

// Blob that can stream a byte range without buffering it
type Ranger interface {
	Range(ctx context.Context, off, length int64) (io.ReadCloser, error)
}

// Blob backed by a local file
type Pather interface {
	Path() string
}

type fileBlob struct {
	*os.File
	size int64
}

func (f *fileBlob) Size() int64 { return f.size }

func (f *fileBlob) Path() string { return f.Name() }

func (f *fileBlob) Range(ctx context.Context, off, length int64) (io.ReadCloser, error) {
	return io.NopCloser(io.NewSectionReader(f.File, off, length)), nil
}

// Opens a local file as a blob
func OpenFile(path string) (Blob, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &fileBlob{File: f, size: info.Size()}, nil
}

// Streams a range, falling back to buffered reads
func RangeOf(ctx context.Context, b Blob, off, length int64) (io.ReadCloser, error) {
	if r, ok := b.(Ranger); ok {
		return r.Range(ctx, off, length)
	}
	return io.NopCloser(io.NewSectionReader(b, off, length)), nil
}

// Catalog that can search, resolve, and open artifacts
type Source interface {
	Spec() *v1.Source
	Search(ctx context.Context, query string, tags []string, limit int) ([]*v1.SearchHit, error)
	Resolve(ctx context.Context, repo, revision string) (*v1.Model, error)
	Open(ctx context.Context, model *v1.Model, artifact *v1.Artifact) (Blob, error)
}

// Builds a source from its config
type Constructor func(cfg *v1.Source) (Source, error)

// Constructors keyed by source kind
type Constructors map[v1.SourceKind]Constructor

// Configured sources in config order
type Registry struct {
	order []Source
	byID  map[string]Source
}

// Builds every configured source
func Build(cfgs []*v1.Source, ctors Constructors) (*Registry, error) {
	r := &Registry{byID: map[string]Source{}}
	for _, cfg := range cfgs {
		if cfg.GetId() == "" {
			return nil, fmt.Errorf("source without id")
		}
		if _, dup := r.byID[cfg.GetId()]; dup {
			return nil, fmt.Errorf("duplicate source id %q", cfg.GetId())
		}
		ctor, ok := ctors[cfg.GetKind()]
		if !ok {
			return nil, fmt.Errorf("source %s: unsupported kind %s", cfg.GetId(), cfg.GetKind())
		}
		src, err := ctor(cfg)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", cfg.GetId(), err)
		}
		r.order = append(r.order, src)
		r.byID[cfg.GetId()] = src
	}
	return r, nil
}

// Returns a source by id, or the first when empty
func (r *Registry) Get(id string) (Source, error) {
	if id == "" {
		if len(r.order) == 0 {
			return nil, fmt.Errorf("no sources configured")
		}
		return r.order[0], nil
	}
	src, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownSource, id)
	}
	return src, nil
}

// Lists source configs in order
func (r *Registry) List() []*v1.Source {
	out := make([]*v1.Source, 0, len(r.order))
	for _, s := range r.order {
		out = append(out, s.Spec())
	}
	return out
}
