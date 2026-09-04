// Package sources defines where models come from.
//
// A transport moves bytes and listings, one Go type per protocol behind one
// interface. A provider is a catalog: where it lives, which transports it
// uses, what it accepts, and how its wire format maps onto the shared model.
// A source is a row configuring one provider. The source list is a row per
// source: a seeded default for every provider that runs unconfigured, plus
// whatever config or the API added.
package sources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"google.golang.org/protobuf/proto"
	"io"
	"os"
	"strings"
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

// Blob that lands as a whole file on this host, already there or moved by
// a transport that cannot serve ranges, reporting bytes as they move
type Materializer interface {
	Materialize(ctx context.Context, progress func(delta int64)) (string, error)
}

type fileBlob struct {
	*os.File
	size   int64
	remove bool
}

func (f *fileBlob) Size() int64 { return f.size }

func (f *fileBlob) Materialize(context.Context, func(int64)) (string, error) { return f.Name(), nil }

func (f *fileBlob) Range(ctx context.Context, off, length int64) (io.ReadCloser, error) {
	return io.NopCloser(io.NewSectionReader(f.File, off, length)), nil
}

// Closes the file, removing it when it was a transport's scratch copy
func (f *fileBlob) Close() error {
	err := f.File.Close()
	if f.remove {
		os.Remove(f.Name())
	}
	return err
}

// Opens a local file as a blob
func OpenFile(path string) (Blob, error) {
	return openFile(path, false)
}

func openFile(path string, remove bool) (Blob, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &fileBlob{File: f, size: info.Size(), remove: remove}, nil
}

// A blob that serves ranges through one transport and lands whole through another
type rangedWhole struct {
	Blob
	whole Blob
}

func (r *rangedWhole) Materialize(ctx context.Context, progress func(int64)) (string, error) {
	m, ok := r.whole.(Materializer)
	if !ok {
		return "", fmt.Errorf("%T cannot land as a file", r.whole)
	}
	return m.Materialize(ctx, progress)
}

func (r *rangedWhole) Range(ctx context.Context, off, length int64) (io.ReadCloser, error) {
	return RangeOf(ctx, r.Blob, off, length)
}

func (r *rangedWhole) Close() error {
	r.whole.Close()
	return r.Blob.Close()
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
	// Directory transports keep clones and scratch downloads under
	CacheDir string

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
		if c, err := newClient(cat, cfg, r.CacheDir); err != nil {
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
func (r *Registry) Check(spec *v1.Source) error {
	cat, ok := catalogs[spec.GetKind()]
	if !ok {
		return fmt.Errorf("%w: unsupported kind %s", ErrSource, spec.GetKind())
	}
	if _, err := newClient(cat, spec, r.CacheDir); err != nil {
		return fmt.Errorf("%w: %v", ErrSource, err)
	}
	return nil
}

// The default source of every provider that runs unconfigured, under the
// provider's name, in kind order
func Seeds() []*v1.Source {
	var out []*v1.Source
	for _, cat := range all() {
		if cat.Configured {
			continue
		}
		out = append(out, &v1.Source{Id: cat.ID, Kind: cat.Kind, Name: cat.seedName(), Seeded: true})
	}
	return out
}

// Every provider with the settings its sources accept, in kind order
func Providers() []*v1.Provider {
	var out []*v1.Provider
	for _, cat := range all() {
		out = append(out, cat.Provider())
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

// Lists the sources of one provider in order
func (r *Registry) OfKind(kind v1.SourceKind) []Source {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Source
	for _, s := range r.order {
		if s.Spec().GetKind() == kind {
			out = append(out, s)
		}
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
	caps := &v1.SourceCapabilities{}
	if cat, ok := catalogs[b.spec.GetKind()]; ok {
		caps.Name, caps.Description, caps.Fields, caps.Transports, caps.HiddenTags = cat.Name, cat.Description, cat.Fields(), cat.transportNames(), cat.Noise
	}
	return caps
}

func (b *broken) Search(context.Context, *v1.SearchRequest) (*v1.SearchResponse, error) {
	return nil, b.err
}

func (b *broken) Resolve(context.Context, string, string) (*v1.Model, error) { return nil, b.err }

func (b *broken) Revisions(context.Context, string) ([]*v1.Revision, error) { return nil, b.err }

func (b *broken) Card(context.Context, string, string) (*v1.ModelCard, error) { return nil, b.err }

func (b *broken) Open(context.Context, *v1.Model, *v1.Artifact) (Blob, error) { return nil, b.err }

// Searches one source, every source of a provider, or every source there is
//
// The cursor of a fanned out search is the set of per source cursors, so a
// source that ran out drops from the next page and the rest continue where
// they were. A source that fails leaves a warning rather than failing the page,
// unless every one failed.
func (r *Registry) Search(ctx context.Context, req *v1.SearchRequest) (*v1.SearchResponse, error) {
	if req.GetSourceId() != "" {
		src, err := r.Get(req.GetSourceId())
		if err != nil {
			return nil, err
		}
		return r.searchOne(ctx, src, req)
	}
	var srcs []Source
	if req.GetKind() != v1.SourceKind_SOURCE_KIND_UNSPECIFIED {
		srcs = r.OfKind(req.GetKind())
		if len(srcs) == 0 {
			return nil, fmt.Errorf("%w: no source of kind %s", ErrUnknownSource, req.GetKind())
		}
	} else {
		for _, spec := range r.List() {
			if src, err := r.Get(spec.GetId()); err == nil {
				srcs = append(srcs, src)
			}
		}
		if len(srcs) == 0 {
			return nil, fmt.Errorf("%w: no sources", ErrUnknownSource)
		}
	}
	cursors := map[string]string{}
	if req.GetCursor() != "" {
		data, err := base64.RawURLEncoding.DecodeString(req.GetCursor())
		if err != nil || json.Unmarshal(data, &cursors) != nil {
			return nil, fmt.Errorf("%w: cursor %q is not one this search issued", ErrSource, req.GetCursor())
		}
	} else {
		for _, src := range srcs {
			cursors[src.Spec().GetId()] = ""
		}
	}
	type page struct {
		id   string
		req  *v1.SearchRequest
		resp *v1.SearchResponse
		err  error
	}
	pages := make([]page, 0, len(srcs))
	for _, src := range srcs {
		if cursor, ok := cursors[src.Spec().GetId()]; ok {
			sub := proto.Clone(req).(*v1.SearchRequest)
			sub.SourceId, sub.Kind, sub.Cursor = src.Spec().GetId(), v1.SourceKind_SOURCE_KIND_UNSPECIFIED, cursor
			pages = append(pages, page{id: src.Spec().GetId(), req: sub})
		}
	}
	var wg sync.WaitGroup
	for i := range pages {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src, err := r.Get(pages[i].id)
			if err != nil {
				pages[i].err = err
				return
			}
			pages[i].resp, pages[i].err = r.searchOne(ctx, src, pages[i].req)
		}()
	}
	wg.Wait()
	out := &v1.SearchResponse{}
	next := map[string]string{}
	var failed []string
	for _, p := range pages {
		if p.err != nil {
			failed = append(failed, p.id+": "+p.err.Error())
			out.Warnings = append(out.Warnings, p.id+" did not answer: "+p.err.Error())
			continue
		}
		out.Total += p.resp.GetTotal()
		out.Warnings = append(out.Warnings, p.resp.GetWarnings()...)
		if p.resp.GetNextCursor() != "" {
			next[p.id] = p.resp.GetNextCursor()
		}
	}
	if len(failed) == len(pages) {
		return nil, fmt.Errorf("%s", strings.Join(failed, "; "))
	}
	for i := 0; ; i++ {
		added := false
		for _, p := range pages {
			if p.err == nil && i < len(p.resp.GetHits()) {
				out.Hits = append(out.Hits, p.resp.GetHits()[i])
				added = true
			}
		}
		if !added {
			break
		}
	}
	if len(next) > 0 {
		data, err := json.Marshal(next)
		if err != nil {
			return nil, err
		}
		out.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return out, nil
}

// Searches one source, stamping its id on every hit
func (r *Registry) searchOne(ctx context.Context, src Source, req *v1.SearchRequest) (*v1.SearchResponse, error) {
	resp, err := src.Search(ctx, req)
	if err != nil {
		return nil, err
	}
	for _, h := range resp.GetHits() {
		if h.SourceId == "" {
			h.SourceId = src.Spec().GetId()
		}
	}
	return resp, nil
}
