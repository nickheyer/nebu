package sources

import (
	"context"
	"errors"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
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
