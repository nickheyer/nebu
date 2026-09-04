package sources

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Where source rows are kept
type Store interface {
	ListSources(ctx context.Context) ([]*v1.Source, error)
	PutSource(ctx context.Context, s *v1.Source) error
	DeleteSource(ctx context.Context, id string) (bool, error)
}

// Owns the source rows and the registry built from them
//
// Seeded defaults and config entries become rows on Load, so from then on
// the rows are the only truth and the registry is rebuilt from them on
// every change. Rows that were added come first, oldest first, then the
// seeded defaults in kind order, so the first source is the one commands
// fall back to: your first added source, or huggingface with none.
type Manager struct {
	Store    Store
	Events   *events.Bus
	Log      *slog.Logger
	Registry *Registry

	mu   sync.Mutex
	rows []*v1.Source
	// Names what starts from a source, watches and wants, dropping them when clear is set
	Referrers func(id string, clear bool) []string
}

// Builds a manager with an empty registry, filled by Load; transports keep clones and scratch under cacheDir
func NewManager(store Store, bus *events.Bus, log *slog.Logger, cacheDir string) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{Store: store, Events: bus, Log: log, Registry: &Registry{CacheDir: cacheDir, byID: map[string]Source{}}}
}

// Ids are path segments in the store, so they stay plain
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Loads the rows, creating each bootstrap entry and seeded default whose id
// is absent, then builds the registry
//
// Bootstrap entries come first so a config file can define the source a
// provider's name gets; a seeded default fills in only when nothing has that
// name. An entry whose id exists is left alone, changed or not.
func (m *Manager) Load(ctx context.Context, bootstrap []*v1.Source) error {
	rows, err := m.Store.ListSources(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, r := range rows {
		have[r.GetId()] = true
	}
	for _, cfg := range bootstrap {
		if have[strings.TrimSpace(cfg.GetId())] {
			continue
		}
		// The row is kept on its shape alone, a client that cannot be built stays listed as broken
		row, err := m.prepare(cfg, false)
		if err != nil {
			return fmt.Errorf("config source %q: %w", cfg.GetId(), err)
		}
		if err := m.Store.PutSource(ctx, row); err != nil {
			return err
		}
		m.Log.Info("source created from config", "id", row.GetId(), "kind", row.GetKind())
		rows = append(rows, row)
		have[row.GetId()] = true
	}
	for _, seed := range Seeds() {
		if have[seed.GetId()] {
			continue
		}
		now := timestamppb.Now()
		seed.CreatedAt, seed.UpdatedAt = now, now
		if err := m.Store.PutSource(ctx, seed); err != nil {
			return err
		}
		rows = append(rows, seed)
		have[seed.GetId()] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = rows
	return m.rebuildLocked()
}

// Sorts the rows and rebuilds the registry from copies of them
func (m *Manager) rebuildLocked() error {
	sortRows(m.rows)
	cfgs := make([]*v1.Source, 0, len(m.rows))
	for _, r := range m.rows {
		cfgs = append(cfgs, clone(r))
	}
	return m.Registry.Reload(cfgs)
}

// Lists every source in fallback order
func (m *Manager) List() []*v1.Source {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*v1.Source, 0, len(m.rows))
	for _, r := range m.rows {
		out = append(out, clone(r))
	}
	return out
}

// Returns one source by id
func (m *Manager) Get(id string) (*v1.Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i := m.indexLocked(id); i >= 0 {
		return clone(m.rows[i]), nil
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownSource, id)
}

func (m *Manager) indexLocked(id string) int {
	for i, r := range m.rows {
		if r.GetId() == id {
			return i
		}
	}
	return -1
}

// Creates a source after checking that its provider accepts the settings
func (m *Manager) Create(ctx context.Context, in *v1.Source) (*v1.Source, error) {
	row, err := m.prepare(in, true)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.indexLocked(row.GetId()) >= 0 {
		return nil, fmt.Errorf("%w: source %q exists", ErrSource, row.GetId())
	}
	if err := m.Store.PutSource(ctx, row); err != nil {
		return nil, err
	}
	m.rows = append(m.rows, row)
	if err := m.rebuildLocked(); err != nil {
		return nil, err
	}
	m.publish(v1.EventAction_EVENT_ACTION_CREATED, row)
	return clone(row), nil
}

// Replaces the name and settings of a source; its id, kind, and origin stay
func (m *Manager) Update(ctx context.Context, in *v1.Source) (*v1.Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.indexLocked(in.GetId())
	if i < 0 {
		return nil, fmt.Errorf("%w %q", ErrUnknownSource, in.GetId())
	}
	row := m.rows[i]
	if k := in.GetKind(); k != v1.SourceKind_SOURCE_KIND_UNSPECIFIED && k != row.GetKind() {
		return nil, fmt.Errorf("%w: the kind of %s is fixed, remove it and add a source of the new kind", ErrSource, row.GetId())
	}
	next := clone(row)
	next.Name = strings.TrimSpace(in.GetName())
	next.Config = trimConfig(in.GetConfig())
	next.UpdatedAt = timestamppb.Now()
	if err := m.Registry.Check(next); err != nil {
		return nil, err
	}
	if err := m.Store.PutSource(ctx, next); err != nil {
		return nil, err
	}
	m.rows[i] = next
	if err := m.rebuildLocked(); err != nil {
		return nil, err
	}
	m.publish(v1.EventAction_EVENT_ACTION_UPDATED, next)
	return clone(next), nil
}

// Removes a source; seeded defaults stay, and one a watch or want names goes only when forced
func (m *Manager) Delete(ctx context.Context, id string, force bool) (*v1.Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.indexLocked(id)
	if i < 0 {
		return nil, fmt.Errorf("%w %q", ErrUnknownSource, id)
	}
	row := m.rows[i]
	if row.GetSeeded() {
		return nil, fmt.Errorf("%w: %s is a seeded default and stays, change its settings instead", ErrSource, id)
	}
	if m.Referrers != nil {
		if used := m.Referrers(id, false); len(used) > 0 && !force {
			return nil, fmt.Errorf("%w: %s is named by %s, remove them or remove with force to drop the references", ErrSourceInUse, id, strings.Join(used, ", "))
		} else if len(used) > 0 {
			m.Referrers(id, true)
		}
	}
	if _, err := m.Store.DeleteSource(ctx, id); err != nil {
		return nil, err
	}
	m.rows = append(m.rows[:i], m.rows[i+1:]...)
	if err := m.rebuildLocked(); err != nil {
		return nil, err
	}
	m.publish(v1.EventAction_EVENT_ACTION_DELETED, row)
	return clone(row), nil
}

func (m *Manager) publish(action v1.EventAction, s *v1.Source) {
	if m.Events != nil {
		m.Events.Publish(v1.EventKind_EVENT_KIND_SOURCE, action, s.GetId(), s)
	}
}

// Turns a request into a row: a plain id, settings the provider accepts, fresh stamps, the client built when asked
func (m *Manager) prepare(in *v1.Source, build bool) (*v1.Source, error) {
	id := strings.TrimSpace(in.GetId())
	if !validID.MatchString(id) || strings.Contains(id, "..") {
		return nil, fmt.Errorf("%w: id %q must be letters, digits, dots, dashes, or underscores", ErrSource, in.GetId())
	}
	row := clone(in)
	row.Id = id
	row.Name = strings.TrimSpace(in.GetName())
	row.Config = trimConfig(in.GetConfig())
	row.Seeded = false
	now := timestamppb.Now()
	row.CreatedAt, row.UpdatedAt = now, now
	check := m.Registry.Validate
	if build {
		check = m.Registry.Check
	}
	if err := check(row); err != nil {
		return nil, err
	}
	return row, nil
}

// Keeps the settings that carry a value, trimmed
func trimConfig(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if k, v = strings.TrimSpace(k), strings.TrimSpace(v); k != "" && v != "" {
			out[k] = v
		}
	}
	return out
}

// Added sources first, oldest first, then seeded defaults in kind order
func sortRows(rows []*v1.Source) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.GetSeeded() != b.GetSeeded() {
			return !a.GetSeeded()
		}
		if a.GetSeeded() {
			if a.GetKind() != b.GetKind() {
				return a.GetKind() < b.GetKind()
			}
			return a.GetId() < b.GetId()
		}
		at, bt := a.GetCreatedAt().AsTime(), b.GetCreatedAt().AsTime()
		if !at.Equal(bt) {
			return at.Before(bt)
		}
		return a.GetId() < b.GetId()
	})
}

func clone(s *v1.Source) *v1.Source { return proto.Clone(s).(*v1.Source) }
