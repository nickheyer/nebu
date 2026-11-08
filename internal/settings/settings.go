// Package settings stores host preferences.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/tools"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
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

	mu      sync.Mutex
	current *v1.Settings
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

// A copy of the settings with tool server secrets replaced by the redaction marker, what reads and events carry
func (m *Manager) Get() *v1.Settings {
	return redact(m.Full())
}

// A copy of the settings with every secret, for the daemon's own use and never for a client
func (m *Manager) Full() *v1.Settings {
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

// Copies settings with each tool server redacted
func redact(s *v1.Settings) *v1.Settings {
	out := proto.Clone(s).(*v1.Settings)
	out.ToolServers = tools.RedactAll(out.GetToolServers())
	return out
}

// Replaces the settings after checking them, keeping stored secrets sent back as the marker and a tool server something attaches
func (m *Manager) Update(ctx context.Context, in *v1.Settings) (*v1.Settings, error) {
	next := proto.Clone(in).(*v1.Settings)
	next.HostLabel = strings.Join(strings.Fields(next.GetHostLabel()), " ")
	next.RuntimeDefaults = tidyDefaults(next.GetRuntimeDefaults())
	if utf8.RuneCountInString(next.GetHostLabel()) > labelMax {
		return nil, fmt.Errorf("%w: the host label is longer than %d characters", ErrSetting, labelMax)
	}
	if err := tools.Restore(next.ToolServers, m.Full().GetToolServers()); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSetting, err)
	}
	if err := tools.Check(next.ToolServers); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSetting, err)
	}
	if err := m.removable(next); err != nil {
		return nil, err
	}
	if err := m.DB.PutSettings(ctx, next); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.current = next
	m.mu.Unlock()
	if m.OnUpdate != nil {
		m.OnUpdate(ctx, proto.Clone(next).(*v1.Settings))
	}
	shown := redact(next)
	if m.Events != nil {
		m.Events.Publish(v1.EventKind_EVENT_KIND_SETTINGS, v1.EventAction_EVENT_ACTION_UPDATED, ID, shown)
	}
	return shown, nil
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
	for _, t := range m.Full().GetToolServers() {
		if keep[t.GetName()] {
			continue
		}
		if users := m.Referenced(t.GetName()); len(users) > 0 {
			return fmt.Errorf("%w: tool server %s is still attached to %s", ErrSetting, t.GetName(), strings.Join(users, ", "))
		}
	}
	return nil
}
