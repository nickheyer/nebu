package services

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/formats/all"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
)

// A provider that lists fixed hits by offset and applies no filter, as catalogs without format or
// kind parameters do
type listingSource struct {
	id   string
	hits []*v1.SearchHit
	mu   sync.Mutex
	seen []*v1.SearchRequest
}

func (s *listingSource) Spec() *v1.Source {
	return &v1.Source{Id: s.id, Kind: v1.SourceKind_SOURCE_KIND_LOCAL}
}
func (s *listingSource) Capabilities(context.Context) *v1.SourceCapabilities {
	return &v1.SourceCapabilities{Browse: true, Search: true, Paginate: true}
}
func (s *listingSource) Search(_ context.Context, req *v1.SearchRequest) (*v1.SearchResponse, error) {
	s.mu.Lock()
	s.seen = append(s.seen, req)
	s.mu.Unlock()
	return sources.Page(s.hits, req, sources.Limit(req, 30, 100)), nil
}
func (s *listingSource) Resolve(context.Context, string, string) (*v1.Model, error) {
	return nil, sources.ErrUnsupported
}
func (s *listingSource) Revisions(context.Context, string) ([]*v1.Revision, error) {
	return nil, sources.ErrUnsupported
}
func (s *listingSource) Card(context.Context, string, string) (*v1.ModelCard, error) {
	return nil, sources.ErrUnsupported
}
func (s *listingSource) Open(context.Context, *v1.Model, *v1.Artifact) (sources.Blob, error) {
	return nil, sources.ErrUnsupported
}

func hit(repo, library string, tags ...string) *v1.SearchHit {
	return &v1.SearchHit{Repo: repo, Library: library, Tags: tags}
}

func newCatalog(t *testing.T, srcs ...sources.Source) *SourceService {
	t.Helper()
	fmts, err := all.Registry()
	if err != nil {
		t.Fatal(err)
	}
	rts, err := runtimes.New(runtimes.All())
	if err != nil {
		t.Fatal(err)
	}
	return NewSourceService(&sources.Manager{Registry: sources.NewRegistry(srcs...)}, nil, fmts, rts)
}

func repos(resp *v1.SearchResponse) string {
	var out []string
	for _, h := range resp.GetHits() {
		out = append(out, h.GetRepo())
	}
	return strings.Join(out, ",")
}

// A runtime filter reaches providers as the formats and kind it serves, and the daemon fetches
// pages until it can fill one with matching hits.
func TestSearchFillsPagesForRuntime(t *testing.T) {
	src := &listingSource{id: "hub", hits: []*v1.SearchHit{
		hit("a/one", "gguf", "gguf"),
		hit("a/sd", "diffusers", "text-to-image", "safetensors"),
		hit("a/hf", "transformers", "safetensors"),
		hit("a/two", "gguf", "gguf"),
		hit("a/blank", ""),
		hit("a/sdxl", "diffusers", "text-to-image", "safetensors"),
		hit("a/three", "gguf", "gguf"),
	}}
	svc := newCatalog(t, src)
	search := func(filters map[string]string, limit uint32, cursor string) *v1.SearchResponse {
		t.Helper()
		resp, err := svc.Search(context.Background(), connect.NewRequest(&v1.SearchRequest{SourceId: "hub", Limit: limit, Cursor: cursor, Filters: filters}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg
	}
	page := search(map[string]string{sources.FacetRuntime: "llamacpp"}, 2, "")
	if repos(page) != "a/one,a/two" || page.GetNextCursor() != strconv.Itoa(4) || page.GetTotal() != 0 {
		t.Fatalf("first page %s next %q total %d", repos(page), page.GetNextCursor(), page.GetTotal())
	}
	if len(src.seen) != 2 {
		t.Fatalf("two provider pages fill one page of two, got %d", len(src.seen))
	}
	for _, req := range src.seen {
		if req.GetFilters()[sources.FacetFormat] != "gguf" || req.GetFilters()[sources.FacetKind] != "language" || req.GetFilters()[sources.FacetRuntime] != "" {
			t.Fatalf("providers see the runtime as its formats and kind, got %v", req.GetFilters())
		}
	}
	page = search(map[string]string{sources.FacetRuntime: "llamacpp"}, 2, page.GetNextCursor())
	if repos(page) != "a/three" || page.GetNextCursor() != "" {
		t.Fatalf("last page %s next %q", repos(page), page.GetNextCursor())
	}
	// Every hit is stamped with what runs it.
	for _, h := range page.GetHits() {
		if h.GetKind() != v1.ModelKind_MODEL_KIND_LANGUAGE || strings.Join(h.GetFormats(), ",") != "gguf" || h.GetRuntimes() == 0 {
			t.Fatalf("stamp %+v", h)
		}
	}
	// Formats and kinds outside the runtime ask for nothing.
	src.seen = nil
	if page := search(map[string]string{sources.FacetRuntime: "llamacpp", sources.FacetFormat: "safetensors"}, 2, ""); len(page.GetHits()) != 0 || len(src.seen) != 0 {
		t.Fatalf("a contradiction fetches nothing, got %v after %d requests", page, len(src.seen))
	}
	if page := search(map[string]string{sources.FacetRuntime: "sdcpp", sources.FacetKind: "language"}, 2, ""); len(page.GetHits()) != 0 || len(src.seen) != 0 {
		t.Fatalf("a contradiction fetches nothing, got %v after %d requests", page, len(src.seen))
	}
	// A runtime with several formats narrows explicit ones to those it serves.
	page = search(map[string]string{sources.FacetRuntime: "sdcpp", sources.FacetFormat: "diffusers,gguf,nemo"}, 5, "")
	if repos(page) != "a/sd,a/sdxl" || src.seen[0].GetFilters()[sources.FacetFormat] != "diffusers,gguf" || src.seen[0].GetFilters()[sources.FacetKind] != "diffusion" {
		t.Fatalf("sdcpp page %s filters %v", repos(page), src.seen[0].GetFilters())
	}
	// Unfiltered listings keep the provider's count.
	if page := search(nil, 3, ""); len(page.GetHits()) != 3 || page.GetTotal() != 7 || page.GetNextCursor() != "3" {
		t.Fatalf("plain page %v", page)
	}
	if _, err := svc.Search(context.Background(), connect.NewRequest(&v1.SearchRequest{SourceId: "hub", Filters: map[string]string{sources.FacetRuntime: "bogus"}})); err == nil {
		t.Fatal("an unknown runtime is refused")
	}
	if _, err := svc.Search(context.Background(), connect.NewRequest(&v1.SearchRequest{SourceId: "hub", Filters: map[string]string{sources.FacetKind: "audio"}})); err == nil {
		t.Fatal("an unknown kind is refused")
	}
}
