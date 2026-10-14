// Package settings stores host preferences.
package settings

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// Returned when a setting is malformed
var ErrSetting = errors.New("invalid setting")

// Event ID for settings updates.
const ID = "settings"

const labelMax = 64

// Stores settings and publishes changes to the UI.
type Manager struct {
	DB     *db.DB
	Events *events.Bus

	mu      sync.Mutex
	current *v1.Settings
}

// Loads settings and serves later reads from memory.
func (m *Manager) Load(ctx context.Context) error {
	s, err := m.DB.GetSettings(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.current = s
	m.mu.Unlock()
	return nil
}

// Returns a copy of the settings
func (m *Manager) Get() *v1.Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return proto.Clone(m.current).(*v1.Settings)
}

// Replaces the settings after checking them
func (m *Manager) Update(ctx context.Context, in *v1.Settings) (*v1.Settings, error) {
	next := proto.Clone(in).(*v1.Settings)
	next.HostLabel = strings.Join(strings.Fields(next.GetHostLabel()), " ")
	if utf8.RuneCountInString(next.GetHostLabel()) > labelMax {
		return nil, fmt.Errorf("%w: the host label is longer than %d characters", ErrSetting, labelMax)
	}
	if err := checkWebSearch(next.GetWebSearch()); err != nil {
		return nil, err
	}
	if err := m.DB.PutSettings(ctx, next); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.current = next
	m.mu.Unlock()
	if m.Events != nil {
		m.Events.Publish(v1.EventKind_EVENT_KIND_SETTINGS, v1.EventAction_EVENT_ACTION_UPDATED, ID, next)
	}
	return proto.Clone(next).(*v1.Settings), nil
}

// Trims the web search settings and refuses a provider missing what it needs
func checkWebSearch(w *v1.WebSearch) error {
	if w == nil {
		return nil
	}
	w.Provider, w.SearxngUrl, w.BraveKey = strings.TrimSpace(w.GetProvider()), strings.TrimRight(strings.TrimSpace(w.GetSearxngUrl()), "/"), strings.TrimSpace(w.GetBraveKey())
	switch w.GetProvider() {
	case "":
		return nil
	case "searxng":
		u, err := url.Parse(w.GetSearxngUrl())
		if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("%w: the SearXNG URL must start with http:// or https:// and name a host", ErrSetting)
		}
		return nil
	case "brave":
		if w.GetBraveKey() == "" {
			return fmt.Errorf("%w: Brave Search needs a subscription token", ErrSetting)
		}
		return nil
	}
	return fmt.Errorf("%w: web search provider %q is not searxng or brave", ErrSetting, w.GetProvider())
}
