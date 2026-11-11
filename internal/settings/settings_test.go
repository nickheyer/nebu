package settings

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if got, err := m.Update(ctx, &v1.Settings{ToolServers: []*v1.ToolServer{{Name: "b", Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: "brave", BraveKey: " tok "}}}); err != nil || m.Get().GetToolServers()[0].GetBraveKey() != "tok" {
		t.Fatalf("brave: %v %v", got, err)
	}
}

// Runtime defaults keep trimmed names and values, drop blank ones and empty runtimes, survive a reload, and read back as copies
func TestRuntimeDefaults(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	if m.Defaults("llamacpp") != nil {
		t.Fatal("nothing kept means no defaults")
	}
	got, err := m.Update(ctx, &v1.Settings{RuntimeDefaults: map[string]*v1.RuntimeDefaults{
		"llamacpp": {Params: map[string]string{" n_ctx ": " 8192 ", "flash_attn": "on", "blank": " ", "": "x"}},
		"vllm":     {Params: map[string]string{"gpu_memory_utilization": ""}},
		" ":        {Params: map[string]string{"a": "b"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.GetRuntimeDefaults()) != 1 || got.GetRuntimeDefaults()["llamacpp"].GetParams()["n_ctx"] != "8192" || len(got.GetRuntimeDefaults()["llamacpp"].GetParams()) != 2 {
		t.Fatalf("stored %v", got.GetRuntimeDefaults())
	}
	again := &Manager{DB: m.DB}
	if err := again.Load(ctx); err != nil {
		t.Fatal(err)
	}
	d := again.Defaults("llamacpp")
	if d["n_ctx"] != "8192" || d["flash_attn"] != "on" || again.Defaults("vllm") != nil {
		t.Fatalf("loaded %v", d)
	}
	d["n_ctx"] = "1"
	if again.Defaults("llamacpp")["n_ctx"] == "1" {
		t.Fatal("a caller's edit must not reach the stored defaults")
	}
}

// Reads, answers, and events carry every secret as stored, and what is read back and sent again stays
func TestSecretsReadBack(t *testing.T) {
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
		if b := servers[0]; b.GetBraveKey() != "brave-secret" {
			t.Fatalf("%s brave %v", name, b)
		}
		if f := servers[1]; f.GetEnv()["API_KEY"] != "env-secret" || f.GetEnv()["MODE"] != "fast" {
			t.Fatalf("%s env %v", name, f)
		}
		if a := servers[2]; a.GetHeaders()["Authorization"] != "Bearer header-secret" {
			t.Fatalf("%s headers %v", name, a)
		}
	}
	full := m.Get().GetToolServers()
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
	full = m.Get().GetToolServers()
	if full[0].GetBraveKey() != "brave-secret" || full[1].GetEnv()["API_KEY"] != "env-secret" || full[1].GetEnv()["MODE"] != "slow" ||
		full[2].GetHeaders()["Authorization"] != "Bearer header-secret" || full[2].GetHeaders()["X-Extra"] != "plain" {
		t.Fatalf("kept %v", full)
	}
	again := &Manager{DB: m.DB}
	if err := again.Load(ctx); err != nil || again.Get().GetToolServers()[1].GetEnv()["API_KEY"] != "env-secret" {
		t.Fatalf("persisted %v %v", again.Get(), err)
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
	servers := again.Get().GetToolServers()
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

// A stored section stands over the config file's, one equal to the file's is dropped so the file is followed again
func TestSettingsLayerOverTheConfigFile(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	base := &v1.Settings{
		LogLevel:      "info",
		Downloads:     &v1.Downloads{Connections: 4, Retries: 5},
		Storage:       &v1.DiskLimits{MinFreeBytes: 1 << 30},
		Notifications: &v1.Notifications{},
	}
	var applied *v1.Settings
	m.Apply = func(s *v1.Settings) error { applied = s; return nil }
	m.Check = func(s *v1.Settings) error {
		if s.GetSignIn() != nil {
			return errors.New("authentication is off")
		}
		return nil
	}
	if err := m.Rebase(base); err != nil {
		t.Fatal(err)
	}
	if applied.GetDownloads().GetConnections() != 4 || applied.GetLogLevel() != "info" {
		t.Fatalf("the config file's values are not in force: %v", applied)
	}
	in := m.Get()
	in.LogLevel = "debug"
	in.Downloads = &v1.Downloads{Connections: 8, Retries: 5}
	in.Storage = &v1.DiskLimits{MinFreeBytes: 1 << 30}
	in.Notifications = &v1.Notifications{Webhooks: []*v1.Webhook{{Name: " ops  chat ", Url: " https://hooks.example/a ", Enabled: true, Events: []v1.NotifyEvent{v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED, v1.NotifyEvent_NOTIFY_EVENT_MODEL_FAILED, v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED}}}}
	got, err := m.Update(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetStorage() != nil {
		t.Errorf("a storage section equal to the config file's was stored: %v", got.GetStorage())
	}
	if applied.GetDownloads().GetConnections() != 8 || applied.GetLogLevel() != "debug" || applied.GetStorage().GetMinFreeBytes() != 1<<30 {
		t.Errorf("in force after the save: %v", applied)
	}
	hooks := applied.GetNotifications().GetWebhooks()
	if len(hooks) != 1 || hooks[0].GetUrl() != "https://hooks.example/a" || hooks[0].GetName() != "ops chat" || hooks[0].GetId() == "" || hooks[0].GetFormat() != v1.WebhookFormat_WEBHOOK_FORMAT_EVENT || len(hooks[0].GetEvents()) != 2 {
		t.Errorf("webhooks in force: %v", hooks)
	}
	base.Downloads.Connections, base.Storage.MinFreeBytes = 2, 5<<30
	if err := m.Rebase(base); err != nil {
		t.Fatal(err)
	}
	if applied.GetDownloads().GetConnections() != 8 || applied.GetStorage().GetMinFreeBytes() != 5<<30 {
		t.Errorf("after a config reload the stored downloads or the file's storage are not in force: %v", applied)
	}
	in = m.Get()
	in.Downloads = &v1.Downloads{Connections: 2, Retries: 5}
	in.LogLevel = "info"
	if got, err = m.Update(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got.GetDownloads() != nil || got.GetLogLevel() != "" {
		t.Errorf("values set back to the config file's stayed stored: %v", got)
	}
	for name, bad := range map[string]*v1.Settings{
		"webhook url":   {Notifications: &v1.Notifications{Webhooks: []*v1.Webhook{{Name: "a", Url: "hooks.example", Events: []v1.NotifyEvent{v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED}}}}},
		"webhook event": {Notifications: &v1.Notifications{Webhooks: []*v1.Webhook{{Name: "a", Url: "https://hooks.example"}}}},
		"webhook name":  {Notifications: &v1.Notifications{Webhooks: []*v1.Webhook{{Url: "https://hooks.example", Events: []v1.NotifyEvent{v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED}}}}},
		"webhook twice": {Notifications: &v1.Notifications{Webhooks: []*v1.Webhook{{Name: "a", Url: "https://hooks.example", Events: []v1.NotifyEvent{v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED}}, {Name: "a", Url: "https://other.example", Events: []v1.NotifyEvent{v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED}}}}},
		"sign in":       {SignIn: &v1.SignIn{SessionSeconds: 3600}},
	} {
		if _, err := m.Update(ctx, bad); !errors.Is(err, ErrSetting) {
			t.Errorf("%s: err = %v, want ErrSetting", name, err)
		}
	}
	if stored, err := m.DB.GetSettings(ctx); err != nil || stored.GetNotifications().GetWebhooks()[0].GetUrl() != "https://hooks.example/a" || stored.GetSignIn() != nil {
		t.Errorf("stored after the refusals: %v %v", stored, err)
	}
}

// A sample needs a URL alone, trimmed and http or https with a host, the format defaulting to the event document
func TestWebhookTarget(t *testing.T) {
	w := &v1.Webhook{Url: " https://hooks.example/a "}
	if err := CheckWebhookTarget(w); err != nil || w.GetUrl() != "https://hooks.example/a" || w.GetFormat() != v1.WebhookFormat_WEBHOOK_FORMAT_EVENT {
		t.Errorf("a webhook holding a URL alone: %v err %v", w, err)
	}
	for _, bad := range []string{"", "hooks.example", "ftp://hooks.example", "https://"} {
		if err := CheckWebhookTarget(&v1.Webhook{Name: "a", Url: bad, Events: []v1.NotifyEvent{v1.NotifyEvent_NOTIFY_EVENT_TASK_FAILED}}); !errors.Is(err, ErrSetting) {
			t.Errorf("%q: err = %v, want ErrSetting", bad, err)
		}
	}
}
