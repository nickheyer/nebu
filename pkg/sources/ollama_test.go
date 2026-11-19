package sources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// One search page result as the site writes it
func olResult(href, body string) string {
	return `<li  class="border-t-[0.5px] border-line first:border-t-0">
  <a href="/` + href + `" class="group grid grid-cols-1 gap-x-4 px-4 py-6">` + body + `</a>
</li>`
}

// One library page listing as the site writes it
func olListing(name, pulls string) string {
	return `<li x-test-model class="flex items-baseline border-b py-6">
      <a href="/library/` + name + `" class="group w-full space-y-5"><h2><span>` + name + `</span></h2>
        <span class="inline-flex items-center rounded-md bg-indigo-50 px-2 py-0.5">vision</span>
        <span class="flex items-center"><span >` + pulls + `</span><span class="hidden sm:flex">&nbsp;Pulls</span></span>
        <span class="flex items-center" title="Nov 30, 2024 10:34 PM UTC"><span>1 year ago</span></span>
      </a></li>`
}

const olSearchFixture = `<div hidden data-result-count="20" data-has-more="true"></div>
<ul role="list" class="overflow-hidden rounded-3xl border-[0.5px] border-line">` +
	`
<li class="nav"><a href="/blog" class="hover:underline">Blog</a></li>` +
	`
`

func TestOllamaSearchPageSendsEveryFilterAndOrder(t *testing.T) {
	var got url.Values
	var scripted bool
	more := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		got, scripted = r.URL.Query(), r.Header.Get("HX-Request") == "true"
		page := olSearchFixture +
			olResult("library/glm-5.3", `<h2 title="glm-5.3"><span >glm-5.3</span></h2>
      <span class="flex-none text-[15px] tabular-nums text-muted" title="Updated August 28, 2026">2mo</span>
      <div class="order-last mt-3 flex"><span  class="inline-flex items-center rounded-md bg-indigo-50 px-2 py-0.5">tools</span><span  class="inline-flex items-center rounded-md bg-indigo-50 px-2 py-0.5">thinking</span>
        <span  class="inline-flex items-center rounded-md bg-cyan-50 px-2 py-0.5 text-cyan-500">cloud</span></div>
      <p class="mt-1 line-clamp-2 text-[15px] leading-6 text-black">Z.ai&#39;s flagship model.</p>
      <div class="mt-3 flex"><span class="inline-flex items-center gap-1.5 text-ink sm:mr-1" title="Context window"><svg width="15" height="15"><path d="M3.5 8V6"/></svg><span >976K</span></span>
        <span class="font-semibold sm:mr-1" title="Extra High usage"><span  class="text-ink">$</span></span></div>`) +
			olResult("library/qwen3.8", `<h2 title="qwen3.8"><span >qwen3.8</span></h2>
      <span class="flex-none text-[15px] tabular-nums text-muted" title="Updated November 8, 2026">1w</span>
      <div class="order-last mt-3 flex"><span  class="inline-flex items-center rounded-md bg-indigo-50 px-2 py-0.5">vision</span><span  class="inline-flex items-center rounded-md bg-indigo-50 px-2 py-0.5">tools</span></div>
      <p class="mt-1 line-clamp-2 text-[15px] leading-6 text-black">Qwen3.8 delivers gains.</p>
      <div class="mt-3 flex"><span class="inline-flex items-center gap-1.5 tabular-nums text-ink sm:mr-1" title="6,929,896 downloads"><svg></svg><span >6.9M</span></span>
        <span class="inline-flex items-center gap-1.5 tabular-nums text-ink sm:mr-1"><svg></svg><span >13 tags</span></span>
        <span  class="inline-flex items-center rounded-md bg-[#ddf4ff] px-2 py-0.5">27b</span></div>`) +
			olResult("QwertyMcQwertz/MutualistLLM", `<h2 title="MutualistLLM"><span >QwertyMcQwertz/MutualistLLM</span></h2>
      <span class="flex-none text-[15px] tabular-nums text-muted" title="Updated January 8, 2026">10mo</span>
      <div class="mt-3 flex"><span class="inline-flex items-center gap-1.5 tabular-nums text-ink sm:mr-1" title="80 downloads"><svg></svg><span >80</span></span></div>`) +
			`</ul>`
		if more {
			page += `<div hx-get="/search?page=3&q=qwen" hx-trigger="revealed" hx-swap="outerHTML"></div>`
		}
		w.Write([]byte(page))
	}))
	defer srv.Close()
	c := testClient(t, ollama, &v1.Source{Id: "ol", Kind: v1.SourceKind_SOURCE_KIND_OLLAMA, Config: map[string]string{"endpoint": srv.URL}})
	resp, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortName, Query: "qwen", Cursor: "2", Filters: map[string]string{
		FacetCapability: "vision,tools",
		olFacetWhere:    "local",
		olFacetSize:     "16",
	}})
	if err != nil {
		t.Fatal(err)
	}
	for param, want := range map[string]string{"o": "name", "page": "2", "q": "qwen", "c": "vision tools local", "s": "16"} {
		if v := strings.Join(got[param], " "); v != want {
			t.Fatalf("%s\nwant %s\n got %s", param, want, v)
		}
	}
	if !scripted || resp.GetNextCursor() != "3" || len(resp.GetHits()) != 3 {
		t.Fatalf("asked as the page's script %t, next %q, hits %v", scripted, resp.GetNextCursor(), resp.GetHits())
	}
	show := func(h *v1.SearchHit) string {
		return fmt.Sprintf("%s by %s, %d downloads, updated %s, task %s, %s %q", h.GetRepo(), h.GetAuthor(), h.GetDownloads(), h.GetUpdatedAt().AsTime().Format(time.DateOnly), h.GetTask(), h.GetDescription(), h.GetExtra())
	}
	for i, want := range []string{
		`glm-5.3 by library, 0 downloads, updated 2026-08-28, task tools, Z.ai's flagship model. map["capabilities":"tools,thinking" "cloud":"true" "context":"976000" "price":"Extra High"]`,
		`qwen3.8 by library, 6929896 downloads, updated 2026-11-08, task vision, Qwen3.8 delivers gains. map["capabilities":"vision,tools" "sizes":"27b" "tags":"13"]`,
		`QwertyMcQwertz/MutualistLLM by QwertyMcQwertz, 80 downloads, updated 2026-01-08, task ,  map[]`,
	} {
		if got := show(resp.GetHits()[i]); got != want {
			t.Fatalf("hit %d\nwant %s\n got %s", i, want, got)
		}
	}
	if u := resp.GetHits()[2].GetUrl(); u != srv.URL+"/QwertyMcQwertz/MutualistLLM" {
		t.Fatalf("a community model's page is under its owner, got %s", u)
	}
	// Popular is the page's own order, sent by name, and the last page names no next one
	more = false
	resp, err = c.Search(context.Background(), &v1.SearchRequest{Sort: SortTrending, Author: "QwertyMcQwertz"})
	if err != nil || got.Get("o") != "popular" || got.Get("page") != "1" || got.Has("c") || resp.GetNextCursor() != "" || len(resp.GetHits()) != 1 {
		t.Fatalf("popular %v, hits %v, next %q, %v", got, resp.GetHits(), resp.GetNextCursor(), err)
	}
	got = nil
	for _, filters := range []map[string]string{
		{FacetCapability: "tools,audio"},
		{olFacetWhere: "space"},
		{olFacetSize: "huge"},
		{olFacetWhere: "cloud", olFacetSize: "8"},
	} {
		if _, err := c.Search(context.Background(), &v1.SearchRequest{Sort: SortTrending, Filters: filters}); !errors.Is(err, ErrSource) {
			t.Fatalf("%v should be refused, the page ignores it, got %v", filters, err)
		}
	}
	if got != nil {
		t.Fatalf("a refused filter reaches no page, got %v", got)
	}
}

// The library page orders by pulls and age, and the search page says which models a filter admits
func TestOllamaLibraryOrdersNarrowToTheSearchPagesMembers(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		switch r.URL.Path {
		case "/library":
			if r.URL.Query().Has("c") {
				t.Errorf("the library page takes no filters, got %s", r.URL)
			}
			w.Write([]byte(`<ul>` + olListing("a", "9M") + olListing("b", "5M") + olListing("c", "1M") + `</ul>`))
		case "/search":
			if r.Header.Get("HX-Request") != "true" || r.URL.Query().Get("c") != "vision" {
				t.Errorf("members asked without the script header or the filter: %s", r.URL)
			}
			switch r.URL.Query().Get("page") {
			case "1":
				w.Write([]byte(olSearchFixture + olResult("library/c", "") + `</ul><div hx-get="/search?page=2" hx-trigger="revealed"></div>`))
			case "2":
				w.Write([]byte(olSearchFixture + olResult("library/a", "") + `</ul>`))
			default:
				t.Errorf("read past the last page: %s", r.URL)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient(t, ollama, &v1.Source{Id: "ol", Kind: v1.SourceKind_SOURCE_KIND_OLLAMA, Config: map[string]string{"endpoint": srv.URL}})
	repos := func(req *v1.SearchRequest) string {
		t.Helper()
		resp, err := c.Search(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range resp.GetHits() {
			out = append(out, h.GetRepo())
		}
		return strings.Join(out, " ")
	}
	if got := repos(&v1.SearchRequest{Filters: map[string]string{FacetCapability: "vision"}}); got != "a c" {
		t.Fatalf("most downloaded vision models, got %q", got)
	}
	// The whole list flips into ascending order, and the members are read once
	if got := repos(&v1.SearchRequest{Sort: SortCreated, Ascending: true, Filters: map[string]string{FacetCapability: "vision"}}); got != "c a" {
		t.Fatalf("oldest vision models, got %q", got)
	}
	if got := repos(&v1.SearchRequest{Ascending: true}); got != "c b a" {
		t.Fatalf("least downloaded, got %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := "/library?sort=popular /search?c=vision&page=1 /search?c=vision&page=2 /library?sort=newest /library?sort=popular"; strings.Join(requests, " ") != want {
		t.Fatalf("requests\nwant %s\n got %s", want, strings.Join(requests, " "))
	}
	caps := c.Capabilities(context.Background())
	var sorts []string
	for _, s := range caps.GetSorts() {
		sorts = append(sorts, fmt.Sprintf("%s=%s/%t", s.GetId(), s.GetLabel(), s.GetReversible()))
	}
	if want := "downloads=Most downloaded/true trending=Popular/false created=Newest/true name=Name/false context=Largest context/false price=Lowest price/false"; strings.Join(sorts, " ") != want {
		t.Fatalf("sorts\nwant %s\n got %s", want, strings.Join(sorts, " "))
	}
	var facets []string
	for _, f := range caps.GetFacets() {
		var values []string
		for _, v := range f.GetValues() {
			values = append(values, v.GetId())
		}
		facets = append(facets, fmt.Sprintf("%s/%t:%s", f.GetId(), f.GetMulti(), strings.Join(values, ",")))
	}
	if want := "capability/true:tools,thinking,vision,embedding,decision where/false:local,cloud size/false:8,16,32,64,over64"; strings.Join(facets, " ") != want {
		t.Fatalf("facets\nwant %s\n got %s", want, strings.Join(facets, " "))
	}
}
