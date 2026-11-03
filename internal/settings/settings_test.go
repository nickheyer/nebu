package settings

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/tools"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	_ "modernc.org/sqlite"
)

func newManager(t *testing.T) (*Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nebu.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	m := &Manager{DB: store}
	if err := m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m, path
}

func search(name, url string) *v1.ToolServer {
	return &v1.ToolServer{Name: name, Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: "searxng", SearxngUrl: url}
}

// Tool servers are trimmed, given ids, and kept, and ones missing what their kind needs or
// clashing by name or by the tool they offer are refused
func TestToolServerSettings(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	for _, bad := range [][]*v1.ToolServer{
		{{Name: "web", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: "searxng"}},
		{search("web", "searx.local")},
		{search("web", "ftp://searx.local")},
		{{Name: "web", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: "brave"}},
		{{Name: "web", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: "google", BraveKey: "k"}},
		{{Name: "fs", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_STDIO}},
		{{Name: "api", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_HTTP, Url: "mcp.example"}},
		{{Name: "bad name", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_FETCH}},
		{{Name: "two__under", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_FETCH}},
		{{Name: "x", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_FETCH}, {Name: "x", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_STDIO, Command: "srv"}},
		{search("a", "http://a"), search("b", "http://b")},
		{{Name: "f1", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_FETCH}, {Name: "f2", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_FETCH}},
		{{Name: "k", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_UNSPECIFIED}},
	} {
		if _, err := m.Update(ctx, &v1.Settings{ToolServers: bad}); !errors.Is(err, ErrSetting) {
			t.Fatalf("%v: %v", bad, err)
		}
	}
	got, err := m.Update(ctx, &v1.Settings{HostLabel: "box", ToolServers: []*v1.ToolServer{
		{Name: " web ", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: " searxng ", SearxngUrl: " http://searx.local:8080/ "},
		{Name: "brave", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: "brave", BraveKey: "k"},
		{Name: "fs", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_STDIO, Command: " npx ", Args: []string{" -y ", "", "server"}, AllowedTools: []string{" read_file ", ""}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	web := got.GetToolServers()[0]
	if web.GetName() != "web" || web.GetProvider() != "searxng" || web.GetSearxngUrl() != "http://searx.local:8080" || web.GetId() == "" {
		t.Fatalf("stored %v", web)
	}
	fs := got.GetToolServers()[2]
	if fs.GetCommand() != "npx" || strings.Join(fs.GetArgs(), " ") != "-y server" || strings.Join(fs.GetAllowedTools(), ",") != "read_file" {
		t.Fatalf("stored %v", fs)
	}
	again := &Manager{DB: m.DB}
	if err := again.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if len(again.Get().GetToolServers()) != 3 || again.Get().GetToolServers()[0].GetId() != web.GetId() || again.Get().GetHostLabel() != "box" {
		t.Fatalf("loaded %v", again.Get())
	}
	if got, err := m.Update(ctx, &v1.Settings{ToolServers: []*v1.ToolServer{{Name: "b", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: "brave", BraveKey: " tok "}}}); err != nil || m.Full().GetToolServers()[0].GetBraveKey() != "tok" {
		t.Fatalf("brave: %v %v", got, err)
	}
}

// Reads, update answers, and events carry the marker for every secret, and the marker sent back keeps the stored value
func TestSecretsRedactedAndKept(t *testing.T) {
	m, _ := newManager(t)
	bus := events.New()
	m.Events = bus
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := bus.Subscribe(ctx, []v1.EventKind{v1.EventKind_EVENT_KIND_SETTINGS})
	var applied *v1.Settings
	m.OnUpdate = func(_ context.Context, s *v1.Settings) { applied = s }
	in := &v1.Settings{ToolServers: []*v1.ToolServer{
		{Name: "brave", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: "brave", BraveKey: "brave-secret"},
		{Name: "fs", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_STDIO, Command: "srv", Env: map[string]string{"API_KEY": "env-secret", "MODE": "fast"}},
		{Name: "api", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_HTTP, Url: "https://mcp.example", Headers: map[string]string{"Authorization": "Bearer header-secret"}},
		{Name: "fetch", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_FETCH},
	}}
	got, err := m.Update(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var ev *v1.Event
	select {
	case ev = <-sub.Events():
	case <-time.After(5 * time.Second):
		t.Fatal("no settings event")
	}
	for name, s := range map[string]*v1.Settings{"answer": got, "read": m.Get(), "event": ev.GetSettings()} {
		servers := s.GetToolServers()
		if len(servers) != 4 {
			t.Fatalf("%s %v", name, s)
		}
		if b := servers[0]; b.GetBraveKey() != auth.Redacted || !b.GetSecretsSet() {
			t.Fatalf("%s brave %v", name, b)
		}
		if f := servers[1]; f.GetEnv()["API_KEY"] != auth.Redacted || f.GetEnv()["MODE"] != auth.Redacted || !f.GetSecretsSet() {
			t.Fatalf("%s env %v", name, f)
		}
		if a := servers[2]; a.GetHeaders()["Authorization"] != auth.Redacted || !a.GetSecretsSet() {
			t.Fatalf("%s headers %v", name, a)
		}
		if f := servers[3]; f.GetSecretsSet() {
			t.Fatalf("%s fetch holds no secret %v", name, f)
		}
		if strings.Contains(s.String(), "-secret") {
			t.Fatalf("%s carries a secret %v", name, s)
		}
	}
	full := m.Full().GetToolServers()
	if full[0].GetBraveKey() != "brave-secret" || full[1].GetEnv()["API_KEY"] != "env-secret" || full[2].GetHeaders()["Authorization"] != "Bearer header-secret" || full[0].GetSecretsSet() {
		t.Fatalf("stored %v", full)
	}
	if applied.GetToolServers()[0].GetBraveKey() != "brave-secret" {
		t.Fatalf("the tool registry is handed the real key %v", applied)
	}
	// The UI sends back what it read with one value changed and one header added
	back := m.Get()
	back.ToolServers[1].Env["MODE"] = "slow"
	back.ToolServers[2].Headers["X-Extra"] = "plain"
	if _, err := m.Update(ctx, back); err != nil {
		t.Fatal(err)
	}
	full = m.Full().GetToolServers()
	if full[0].GetBraveKey() != "brave-secret" || full[1].GetEnv()["API_KEY"] != "env-secret" || full[1].GetEnv()["MODE"] != "slow" ||
		full[2].GetHeaders()["Authorization"] != "Bearer header-secret" || full[2].GetHeaders()["X-Extra"] != "plain" {
		t.Fatalf("kept %v", full)
	}
	again := &Manager{DB: m.DB}
	if err := again.Load(ctx); err != nil || again.Full().GetToolServers()[1].GetEnv()["API_KEY"] != "env-secret" {
		t.Fatalf("persisted %v %v", again.Full(), err)
	}
	// A marker with no stored value behind it is refused rather than stored
	for _, bad := range []*v1.ToolServer{
		{Name: "new", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: "brave", BraveKey: auth.Redacted},
		{Id: full[1].GetId(), Name: "fs", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_STDIO, Command: "srv", Env: map[string]string{"OTHER": auth.Redacted}},
		{Id: full[2].GetId(), Name: "api", Kind: v1.ToolServerKind_TOOL_SERVER_KIND_HTTP, Url: "https://mcp.example", Headers: map[string]string{"Cookie": auth.Redacted}},
	} {
		if _, err := m.Update(ctx, &v1.Settings{ToolServers: []*v1.ToolServer{bad}}); !errors.Is(err, ErrSetting) {
			t.Fatalf("%v gave %v", bad, err)
		}
	}
	if m.Full().GetToolServers()[0].GetBraveKey() != "brave-secret" {
		t.Fatal("a refused update changed the store")
	}
}

// A tool server a route or persona still attaches cannot be dropped, and the refusal names them
func TestToolServerReferenced(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	m.Referenced = func(name string) []string {
		if name == "web" {
			return []string{"route coder", "persona Ada of bot Nebu"}
		}
		return nil
	}
	if _, err := m.Update(ctx, &v1.Settings{ToolServers: []*v1.ToolServer{search("web", "http://s"), {Name: "fetch", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_FETCH}}}); err != nil {
		t.Fatal(err)
	}
	_, err := m.Update(ctx, &v1.Settings{ToolServers: []*v1.ToolServer{{Name: "fetch", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_FETCH}}})
	if !errors.Is(err, ErrSetting) || !strings.Contains(err.Error(), "route coder, persona Ada of bot Nebu") {
		t.Fatalf("dropping a used server: %v", err)
	}
	if _, err := m.Update(ctx, &v1.Settings{ToolServers: []*v1.ToolServer{search("web", "http://s")}}); err != nil {
		t.Fatalf("dropping an unused server: %v", err)
	}
}

// A web search provider stored before tool servers becomes a search server on load, once
func TestLegacyWebSearch(t *testing.T) {
	m, path := newManager(t)
	ctx := context.Background()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('web_search', '{"provider":"brave","searxng_url":"","brave_key":"tok"}')`); err != nil {
		t.Fatal(err)
	}
	again := &Manager{DB: m.DB}
	if err := again.Load(ctx); err != nil {
		t.Fatal(err)
	}
	servers := again.Full().GetToolServers()
	if len(servers) != 1 || servers[0].GetName() != "web" || servers[0].GetKind() != v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH || servers[0].GetProvider() != "brave" || servers[0].GetBraveKey() != "tok" || !servers[0].GetEnabled() {
		t.Fatalf("migrated %v", servers)
	}
	if _, ok, err := m.DB.SettingRow(ctx, "web_search"); err != nil || ok {
		t.Fatalf("the old row stays: %v %v", ok, err)
	}
	if err := tools.Check(servers); err != nil {
		t.Fatal(err)
	}
}
