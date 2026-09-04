// Package sources defines where models come from.
//
// There is one client. Each catalog is a file that tells the client where the
// catalog lives, what it accepts, and how its wire format maps onto the shared
// model; the client does everything else. The source list is a row per
// source: a seeded default for every catalog that runs unconfigured, plus
// whatever config or the API added.
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

// Returned when a source definition is invalid
var ErrSource = errors.New("invalid source")

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

// Every source in fallback order, rebuilt whenever a row changes
type Registry struct {
	mu    sync.RWMutex
	order []Source
	byID  map[string]Source
}

// Builds a registry over cfgs, in the given order, so the first is the one
// commands fall back to
func Build(cfgs []*v1.Source) (*Registry, error) {
	r := &Registry{byID: map[string]Source{}}
	if err := r.Reload(cfgs); err != nil {
		return nil, err
	}
	return r, nil
}

// Replaces every source with clients built from cfgs, in the given order
//
// A source whose client cannot be built stays listed and reports the reason
// from every call, so one bad row never hides the rest. The errors here are
// structural: a missing id, a duplicate, or an unknown kind.
func (r *Registry) Reload(cfgs []*v1.Source) error {
	order := make([]Source, 0, len(cfgs))
	byID := make(map[string]Source, len(cfgs))
	for _, cfg := range cfgs {
		if cfg.GetId() == "" {
			return fmt.Errorf("%w: source without id", ErrSource)
		}
		if _, dup := byID[cfg.GetId()]; dup {
			return fmt.Errorf("%w: duplicate source id %q", ErrSource, cfg.GetId())
		}
		cat, ok := catalogs[cfg.GetKind()]
		if !ok {
			return fmt.Errorf("%w: source %s: unsupported kind %s", ErrSource, cfg.GetId(), cfg.GetKind())
		}
		var src Source
		if c, err := newClient(cat, cfg); err != nil {
			src = &broken{spec: cfg, err: fmt.Errorf("source %s: %w", cfg.GetId(), err)}
		} else {
			src = c
		}
		order = append(order, src)
		byID[cfg.GetId()] = src
	}
	r.mu.Lock()
	r.order, r.byID = order, byID
	r.mu.Unlock()
	return nil
}

// Builds a client for spec without keeping it, reporting what is wrong with it
func Check(spec *v1.Source) error {
	cat, ok := catalogs[spec.GetKind()]
	if !ok {
		return fmt.Errorf("%w: unsupported kind %s", ErrSource, spec.GetKind())
	}
	if _, err := newClient(cat, spec); err != nil {
		return fmt.Errorf("%w: %v", ErrSource, err)
	}
	return nil
}

// The default source of every catalog that runs unconfigured, under the
// catalog's name, in kind order
func Seeds() []*v1.Source {
	var out []*v1.Source
	for _, cat := range all() {
		if cat.Configured {
			continue
		}
		out = append(out, &v1.Source{Id: cat.ID, Kind: cat.Kind, Seeded: true})
	}
	return out
}

// Returns a source by id, or the first when empty
func (r *Registry) Get(id string) (Source, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
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
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*v1.Source, 0, len(r.order))
	for _, s := range r.order {
		out = append(out, s.Spec())
	}
	return out
}

// Lists every source with what it can do, or why it cannot
func (r *Registry) Statuses(ctx context.Context) []*v1.SourceStatus {
	r.mu.RLock()
	order := append([]Source{}, r.order...)
	r.mu.RUnlock()
	out := make([]*v1.SourceStatus, len(order))
	var wg sync.WaitGroup
	for i, s := range order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = status(ctx, s)
		}()
	}
	wg.Wait()
	return out
}

// Describes one source with what it can do, or why it cannot
func (r *Registry) Status(ctx context.Context, id string) (*v1.SourceStatus, error) {
	src, err := r.Get(id)
	if err != nil {
		return nil, err
	}
	return status(ctx, src), nil
}

func status(ctx context.Context, s Source) *v1.SourceStatus {
	st := &v1.SourceStatus{Source: s.Spec(), Capabilities: s.Capabilities(ctx)}
	if b, ok := s.(*broken); ok {
		st.Error = b.err.Error()
	}
	return st
}

// A source whose client could not be built, listed so the failure is visible
type broken struct {
	spec *v1.Source
	err  error
}

func (b *broken) Spec() *v1.Source { return b.spec }

func (b *broken) Capabilities(context.Context) *v1.SourceCapabilities {
	return &v1.SourceCapabilities{}
}

func (b *broken) Search(context.Context, *v1.SearchRequest) (*v1.SearchResponse, error) {
	return nil, b.err
}

func (b *broken) Resolve(context.Context, string, string) (*v1.Model, error) { return nil, b.err }

func (b *broken) Revisions(context.Context, string) ([]*v1.Revision, error) { return nil, b.err }

func (b *broken) Card(context.Context, string, string) (*v1.ModelCard, error) { return nil, b.err }

func (b *broken) Open(context.Context, *v1.Model, *v1.Artifact) (Blob, error) { return nil, b.err }
