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

// Returns a copy of the settings
func (m *Manager) Get() *v1.Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return proto.Clone(m.current).(*v1.Settings)
}

// Replaces the settings after checking them, keeping a tool server something still attaches
func (m *Manager) Update(ctx context.Context, in *v1.Settings) (*v1.Settings, error) {
	next := proto.Clone(in).(*v1.Settings)
	next.HostLabel = strings.Join(strings.Fields(next.GetHostLabel()), " ")
	if utf8.RuneCountInString(next.GetHostLabel()) > labelMax {
		return nil, fmt.Errorf("%w: the host label is longer than %d characters", ErrSetting, labelMax)
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
	if m.Events != nil {
		m.Events.Publish(v1.EventKind_EVENT_KIND_SETTINGS, v1.EventAction_EVENT_ACTION_UPDATED, ID, next)
	}
	return proto.Clone(next).(*v1.Settings), nil
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
