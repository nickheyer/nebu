package sources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// A source with fixed capabilities and one page of hits
type stubSource struct {
	id   string
	caps *v1.SourceCapabilities
	hits []string
}

func (s *stubSource) Spec() *v1.Source {
	return &v1.Source{Id: s.id, Kind: v1.SourceKind_SOURCE_KIND_LOCAL}
}
func (s *stubSource) Capabilities(context.Context) *v1.SourceCapabilities { return s.caps }
func (s *stubSource) Search(context.Context, *v1.SearchRequest) (*v1.SearchResponse, error) {
	resp := &v1.SearchResponse{}
	for _, h := range s.hits {
		resp.Hits = append(resp.Hits, &v1.SearchHit{Repo: h})
	}
	return resp, nil
}
func (s *stubSource) Resolve(context.Context, string, string) (*v1.Model, error) {
	return nil, ErrUnsupported
}
func (s *stubSource) Revisions(context.Context, string) ([]*v1.Revision, error) {
	return nil, ErrUnsupported
}
func (s *stubSource) Card(context.Context, string, string) (*v1.ModelCard, error) {
	return nil, ErrUnsupported
}
func (s *stubSource) Open(context.Context, *v1.Model, *v1.Artifact) (Blob, error) {
	return nil, ErrUnsupported
}

func TestFanOutSkipsSourcesThatCannotAnswer(t *testing.T) {
	lists := &stubSource{id: "lists", caps: &v1.SourceCapabilities{Browse: true, Search: true}, hits: []string{"a/one"}}
	typed := &stubSource{id: "typed", caps: &v1.SourceCapabilities{Browse: false, Search: false}, hits: []string{"b/two"}}
	dead := &broken{spec: &v1.Source{Id: "dead", Kind: v1.SourceKind_SOURCE_KIND_LOCAL}, err: errors.New("no path")}
	r := &Registry{byID: map[string]Source{}}
	for _, s := range []Source{lists, typed, dead} {
		r.order = append(r.order, s)
		r.byID[s.Spec().GetId()] = s
	}
	resp, err := r.Search(context.Background(), &v1.SearchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetHits()) != 1 || resp.GetHits()[0].GetSourceId() != "lists" || len(resp.GetWarnings()) != 0 {
		t.Fatalf("only the listing source should answer, got %v warnings %v", resp.GetHits(), resp.GetWarnings())
	}
	r.order, r.byID = []Source{typed, dead}, map[string]Source{"typed": typed, "dead": dead}
	_, err = r.Search(context.Background(), &v1.SearchRequest{Query: "x"})
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "searches") {
		t.Fatalf("nothing able should say so, got %v", err)
	}
}

var errUpstream = errors.New("upstream down")

// A source answering from pages keyed by filters and cursor, failing where the page is nil
type tableSource struct {
	stubSource
	pages map[string]*v1.SearchResponse
	mu    sync.Mutex
	reqs  []*v1.SearchRequest
}

// A browsable source with the given facets answering from pages
func newTable(id string, pages map[string]*v1.SearchResponse, facets ...*v1.Facet) *tableSource {
	return &tableSource{stubSource: stubSource{id: id, caps: &v1.SourceCapabilities{Browse: true, Search: true, Facets: facets}}, pages: pages}
}

// Filters as sorted key=value pairs joined by ampersands, then a hash and the cursor
func tableKey(req *v1.SearchRequest) string {
	var parts []string
	for k, v := range req.GetFilters() {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, "&") + "#" + req.GetCursor()
}

func (s *tableSource) Search(_ context.Context, req *v1.SearchRequest) (*v1.SearchResponse, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, proto.Clone(req).(*v1.SearchRequest))
	s.mu.Unlock()
	resp, ok := s.pages[tableKey(req)]
	if !ok {
		return nil, fmt.Errorf("no page for %s", tableKey(req))
	}
	if resp == nil {
		return nil, errUpstream
	}
	return proto.Clone(resp).(*v1.SearchResponse), nil
}

// Returns the key of every request so far in sorted order and forgets them
func (s *tableSource) calls() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, req := range s.reqs {
		out = append(out, tableKey(req))
	}
	sort.Strings(out)
	s.reqs = nil
	return strings.Join(out, " ")
}

func hit(repo string, downloads uint64) *v1.SearchHit {
	return &v1.SearchHit{Repo: repo, Downloads: downloads}
}

// Lists hits as their source and repo joined by a colon in response order
func hitList(resp *v1.SearchResponse) string {
	var out []string
	for _, h := range resp.GetHits() {
		out = append(out, h.GetSourceId()+":"+h.GetRepo())
	}
	return strings.Join(out, ",")
}

// Reads a merged cursor back into its map of upstream cursors
func cursorMap(t *testing.T, cursor string) map[string]string {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("cursor %q %v", cursor, err)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("cursor %q %v", cursor, err)
	}
	return out
}

func TestFanOutUnionsTheValuesOfASingleValuedFacet(t *testing.T) {
	ctx := context.Background()
	src := newTable("s", map[string]*v1.SearchResponse{
		"task=a#": {Hits: []*v1.SearchHit{hit("x/shared", 50), hit("x/a", 10)}, Total: 2, Warnings: []string{"from a"}},
		"task=b#": {Hits: []*v1.SearchHit{hit("x/shared", 50), hit("x/b", 30)}, Total: 5, Warnings: []string{"from b"}},
	}, NewFacet(FacetTask, "Task", false))
	r := NewRegistry(src)
	req := &v1.SearchRequest{SourceId: "s", Filters: map[string]string{FacetTask: "a, b,a"}}
	resp, err := r.Search(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := hitList(resp); got != "s:x/shared,s:x/a,s:x/b" {
		t.Fatalf("round robin union without repeats, got %s", got)
	}
	if resp.GetTotal() != 7 || strings.Join(resp.GetWarnings(), ",") != "from a,from b" || resp.GetNextCursor() != "" {
		t.Fatalf("total %d warnings %v cursor %q", resp.GetTotal(), resp.GetWarnings(), resp.GetNextCursor())
	}
	if got := src.calls(); got != "task=a# task=b#" {
		t.Fatalf("one call per distinct value, got %s", got)
	}
	if req.GetFilters()[FacetTask] != "a, b,a" {
		t.Fatalf("the caller's request is left alone, got %v", req.GetFilters())
	}
	req.Sort = SortDownloads
	if resp, err = r.Search(ctx, req); err != nil || hitList(resp) != "s:x/shared,s:x/b,s:x/a" {
		t.Fatalf("merged by the sort %v %v", resp, err)
	}
}

func TestFanOutCrossesEverySingleValuedFacet(t *testing.T) {
	pages := map[string]*v1.SearchResponse{}
	var want []string
	for _, task := range []string{"a", "b"} {
		for _, lib := range []string{"x", "y"} {
			key := "author=me,you&library=" + lib + "&task=" + task + "&type=LORA,Checkpoint#"
			pages[key] = &v1.SearchResponse{Hits: []*v1.SearchHit{hit("r/"+task+lib, 1)}}
			want = append(want, key)
		}
	}
	sort.Strings(want)
	src := newTable("s", pages, NewFacet(FacetTask, "Task", false), NewFacet(FacetLibrary, "Library", false), NewFacet(FacetType, "Type", true), Freeform(FacetAuthor, "Author"))
	resp, err := NewRegistry(src).Search(context.Background(), &v1.SearchRequest{SourceId: "s", Query: "q", Limit: 7, Filters: map[string]string{
		FacetTask: "a,b", FacetLibrary: "x,y", FacetType: "LORA,Checkpoint", FacetAuthor: "me,you",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := hitList(resp); got != "s:r/ax,s:r/ay,s:r/bx,s:r/by" {
		t.Fatalf("one page per combination in order, got %s", got)
	}
	for _, req := range src.reqs {
		if req.GetQuery() != "q" || req.GetLimit() != 7 || req.GetSourceId() != "s" {
			t.Fatalf("other fields pass through, got %v", req)
		}
	}
	if got := src.calls(); got != strings.Join(want, " ") {
		t.Fatalf("four combinations, got %s", got)
	}
}

func TestFanOutPassesMultiValuedFacetsThrough(t *testing.T) {
	src := newTable("s", map[string]*v1.SearchResponse{
		"format=gguf,safetensors&type=LORA,Checkpoint#": {Hits: []*v1.SearchHit{hit("x/one", 1)}, NextCursor: "p2"},
	}, NewFacet(FacetType, "Type", true))
	resp, err := NewRegistry(src).Search(context.Background(), &v1.SearchRequest{SourceId: "s", Filters: map[string]string{FacetType: "LORA,Checkpoint", FacetFormat: "gguf,safetensors"}})
	if err != nil {
		t.Fatal(err)
	}
	if hitList(resp) != "s:x/one" || resp.GetNextCursor() != "p2" {
		t.Fatalf("one untouched call, got %v", resp)
	}
	if got := src.calls(); got != "format=gguf,safetensors&type=LORA,Checkpoint#" {
		t.Fatalf("calls %s", got)
	}
}

func TestFanOutPagesOnlyCombinationsWithMore(t *testing.T) {
	ctx := context.Background()
	src := newTable("s", map[string]*v1.SearchResponse{
		"task=a#":    {Hits: []*v1.SearchHit{hit("x/a1", 1)}, NextCursor: "a-2"},
		"task=b#":    {Hits: []*v1.SearchHit{hit("x/b1", 1)}},
		"task=a#a-2": {Hits: []*v1.SearchHit{hit("x/a2", 1)}},
	}, NewFacet(FacetTask, "Task", false))
	r := NewRegistry(src)
	req := &v1.SearchRequest{SourceId: "s", Filters: map[string]string{FacetTask: "a,b"}}
	resp, err := r.Search(ctx, req)
	if err != nil || hitList(resp) != "s:x/a1,s:x/b1" {
		t.Fatalf("first page %v %v", resp, err)
	}
	if got := cursorMap(t, resp.GetNextCursor()); !maps.Equal(got, map[string]string{"task=a": "a-2"}) {
		t.Fatalf("only the combination with more stays, got %v", got)
	}
	if got := src.calls(); got != "task=a# task=b#" {
		t.Fatalf("first calls %s", got)
	}
	req.Cursor = resp.GetNextCursor()
	resp, err = r.Search(ctx, req)
	if err != nil || hitList(resp) != "s:x/a2" || resp.GetNextCursor() != "" {
		t.Fatalf("second page %v %v", resp, err)
	}
	if got := src.calls(); got != "task=a#a-2" {
		t.Fatalf("second calls %s", got)
	}
	unknown, _ := json.Marshal(map[string]string{"task=c": "z"})
	for _, cursor := range []string{"a-2", "!", base64.RawURLEncoding.EncodeToString(unknown)} {
		req.Cursor = cursor
		if _, err := r.Search(ctx, req); !errors.Is(err, ErrSource) || !strings.Contains(err.Error(), "not one this search issued") {
			t.Fatalf("cursor %q is refused, got %v", cursor, err)
		}
	}
	if got := src.calls(); got != "" {
		t.Fatalf("a refused cursor reaches no upstream, got %s", got)
	}
}

// A source holding each search until a full set of workers is in flight, recording the most at once
type gateSource struct {
	stubSource
	mu   sync.Mutex
	now  int
	most int
	once sync.Once
	full chan struct{}
}

func (s *gateSource) Search(_ context.Context, req *v1.SearchRequest) (*v1.SearchResponse, error) {
	s.mu.Lock()
	s.now++
	s.most = max(s.most, s.now)
	if s.now == fanWorkers {
		s.once.Do(func() { close(s.full) })
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.now--
		s.mu.Unlock()
	}()
	select {
	case <-s.full:
		return &v1.SearchResponse{Hits: []*v1.SearchHit{hit(tableKey(req), 1)}}, nil
	case <-time.After(5 * time.Second):
		return nil, errors.New("searches ran one at a time")
	}
}

func TestFanOutRunsABoundedSetAtOnce(t *testing.T) {
	src := &gateSource{stubSource: stubSource{id: "s", caps: &v1.SourceCapabilities{Browse: true, Facets: []*v1.Facet{NewFacet(FacetTask, "Task", false), NewFacet(FacetLibrary, "Library", false)}}}, full: make(chan struct{})}
	resp, err := NewRegistry(src).Search(context.Background(), &v1.SearchRequest{SourceId: "s", Filters: map[string]string{FacetTask: "a,b,c", FacetLibrary: "x,y,z"}})
	if err != nil || len(resp.GetHits()) != 9 {
		t.Fatalf("nine combinations %v %v", resp, err)
	}
	if src.most != fanWorkers {
		t.Fatalf("at most %d in flight, got %d", fanWorkers, src.most)
	}
}

func TestFanOutFailsWhenACombinationFails(t *testing.T) {
	src := newTable("s", map[string]*v1.SearchResponse{
		"task=a#": {Hits: []*v1.SearchHit{hit("x/a", 1)}},
		"task=b#": nil,
	}, NewFacet(FacetTask, "Task", false))
	resp, err := NewRegistry(src).Search(context.Background(), &v1.SearchRequest{SourceId: "s", Filters: map[string]string{FacetTask: "a,b"}})
	if !errors.Is(err, errUpstream) || resp != nil {
		t.Fatalf("a failed value fails the search, got %v %v", resp, err)
	}
}

func TestSingleValuesPassThroughWithTheProviderCursor(t *testing.T) {
	ctx := context.Background()
	src := newTable("s", map[string]*v1.SearchResponse{
		"author=me,you&task=a#":      {Hits: []*v1.SearchHit{hit("x/a1", 1)}, NextCursor: "raw-2"},
		"author=me,you&task=a#raw-2": {Hits: []*v1.SearchHit{hit("x/a2", 1)}},
	}, NewFacet(FacetTask, "Task", false), NewFacet(FacetLibrary, "Library", false), Freeform(FacetAuthor, "Author"))
	r := NewRegistry(src)
	req := &v1.SearchRequest{SourceId: "s", Filters: map[string]string{FacetTask: "a", FacetAuthor: "me,you"}}
	resp, err := r.Search(ctx, req)
	if err != nil || hitList(resp) != "s:x/a1" || resp.GetNextCursor() != "raw-2" {
		t.Fatalf("the provider's own page and cursor, got %v %v", resp, err)
	}
	req.Cursor = resp.GetNextCursor()
	if resp, err = r.Search(ctx, req); err != nil || hitList(resp) != "s:x/a2" || resp.GetNextCursor() != "" {
		t.Fatalf("second page %v %v", resp, err)
	}
	if got := src.calls(); got != "author=me,you&task=a# author=me,you&task=a#raw-2" {
		t.Fatalf("one call per page, got %s", got)
	}
}

func TestFanOutCursorNestsInsideTheMultiSourceCursor(t *testing.T) {
	ctx := context.Background()
	task := NewFacet(FacetTask, "Task", false)
	one := newTable("one", map[string]*v1.SearchResponse{
		"task=a#":  {Hits: []*v1.SearchHit{hit("x/a1", 1)}, NextCursor: "n"},
		"task=b#":  {Hits: []*v1.SearchHit{hit("x/b1", 1)}},
		"task=a#n": {Hits: []*v1.SearchHit{hit("x/a2", 1)}},
	}, task)
	two := newTable("two", map[string]*v1.SearchResponse{
		"task=a#": {Hits: []*v1.SearchHit{hit("y/a1", 1)}},
		"task=b#": {Hits: []*v1.SearchHit{hit("y/b1", 1)}},
	}, task)
	r := NewRegistry(one, two)
	req := &v1.SearchRequest{Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Filters: map[string]string{FacetTask: "a,b"}}
	resp, err := r.Search(ctx, req)
	if err != nil || hitList(resp) != "one:x/a1,two:y/a1,one:x/b1,two:y/b1" {
		t.Fatalf("first page %v %v", resp, err)
	}
	outer := cursorMap(t, resp.GetNextCursor())
	if len(outer) != 1 || !maps.Equal(cursorMap(t, outer["one"]), map[string]string{"task=a": "n"}) {
		t.Fatalf("the source cursor holds its combination cursors, got %v", outer)
	}
	if one.calls() != "task=a# task=b#" || two.calls() != "task=a# task=b#" {
		t.Fatal("both sources fan out")
	}
	req.Cursor = resp.GetNextCursor()
	if resp, err = r.Search(ctx, req); err != nil || hitList(resp) != "one:x/a2" || resp.GetNextCursor() != "" {
		t.Fatalf("second page %v %v", resp, err)
	}
	if got, rest := one.calls(), two.calls(); got != "task=a#n" || rest != "" {
		t.Fatalf("only the open combination continues, got %q and %q", got, rest)
	}
}
