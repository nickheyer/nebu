package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// An MCP server over HTTP with a read only tool, a mutating one, and one answering an image
func fakeMCP(t *testing.T) *httptest.Server {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	type echoIn struct {
		Text string `json:"text"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Says it back", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + in.Text}}}, nil, nil
	})
	type pathIn struct {
		Path string `json:"path"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "write file", Description: "Writes"}, func(ctx context.Context, req *mcp.CallToolRequest, in pathIn) (*mcp.CallToolResult, any, error) {
		if in.Path == "" {
			return nil, nil, errors.New("a path is required")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "wrote " + in.Path}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "picture", Description: "Draws", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3}}, &mcp.TextContent{Text: "a picture"}}}, nil, nil
	})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true}))
	t.Cleanup(srv.Close)
	return srv
}

func ready(t *testing.T, r *Registry, name string) Server {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.Wait(ctx)
	s, err := r.Server(ctx, name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return s
}

// An MCP server in Settings connects, offers its tools under its name with the characters the
// APIs refuse replaced, marks read only ones, and runs calls with text and image answers
func TestRegistryMCP(t *testing.T) {
	srv := fakeMCP(t)
	bus := events.New()
	r := New(slog.Default(), bus)
	t.Cleanup(r.Close)
	ctx := context.Background()
	r.Apply(ctx, []*v1.ToolServer{{Name: "fake", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_HTTP, Url: srv.URL}})
	s := ready(t, r, "fake")
	var names []string
	for _, tool := range s.Tools() {
		names = append(names, tool.Name)
	}
	if strings.Join(names, " ") != "fake__echo fake__picture fake__write_file" {
		t.Fatalf("tools %v", names)
	}
	if !s.Tools()[0].ReadOnly || s.Tools()[2].ReadOnly {
		t.Fatalf("read only hints: %v", s.Tools())
	}
	if offered := Offered(s, false); len(offered) != 2 || offered[1].Name != "fake__picture" {
		t.Fatalf("read only offer: %v", offered)
	}
	res, err := s.Call(ctx, Call{Name: "fake__echo", Args: json.RawMessage(`{"text":"hi"}`)})
	if err != nil || res.Failed || res.Text() != "echo: hi" || !res.Asked {
		t.Fatalf("echo: %v %v", res, err)
	}
	res, err = s.Call(ctx, Call{Name: "fake__write_file", Args: json.RawMessage(`{}`)})
	if err != nil || !res.Failed || !strings.Contains(res.Text(), "path") {
		t.Fatalf("tool error: %v %v", res, err)
	}
	res, err = s.Call(ctx, Call{Name: "fake__picture", Args: json.RawMessage(`{}`)})
	if err != nil || len(res.Parts) != 2 || res.Parts[0].Type != "image" || res.Parts[0].MediaType != "image/png" || res.Parts[0].Data != "AQID" || res.Parts[1].Text != "a picture" {
		t.Fatalf("picture: %v %v", res, err)
	}
	if _, err := s.Call(ctx, Call{Name: "fake__gone", Args: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("an unknown tool is an error")
	}
	st := r.Status()
	if len(st) != 1 || st[0].GetState() != v1.ToolServerState_TOOL_SERVER_STATE_READY || len(st[0].GetTools()) != 3 || st[0].GetRef() != "fake" {
		t.Fatalf("status %v", st)
	}
	if out, err := r.Call(ctx, "", "fake__echo", json.RawMessage(`{"text":"by name"}`)); err != nil || out.Text() != "echo: by name" {
		t.Fatalf("call by name: %v %v", out, err)
	}
	r.Apply(ctx, []*v1.ToolServer{{Name: "fake", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_HTTP, Url: srv.URL, AllowedTools: []string{"echo"}}})
	if s := ready(t, r, "fake"); len(s.Tools()) != 1 || s.Tools()[0].Name != "fake__echo" {
		t.Fatalf("allowed tools: %v", s.Tools())
	}
	r.Apply(ctx, []*v1.ToolServer{{Name: "fake", Enabled: false, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_HTTP, Url: srv.URL}})
	if _, err := r.Server(ctx, "fake"); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("disabled: %v", err)
	}
	r.Apply(ctx, nil)
	if _, err := r.Server(ctx, "fake"); !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("removed: %v", err)
	}
}

// A server that cannot be reached is not ready and says why, and a command that is not there either
func TestRegistryFailures(t *testing.T) {
	r := New(slog.Default(), nil)
	t.Cleanup(r.Close)
	ctx := context.Background()
	r.Apply(ctx, []*v1.ToolServer{
		{Name: "down", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_HTTP, Url: "http://127.0.0.1:1/mcp"},
		{Name: "missing", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_STDIO, Command: "/nonexistent/mcp-server"},
		{Name: "web", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: "brave"},
	})
	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r.Wait(wait)
	for _, name := range []string{"down", "missing", "web"} {
		if _, err := r.Server(ctx, name); err == nil || !strings.Contains(err.Error(), "not ready") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, st := range r.Status() {
		if st.GetError() == "" {
			t.Fatalf("%s has no error", st.GetServer().GetName())
		}
	}
	if _, ok := r.Find(v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH); ok {
		t.Fatal("a failed search server is not found")
	}
}

// Ad hoc endpoints connect for one request and close after it
func TestConnect(t *testing.T) {
	srv := fakeMCP(t)
	s, err := Connect(context.Background(), "adhoc", srv.URL, map[string]string{"Authorization": "Bearer x"}, []string{"echo"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tools()) != 1 || s.Tools()[0].Name != "adhoc__echo" {
		t.Fatalf("tools %v", s.Tools())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Connect(context.Background(), "nowhere", "http://127.0.0.1:1/mcp", nil, nil, slog.Default()); err == nil {
		t.Fatal("an unreachable endpoint is an error")
	}
}

func TestModelName(t *testing.T) {
	taken := map[string]bool{}
	for _, c := range [][2]string{{"read_file", "fs__read_file"}, {"read file", "fs__read_file_2"}, {"a.b/c", "fs__a_b_c"}} {
		if got := modelName("fs", c[0], taken); got != c[1] {
			t.Errorf("%s: %s", c[0], got)
		}
	}
}

// Pages come back as their title and text, blocks on their own lines and scripts left out
func TestFetch(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><head><title>Kittens</title><script>var x = 1;</script><style>p{}</style></head><body><h1>Small cats</h1><p>Very   small.</p><ul><li>One</li><li>Two</li></ul><noscript>no</noscript></body></html>`))
		case "/text":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("plain words"))
		case "/bin":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte{0, 1, 2})
		case "/slow":
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(site.Close)
	f := newFetch(&v1.ToolServer{Name: "fetch"})
	ctx := context.Background()
	call := func(url string) *Result {
		res, _ := f.Call(ctx, Call{Name: FetchTool, Args: json.RawMessage(`{"url":"` + url + `"}`)})
		return res
	}
	res := call(site.URL + "/page")
	if res.Failed || res.Page == nil || res.Page.Title != "Kittens" || res.Page.Text != "Small cats\nVery small.\nOne\nTwo" || !res.Asked {
		t.Fatalf("page: %+v", res)
	}
	if !strings.HasPrefix(res.Text(), "Kittens\n"+site.URL+"/page\n\nSmall cats") {
		t.Fatalf("text: %q", res.Text())
	}
	if res := call(site.URL + "/text"); res.Failed || res.Page.Text != "plain words" || res.Page.MediaType != "text/plain" {
		t.Fatalf("text: %+v", res)
	}
	for url, code := range map[string]string{site.URL + "/bin": "unsupported_content_type", site.URL + "/slow": "too_many_requests", site.URL + "/nope": "url_not_accessible", "ftp://x": "invalid_input", "": "invalid_input", "http://" + strings.Repeat("a", 3000) + ".example": "url_too_long"} {
		if res := call(url); !res.Failed || res.Code != code {
			t.Errorf("%s: %+v", url, res)
		}
	}
	res, _ = f.Call(ctx, Call{Name: FetchTool, Args: json.RawMessage(`{"url":"` + site.URL + `/page"}`), Blocked: []string{"127.0.0.1"}})
	if res.Code != "url_not_allowed" {
		t.Fatalf("blocked: %+v", res)
	}
	res, _ = f.Call(ctx, Call{Name: FetchTool, Args: json.RawMessage(`{"url":"` + site.URL + `/page"}`), MaxChars: 10})
	if !strings.HasPrefix(res.Page.Text, "Small cats") || !strings.Contains(res.Page.Text, "[cut after 10 characters]") {
		t.Fatalf("cut: %q", res.Page.Text)
	}
}

// A search server runs the query through its provider, drops results outside the domains, and
// names why a search did not happen
func TestSearch(t *testing.T) {
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
			{"url": "https://cats.example/kittens", "title": "Kittens", "content": "Very small cats.", "publishedDate": "2024-05-01"},
			{"url": "https://dogs.example/puppies", "title": "Puppies", "content": "Not cats."},
		}})
	}))
	t.Cleanup(searx.Close)
	s, err := newSearch(&v1.ToolServer{Name: "web", Provider: "searxng", SearxngUrl: searx.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := s.Call(ctx, Call{Name: SearchTool, Args: json.RawMessage(`{"query":"kittens"}`), Country: "US"})
	if err != nil || res.Failed || len(res.Hits) != 2 || res.Hits[0].Age != "2024-05-01" || !strings.Contains(res.Text(), "1. Kittens") || !res.Asked {
		t.Fatalf("search: %+v %v", res, err)
	}
	res, _ = s.Call(ctx, Call{Name: SearchTool, Args: json.RawMessage(`{"query":"kittens"}`), Allowed: []string{"cats.example"}})
	if len(res.Hits) != 1 || res.Hits[0].URL != "https://cats.example/kittens" {
		t.Fatalf("allowed: %+v", res.Hits)
	}
	if res, _ := s.Call(ctx, Call{Name: SearchTool, Args: json.RawMessage(`{"q":"oops"}`)}); !res.Failed || res.Code != "invalid_input" || res.Asked {
		t.Fatalf("no query: %+v", res)
	}
	searx.Close()
	res, err = s.Call(ctx, Call{Name: SearchTool, Args: json.RawMessage(`{"query":"kittens"}`)})
	if err == nil || !res.Failed || res.Code != "unavailable" || !res.Asked {
		t.Fatalf("engine down: %+v %v", res, err)
	}
}

func TestUnderDomains(t *testing.T) {
	for page, want := range map[string]bool{
		"https://cats.example/kittens":    true,
		"https://pics.cats.example/album": true,
		"https://notcats.example/":        false,
		"https://cats.example.evil/":      false,
		"https://docs.example/blog/post":  true,
		"https://docs.example/blogs/post": false,
		"https://docs.example/blog":       true,
		"https://Docs.Example/other":      false,
		"not a url":                       false,
	} {
		if got := underDomains(page, []string{"cats.example", "https://docs.example/blog/"}); got != want {
			t.Errorf("%s: %v", page, got)
		}
	}
}

// Settings lists are checked: names, kinds, and the tools two enabled servers would both offer
func TestCheck(t *testing.T) {
	good := []*v1.ToolServer{
		{Name: "web", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: "searxng", SearxngUrl: "http://s"},
		{Name: "web2", Enabled: false, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: "brave", BraveKey: "k"},
		{Name: "fetch", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_FETCH},
		{Name: "fs", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_STDIO, Command: "srv", Env: map[string]string{"HOME": "/x"}},
		{Name: "api", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_HTTP, Url: "https://mcp.example/x"},
	}
	if err := Check(good); err != nil {
		t.Fatal(err)
	}
	if good[0].GetId() == "" || good[0].GetId() == good[1].GetId() {
		t.Fatalf("ids %v", good)
	}
	if err := Check([]*v1.ToolServer{{Name: "fs", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_STDIO, Command: "srv", Env: map[string]string{"A=B": "x"}}}); !errors.Is(err, ErrServer) {
		t.Fatalf("env name: %v", err)
	}
}

// Brave is asked under its token with the country, its snippets join, and a refused token or an
// overlong query are named
func TestBrave(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"web": map[string]any{"results": []map[string]any{{"title": "Kittens", "url": "https://cats.example/kittens", "description": "Small cats.", "extra_snippets": []string{"Very fluffy."}, "page_age": "2024-05-01T00:00:00"}}}})
	}))
	t.Cleanup(api.Close)
	real := braveEndpoint
	braveEndpoint = api.URL
	t.Cleanup(func() { braveEndpoint = real })
	s, err := newSearch(&v1.ToolServer{Name: "web", Provider: "brave", BraveKey: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := s.Call(ctx, Call{Name: SearchTool, Args: json.RawMessage(`{"query":"cute kittens"}`), Country: "US"})
	if err != nil || len(res.Hits) != 1 || res.Hits[0].Snippet != "Small cats. Very fluffy." || res.Hits[0].Age != "2024-05-01T00:00:00" {
		t.Fatalf("brave: %+v %v", res, err)
	}
	b := brave{key: "tok", client: &http.Client{}}
	if _, err := b.search(ctx, searchAsk{Query: "x", Country: "AQ"}); err != nil {
		t.Fatalf("a country Brave does not list is left out: %v", err)
	}
	b.key = "wrong"
	if _, err := b.search(ctx, searchAsk{Query: "x"}); err == nil || !strings.Contains(err.Error(), "refuses the subscription token") {
		t.Fatalf("bad token: %v", err)
	}
	if res, _ := s.Call(ctx, Call{Name: SearchTool, Args: json.RawMessage(`{"query":"` + strings.Repeat("kittens ", 60) + `"}`)}); res.Code != "query_too_long" {
		t.Fatalf("long query: %+v", res)
	}
}
