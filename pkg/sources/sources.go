// Package sources defines where models come from.
//
// There is one client. Each catalog is a file that tells the client where the
// catalog lives, what it accepts, and how its wire format maps onto the shared
// model; the client does everything else. The source list is the set of those
// files, plus the directories and mirrors config names.
package sources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Returned when a source id is not configured
var ErrUnknownSource = errors.New("unknown source")

// Returned when a source cannot do what was asked
var ErrUnsupported = errors.New("not supported by this source")

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

// Catalog that can browse, search, resolve, and open artifacts
//
// Every source, from the Hub to a directory on disk, answers the same
// SearchRequest and describes what it accepts through Capabilities, so one
// interface renders all of them. A source that keeps variants under one name,
// such as an image tag or a version, returns a Model whose Repo names the
// variant so stored groups never collide. Revisions and Card answer
// ErrUnsupported on a source that has neither.
type Source interface {
	Spec() *v1.Source
	Capabilities(ctx context.Context) *v1.SourceCapabilities
	Search(ctx context.Context, req *v1.SearchRequest) (*v1.SearchResponse, error)
	Resolve(ctx context.Context, repo, revision string) (*v1.Model, error)
	Revisions(ctx context.Context, repo string) ([]*v1.Revision, error)
	Card(ctx context.Context, repo, revision string) (*v1.ModelCard, error)
	Open(ctx context.Context, model *v1.Model, artifact *v1.Artifact) (Blob, error)
}

// Every source: the directories and mirrors config names, then every implemented catalog
type Registry struct {
	order []Source
	byID  map[string]Source
}

// Builds every source. Config entries come first, in config order, so the
// first is the one commands fall back to; they can only name the kinds that
// need config. Every implemented catalog follows, in kind order.
func Build(cfgs []*v1.Source) (*Registry, error) {
	r := &Registry{byID: map[string]Source{}}
	for _, cfg := range cfgs {
		if cfg.GetId() == "" {
			return nil, fmt.Errorf("source without id")
		}
		if _, dup := r.byID[cfg.GetId()]; dup {
			return nil, fmt.Errorf("duplicate source id %q", cfg.GetId())
		}
		cat, ok := catalogs[cfg.GetKind()]
		if !ok {
			return nil, fmt.Errorf("source %s: unsupported kind %s", cfg.GetId(), cfg.GetKind())
		}
		if !cat.Configured {
			return nil, fmt.Errorf("source %s: %s is built in and takes no config", cfg.GetId(), cat.ID)
		}
		c, err := newClient(cat, cfg)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", cfg.GetId(), err)
		}
		r.add(c)
	}
	for _, cat := range all() {
		if cat.Configured {
			continue
		}
		if _, taken := r.byID[cat.ID]; taken {
			return nil, fmt.Errorf("source id %q belongs to a built in catalog", cat.ID)
		}
		c, err := newClient(cat, &v1.Source{Id: cat.ID, Kind: cat.Kind, Endpoint: cat.Endpoint})
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", cat.ID, err)
		}
		r.add(c)
	}
	return r, nil
}

func (r *Registry) add(src Source) {
	r.order = append(r.order, src)
	r.byID[src.Spec().GetId()] = src
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

// Lists every source with what it can do
func (r *Registry) Statuses(ctx context.Context) []*v1.SourceStatus {
	out := make([]*v1.SourceStatus, len(r.order))
	var wg sync.WaitGroup
	for i, s := range r.order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = &v1.SourceStatus{Source: s.Spec(), Capabilities: s.Capabilities(ctx)}
		}()
	}
	wg.Wait()
	return out
}
