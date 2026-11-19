package sources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestKaggleSendsEveryFilterAndOrder(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models/list" {
			t.Errorf("unexpected request %s", r.URL)
		}
		got = r.URL.Query()
		owner := "google"
		if got.Get("owner") == "nobody" {
			// Kaggle drops an owner it does not know and lists everyone's models
			owner = "keras"
		}
		w.Write([]byte(`{"nextPageToken":"tok2","totalResults":13,"models":[{
			"ref":"` + owner + `/gemma","title":"Gemma","author":"Google","voteCount":20619,"updateTime":"2024-05-08T10:00:00Z","publishTime":"2024-02-20T00:00:00Z",
			"tags":[{"name":"text","fullPath":"data type > text"},{"name":"text generation","fullPath":"task > text-generation"},{"name":"text classification","fullPath":"task > text-classification"}],
			"modelVersionLinks":[{"type":"vertexOpen","url":"https://console.cloud.google.com/vertex-ai"}],
			"instances":[
				{"slug":"2b","framework":"pyTorch","fineTunable":false,"totalUncompressedBytes":5000,"licenseName":"Gemma"},
				{"slug":"7b","framework":"keras","fineTunable":true,"totalUncompressedBytes":9000}
			]}]}`))
	}))
	defer srv.Close()
	c := testClient(t, kaggle, &v1.Source{Id: "kaggle", Kind: v1.SourceKind_SOURCE_KIND_KAGGLE, Config: map[string]string{"endpoint": srv.URL}})
	resp, err := c.Search(context.Background(), &v1.SearchRequest{Query: "gemma", Sort: kgSortPublished, Limit: 100, Cursor: "tok1", Filters: map[string]string{FacetAuthor: "Google", kgFacetVertex: "true"}})
	if err != nil {
		t.Fatal(err)
	}
	for param, want := range map[string]string{
		"sortBy":           "publishTime",
		"pageSize":         "50",
		"search":           "gemma",
		"owner":            "Google",
		"onlyVertexModels": "true",
		"pageToken":        "tok1",
	} {
		if v := strings.Join(got[param], " "); v != want {
			t.Fatalf("%s\nwant %s\n got %s", param, want, v)
		}
	}
	if resp.GetNextCursor() != "tok2" || resp.GetTotal() != 13 || len(resp.GetHits()) != 1 {
		t.Fatalf("response %v", resp)
	}
	hit := resp.GetHits()[0]
	if hit.GetTask() != "text-generation" || hit.GetSizeBytes() != 5000 || hit.GetLikes() != 20619 || hit.GetLicense() != "Gemma" || hit.GetUpdatedAt() == nil {
		t.Fatalf("hit %v", hit)
	}
	for k, want := range map[string]string{"published": "2024-02-20T00:00:00Z", "vertex": "true", "fine_tunable": "true", "instances": "2", "frameworks": "pyTorch, keras"} {
		if hit.GetExtra()[k] != want {
			t.Fatalf("extra %s want %q got %q", k, want, hit.GetExtra()[k])
		}
	}
	// A toggle switched off sends nothing
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortLikes, Filters: map[string]string{kgFacetVertex: "false"}}); err != nil || got.Has("onlyVertexModels") || got.Get("sortBy") != "voteCount" || got.Get("pageSize") != "30" {
		t.Fatalf("query %v %v", got, err)
	}
	if _, err := c.Search(context.Background(), &v1.SearchRequest{Filters: map[string]string{FacetAuthor: "nobody"}}); !errors.Is(err, ErrSource) {
		t.Fatalf("an owner kaggle ignores should be refused, got %v", err)
	}
	// Kaggle takes updateTime without ordering by it, and orders nothing ascending
	for _, req := range []*v1.SearchRequest{{Sort: SortUpdated}, {Sort: SortDownloads, Ascending: true}} {
		if _, err := c.Search(context.Background(), req); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%v should be unsupported, got %v", req, err)
		}
	}
	caps := c.Capabilities(context.Background())
	var sorts, columns []string
	for _, s := range caps.GetSorts() {
		sorts = append(sorts, s.GetId()+"="+s.GetLabel())
	}
	for _, col := range caps.GetColumns() {
		columns = append(columns, col.GetId()+":"+col.GetSort())
	}
	if got, want := strings.Join(sorts, ","), "trending=Trending,downloads=Most downloaded,likes=Most upvoted,created=Newest,published=Recently published,notebooks=Most used in notebooks"; got != want {
		t.Fatalf("sorts\nwant %s\n got %s", want, got)
	}
	// The likes and published columns order by their sorts, and no column claims downloads Kaggle does not list
	if got, want := strings.Join(columns, " "), "size_bytes: likes:likes frameworks: updated_at: published:published task: license: author: instances: fine_tunable: vertex:"; got != want {
		t.Fatalf("columns\nwant %s\n got %s", want, got)
	}
}
