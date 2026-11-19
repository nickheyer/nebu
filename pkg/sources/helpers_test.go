package sources

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
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

func TestTogglesAndRanges(t *testing.T) {
	filters := func(v string) *v1.SearchRequest {
		return &v1.SearchRequest{Filters: map[string]string{"f": v}}
	}
	if !On(filters("true"), "f") || On(filters("false"), "f") || On(filters("yes"), "f") || On(&v1.SearchRequest{}, "f") {
		t.Fatal("a toggle is on only when sent as true")
	}
	show := func(b Bounds) string {
		side := func(n *uint64) string {
			if n == nil {
				return "-"
			}
			return strconv.FormatUint(*n, 10)
		}
		return side(b.Min) + ".." + side(b.Max)
	}
	for raw, want := range map[string]string{
		"":              "-..-",
		"3B..6B":        "3000000000..6000000000",
		"1.5k..":        "1500..-",
		"..250M":        "-..250000000",
		" 0 .. 10 ":     "0..10",
		"7..7":          "7..7",
		"1000..2000000": "1000..2000000",
	} {
		if b, err := Range(filters(raw), "f"); err != nil || show(b) != want {
			t.Fatalf("range %q: want %s, got %s %v", raw, want, show(b), err)
		}
	}
	for _, raw := range []string{"7B", "..", "big..", "1..many", "9..3"} {
		if b, err := Range(filters(raw), "f"); !errors.Is(err, ErrSource) {
			t.Fatalf("range %q should be refused, got %s %v", raw, show(b), err)
		}
	}
	// Every span a range facet offers reads as a range, each starting where the one before ends
	spans := Between("f", "F", ParameterSpans()...).GetValues()
	var end *uint64
	for i, s := range spans {
		b, err := Range(filters(s.GetId()), "f")
		if err != nil || s.GetLabel() == "" {
			t.Fatalf("span %q: %v", s.GetId(), err)
		}
		if (i == 0) != (b.Min == nil) || (i == len(spans)-1) != (b.Max == nil) || end != nil && *b.Min != *end {
			t.Fatalf("span %q does not follow the one before, got %s", s.GetId(), show(b))
		}
		end = b.Max
	}
}

func TestColumnsCarryLabelsTypesAndSorts(t *testing.T) {
	cols := slices.Concat(Shown(Cols(ColSize, ColDownloads)...), []*v1.HitColumn{Named(Col(ColLikes), "Stars"), ExtraCol("forks", "Forks", AsCount, "Forks of the repository")})
	var got []string
	for _, c := range cols {
		got = append(got, fmt.Sprintf("%s/%s/%s/%t", c.GetId(), c.GetLabel(), strings.TrimPrefix(c.GetType().String(), "COLUMN_TYPE_"), c.GetShown()))
	}
	if want := "size_bytes/Size/BYTES/true downloads/Downloads/COUNT/true likes/Stars/COUNT/false forks/Forks/COUNT/false"; strings.Join(got, " ") != want {
		t.Fatalf("columns\nwant %s\n got %s", want, strings.Join(got, " "))
	}
	// Every registered column names the sort that orders by it, and no sort the provider lacks
	for _, cat := range all() {
		for _, c := range cat.Columns {
			want := cmp.Or(columnSorts[c.GetId()], c.GetId())
			if !slices.Contains(cat.Sorts, want) {
				want = ""
			}
			if c.GetSort() != want {
				t.Fatalf("%s column %s: sort %q, want %q", cat.ID, c.GetId(), c.GetSort(), want)
			}
		}
	}
	// Every facet says how it takes its value, so a client can draw it
	for _, cat := range all() {
		for _, f := range cat.Facets {
			if f.GetInput() == v1.FacetInput_FACET_INPUT_UNSPECIFIED || f.GetLabel() == "" {
				t.Fatalf("%s facet %s needs an input and a label", cat.ID, f.GetId())
			}
		}
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
