// Package settings keeps host wide preferences as rows.
package settings

import (
	"context"
	"errors"
	"fmt"
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

// The event id every settings change travels under
const ID = "settings"

const labelMax = 64

// Owns the settings row, the same way sources are owned: the rows are the
// truth and every change reaches the UI as an event
type Manager struct {
	DB     *db.DB
	Events *events.Bus

	mu      sync.Mutex
	current *v1.Settings
}

// Reads the settings once, later reads answering from memory
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
