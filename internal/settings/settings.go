// Package settings stores host preferences.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/tools"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Returned when a setting is malformed
var ErrSetting = errors.New("invalid setting")

// Event ID for settings updates.
const ID = "settings"

const labelMax = 64

// The settings row the web search provider lived in before tool servers
const legacySearchKey = "web_search"

// Stores settings and publishes changes to the UI.
type Manager struct {
	DB     *db.DB
	Events *events.Bus
	// Names what still attaches a tool server, so one in use is not removed
	Referenced func(name string) []string
	// Runs after settings change, before the change is published
	OnUpdate func(context.Context, *v1.Settings)
	// Refuses settings the daemon cannot run with, given what they come to over the config file
	Check func(effective *v1.Settings) error
	// Puts what the settings come to over the config file into force
	Apply func(effective *v1.Settings) error

	mu      sync.Mutex
	current *v1.Settings
	// What the config file gives every setting the stored ones leave unset
	base *v1.Settings
}

// The sections a stored setting replaces whole when set
var sections = []string{"downloads", "storage", "notifications", "sign_in"}

// The stored settings with the config file's value in every section and level left unset
func merge(stored, base *v1.Settings) *v1.Settings {
	out := proto.Clone(stored).(*v1.Settings)
	if out.GetLogLevel() == "" {
		out.LogLevel = base.GetLogLevel()
	}
	o, b := out.ProtoReflect(), base.ProtoReflect()
	for _, name := range sections {
		fd := o.Descriptor().Fields().ByName(protoreflect.Name(name))
		if !o.Has(fd) && b.Has(fd) {
			o.Set(fd, protoreflect.ValueOfMessage(proto.Clone(b.Get(fd).Message().Interface()).ProtoReflect()))
		}
	}
	return out
}

// Unsets every section and level that only repeats the config file, so the file is followed again
func follow(next, base *v1.Settings) {
	if next.GetLogLevel() == base.GetLogLevel() {
		next.LogLevel = ""
	}
	n, b := next.ProtoReflect(), base.ProtoReflect()
	for _, name := range sections {
		fd := n.Descriptor().Fields().ByName(protoreflect.Name(name))
		if n.Has(fd) && b.Has(fd) && proto.Equal(n.Get(fd).Message().Interface(), b.Get(fd).Message().Interface()) {
			n.Clear(fd)
		}
	}
}

// What the config file gives every setting left unset, a copy
func (m *Manager) Base() *v1.Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return proto.Clone(m.base).(*v1.Settings)
}

// The settings in force, the stored ones over the config file's
func (m *Manager) Effective() *v1.Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return merge(m.current, m.base)
}

// Takes what a loaded config file gives the unset settings, puts the result into force and tells the UI
func (m *Manager) Rebase(base *v1.Settings) error {
	m.mu.Lock()
	effective := merge(m.current, base)
	m.mu.Unlock()
	if m.Apply != nil {
		if err := m.Apply(effective); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.base = proto.Clone(base).(*v1.Settings)
	shown := proto.Clone(m.current).(*v1.Settings)
	m.mu.Unlock()
	if m.Events != nil {
		m.Events.Publish(v1.EventKind_EVENT_KIND_SETTINGS, v1.EventAction_EVENT_ACTION_UPDATED, ID, shown)
	}
	return nil
}

// Loads settings and serves later reads from memory, carrying an older web search provider over
func (m *Manager) Load(ctx context.Context) error {
	s, err := m.DB.GetSettings(ctx)
	if err != nil {
		return err
	}
	if err := tools.Check(s.ToolServers); err != nil {
		return err
	}
	if legacy, err := m.legacySearch(ctx, s); err != nil {
		return err
	} else if legacy != nil {
		s.ToolServers = append(s.ToolServers, legacy)
		if err := tools.Check(s.ToolServers); err != nil {
			return err
		}
		if err := m.DB.PutSettings(ctx, s); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.current = s
	m.mu.Unlock()
	return nil
}

// The search server an older web_search row describes, nil when none or a search server exists
func (m *Manager) legacySearch(ctx context.Context, s *v1.Settings) (*v1.ToolServer, error) {
	raw, ok, err := m.DB.SettingRow(ctx, legacySearchKey)
	if err != nil || !ok {
		return nil, err
	}
	var w struct {
		Provider   string `json:"provider"`
		SearxngURL string `json:"searxng_url"`
		BraveKey   string `json:"brave_key"`
	}
	if json.Unmarshal(raw, &w) != nil || w.Provider == "" {
		return nil, nil
	}
	for _, t := range s.GetToolServers() {
		if t.GetKind() == v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH {
			return nil, nil
		}
	}
	name := "web"
	for _, t := range s.GetToolServers() {
		if t.GetName() == name {
			name = "web_search"
		}
	}
	return &v1.ToolServer{Name: name, Enabled: true, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH, Provider: w.Provider, SearxngUrl: w.SearxngURL, BraveKey: w.BraveKey}, nil
}

// A copy of the settings
func (m *Manager) Get() *v1.Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return proto.Clone(m.current).(*v1.Settings)
}

// The parameter values launches and plans of a runtime start from, a copy, none when the settings keep none
func (m *Manager) Defaults(runtimeID string) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.current.GetRuntimeDefaults()[runtimeID]
	if len(d.GetParams()) == 0 {
		return nil
	}
	out := make(map[string]string, len(d.GetParams()))
	for k, v := range d.GetParams() {
		out[k] = v
	}
	return out
}

// Drops blank parameter names and values and runtimes left without any
func tidyDefaults(in map[string]*v1.RuntimeDefaults) map[string]*v1.RuntimeDefaults {
	out := map[string]*v1.RuntimeDefaults{}
	for id, d := range in {
		params := map[string]string{}
		for k, v := range d.GetParams() {
			if k, v = strings.TrimSpace(k), strings.TrimSpace(v); k != "" && v != "" {
				params[k] = v
			}
		}
		if id = strings.TrimSpace(id); id != "" && len(params) > 0 {
			out[id] = &v1.RuntimeDefaults{Params: params}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Replaces the settings after checking them, keeping a tool server something attaches
func (m *Manager) Update(ctx context.Context, in *v1.Settings) (*v1.Settings, error) {
	next := proto.Clone(in).(*v1.Settings)
	next.HostLabel = strings.Join(strings.Fields(next.GetHostLabel()), " ")
	next.RuntimeDefaults = tidyDefaults(next.GetRuntimeDefaults())
	if utf8.RuneCountInString(next.GetHostLabel()) > labelMax {
		return nil, fmt.Errorf("%w: the host label is longer than %d characters", ErrSetting, labelMax)
	}
	if err := tools.Check(next.ToolServers); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSetting, err)
	}
	if err := m.removable(next); err != nil {
		return nil, err
	}
	if err := tidySections(next); err != nil {
		return nil, err
	}
	base := m.Base()
	follow(next, base)
	effective := merge(next, base)
	if m.Check != nil {
		if err := m.Check(effective); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSetting, err)
		}
	}
	if err := m.DB.PutSettings(ctx, next); err != nil {
		return nil, err
	}
	if m.Apply != nil {
		if err := m.Apply(effective); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	m.current = next
	m.mu.Unlock()
	if m.OnUpdate != nil {
		m.OnUpdate(ctx, proto.Clone(next).(*v1.Settings))
	}
	shown := proto.Clone(next).(*v1.Settings)
	if m.Events != nil {
		m.Events.Publish(v1.EventKind_EVENT_KIND_SETTINGS, v1.EventAction_EVENT_ACTION_UPDATED, ID, shown)
	}
	return shown, nil
}

// Trims a webhook and refuses one without a name, an http or https URL with a host, or an event, the format defaulting to the event document
func CheckWebhook(w *v1.Webhook) error {
	w.Name = strings.Join(strings.Fields(w.GetName()), " ")
	if w.GetName() == "" || utf8.RuneCountInString(w.GetName()) > labelMax {
		return fmt.Errorf("%w: a webhook needs a name of at most %d characters", ErrSetting, labelMax)
	}
	if !webhookTarget(w) {
		return fmt.Errorf("%w: %s: the URL must start with http:// or https:// and name a host", ErrSetting, w.GetName())
	}
	slices.Sort(w.Events)
	w.Events = slices.Compact(w.Events)
	if len(w.GetEvents()) == 0 {
		return fmt.Errorf("%w: %s: a webhook needs at least one event", ErrSetting, w.GetName())
	}
	return nil
}

// Trims a webhook down to what a sample needs and refuses one without an http or https URL with a host
func CheckWebhookTarget(w *v1.Webhook) error {
	if !webhookTarget(w) {
		return fmt.Errorf("%w: the URL must start with http:// or https:// and name a host", ErrSetting)
	}
	return nil
}

// Trims the URL and defaults the format to the event document, answering whether the URL is http or https with a host
func webhookTarget(w *v1.Webhook) bool {
	w.Url = strings.TrimSpace(w.GetUrl())
	if w.GetFormat() == v1.WebhookFormat_WEBHOOK_FORMAT_UNSPECIFIED {
		w.Format = v1.WebhookFormat_WEBHOOK_FORMAT_EVENT
	}
	u, err := url.Parse(w.GetUrl())
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

// Trims the lists of the sections, gives new webhooks their ids, and refuses a bad webhook or two sharing a name or id
func tidySections(next *v1.Settings) error {
	names, ids := map[string]bool{}, map[string]bool{}
	for _, w := range next.GetNotifications().GetWebhooks() {
		if err := CheckWebhook(w); err != nil {
			return err
		}
		if w.GetId() == "" {
			w.Id = db.NewID()
		}
		if names[w.GetName()] || ids[w.GetId()] {
			return fmt.Errorf("%w: two webhooks share the name or id of %s", ErrSetting, w.GetName())
		}
		names[w.GetName()], ids[w.GetId()] = true, true
	}
	if s := next.GetSignIn(); s != nil {
		s.TrustedProxies = trimmed(s.GetTrustedProxies())
	}
	for _, w := range next.GetDownloads().GetWindows() {
		w.Days, w.From, w.To = strings.TrimSpace(w.GetDays()), strings.TrimSpace(w.GetFrom()), strings.TrimSpace(w.GetTo())
	}
	return nil
}

// The values without blanks or repeats, in their order
func trimmed(in []string) []string {
	var out []string
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// Refuses dropping a tool server something still attaches
func (m *Manager) removable(next *v1.Settings) error {
	if m.Referenced == nil {
		return nil
	}
	keep := map[string]bool{}
	for _, t := range next.GetToolServers() {
		keep[t.GetName()] = true
	}
	for _, t := range m.Get().GetToolServers() {
		if keep[t.GetName()] {
			continue
		}
		if users := m.Referenced(t.GetName()); len(users) > 0 {
			return fmt.Errorf("%w: tool server %s is still attached to %s", ErrSetting, t.GetName(), strings.Join(users, ", "))
		}
	}
	return nil
}
