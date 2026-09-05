package sources

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPageSortMatch(t *testing.T) {
	now := time.Now()
	hits := []*v1.SearchHit{
		{Repo: "b/two", Downloads: 5, UpdatedAt: timestamppb.New(now.Add(-time.Hour)), Tags: []string{"gguf"}},
		{Repo: "a/one", Downloads: 9, UpdatedAt: timestamppb.New(now), Description: "the best coder"},
		{Repo: "c/three", Downloads: 1},
	}
	SortHits(hits, SortDownloads, false)
	if hits[0].Repo != "a/one" || hits[2].Repo != "c/three" {
		t.Fatalf("downloads desc %v", hits)
	}
	SortHits(hits, SortName, false)
	if hits[0].Repo != "a/one" || hits[1].Repo != "b/two" {
		t.Fatalf("name %v", hits)
	}
	SortHits(hits, SortName, true)
	if hits[0].Repo != "c/three" {
		t.Fatalf("name asc %v", hits)
	}
	SortHits(hits, SortUpdated, false)
	if hits[0].Repo != "a/one" || hits[2].Repo != "c/three" {
		t.Fatalf("updated %v", hits)
	}
	page := Page(hits, &v1.SearchRequest{Limit: 2}, 2)
	if len(page.Hits) != 2 || page.NextCursor != "2" || page.Total != 3 {
		t.Fatalf("page1 %v", page)
	}
	page = Page(hits, &v1.SearchRequest{Limit: 2, Cursor: "2"}, 2)
	if len(page.Hits) != 1 || page.NextCursor != "" {
		t.Fatalf("page2 %v", page)
	}
	if !Matches(hits[0], "best CODER") || Matches(hits[0], "gguf") || !Matches(hits[0], "") {
		t.Fatal("match")
	}
	if !Matches(&v1.SearchHit{Repo: "x/y", Tags: []string{"gguf"}}, "gguf y") {
		t.Fatal("tag match")
	}
}

func TestFiltersAndHelpers(t *testing.T) {
	req := &v1.SearchRequest{Filters: map[string]string{"type": "LORA, Checkpoint ,", "author": "me"}}
	if got := Filter(req, "type"); strings.Join(got, "|") != "LORA|Checkpoint" {
		t.Fatalf("filter %v", got)
	}
	if Author(req) != "me" || Author(&v1.SearchRequest{Author: "you"}) != "you" {
		t.Fatal("author")
	}
	if Limit(&v1.SearchRequest{}, 20, 100) != 20 || Limit(&v1.SearchRequest{Limit: 500}, 20, 100) != 100 || Limit(&v1.SearchRequest{Limit: 3}, 20, 100) != 3 {
		t.Fatal("limit")
	}
	if Offset("x") != 0 || Offset("7") != 7 || Offset("-1") != 0 {
		t.Fatal("offset")
	}
	if Stamp("2025-01-02T03:04:05.123456Z") == nil || Stamp("2025-01-02T03:04:05") == nil || Stamp("2025-01-02 15:04:05") == nil || Stamp("2025-01-02") == nil || Stamp("") != nil || Stamp("yesterday") != nil {
		t.Fatal("stamp")
	}
	if name, tag := SplitTag("llama3.2:1b", "latest"); name != "llama3.2" || tag != "1b" {
		t.Fatal("split tag")
	}
	if name, tag := SplitTag("ai/gemma3", "latest"); name != "ai/gemma3" || tag != "latest" {
		t.Fatal("split default")
	}
	if name, tag := SplitTag("localhost:5000/x", "latest"); name != "localhost:5000/x" || tag != "latest" {
		t.Fatal("split host port")
	}
	if Humanize("text-generation") != "Text generation" || Sorts(SortDownloads)[0].Label != "Most downloaded" || Sorts("odd")[0].Label != "Odd" {
		t.Fatal("labels")
	}
}

func TestTextHelpers(t *testing.T) {
	if got := StripTags("<p>Hello <b>world</b> &amp; <script>x()</script>friends</p>"); got != "Hello world & friends" {
		t.Fatalf("strip %q", got)
	}
	if got := Excerpt("one two three four five", 12); got != "one two…" {
		t.Fatalf("excerpt %q", got)
	}
	if ParseCount("1.4M") != 1400000 || ParseCount("12K") != 12000 || ParseCount("7") != 7 || ParseCount("x") != 0 {
		t.Fatal("count")
	}
	if ParseSize("1.3GB") != 1300000000 || ParseSize("512MB") != 512000000 || ParseSize("nope") != 0 {
		t.Fatal("size")
	}
}

func TestMemo(t *testing.T) {
	calls := 0
	m := Memo[int]{TTL: time.Hour}
	fill := func(ctx context.Context) (int, error) {
		calls++
		return calls, nil
	}
	if v, _ := m.Get(context.Background(), fill); v != 1 {
		t.Fatal("first")
	}
	if v, _ := m.Get(context.Background(), fill); v != 1 || calls != 1 {
		t.Fatal("cached")
	}
	m.TTL = time.Nanosecond
	time.Sleep(time.Millisecond)
	if v, err := m.Get(context.Background(), func(ctx context.Context) (int, error) { return 0, context.Canceled }); v != 1 || err != nil {
		t.Fatal("stale value kept on failure")
	}
}
