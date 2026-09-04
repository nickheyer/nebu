// Package profiles keeps named param sets per runtime as rows.
package profiles

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtime"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	// Returned when a profile id or name is not known
	ErrUnknownProfile = errors.New("unknown profile")
	// Returned when a profile is malformed
	ErrProfile = errors.New("invalid profile")
	// Returned when a delete would strand a watch, want, slot, or instance
	ErrProfileInUse = errors.New("profile in use")
)

// Names what starts from a profile, clearing each reference when asked, set by the daemon
type Referrer func(p *v1.Profile, clear bool) []string

// Reports whether a reference by id or name means this profile
func Refers(p *v1.Profile, ref, runtimeID string) bool {
	if ref == "" {
		return false
	}
	return ref == p.GetId() || strings.EqualFold(ref, p.GetName()) && (runtimeID == "" || runtimeID == p.GetRuntimeId())
}

// Owns the profile rows, the same way sources are owned: rows are the truth
// and every change reaches the UI as an event
type Manager struct {
	DB       *db.DB
	Runtimes *runtime.Registry
	Events   *events.Bus
	Log      *slog.Logger
	// Nil means nothing outside this package can name a profile
	Referrers Referrer

	mu   sync.Mutex
	rows map[string]*v1.Profile
}

// Loads every profile from the store
func (m *Manager) Load(ctx context.Context) error {
	list, err := m.DB.ListProfiles(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = map[string]*v1.Profile{}
	for _, p := range list {
		m.rows[p.GetId()] = p
	}
	return nil
}

// Lists profiles by runtime then name, every runtime when empty
func (m *Manager) List(runtimeID string) []*v1.Profile {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*v1.Profile, 0, len(m.rows))
	for _, p := range m.rows {
		if runtimeID == "" || p.GetRuntimeId() == runtimeID {
			out = append(out, clone(p))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetRuntimeId() != out[j].GetRuntimeId() {
			return out[i].GetRuntimeId() < out[j].GetRuntimeId()
		}
		return out[i].GetName() < out[j].GetName()
	})
	return out
}

// Finds a profile by id, or by name, within one runtime when given
func (m *Manager) findLocked(runtimeID, ref string) (*v1.Profile, error) {
	found, ok := m.rows[ref]
	if !ok {
		for _, p := range m.rows {
			if (runtimeID != "" && p.GetRuntimeId() != runtimeID) || !strings.EqualFold(p.GetName(), ref) {
				continue
			}
			if found != nil {
				return nil, fmt.Errorf("%w: %q names a profile of both %s and %s, pass its id", ErrProfile, ref, found.GetRuntimeId(), p.GetRuntimeId())
			}
			found = p
		}
	}
	if found == nil {
		return nil, fmt.Errorf("%w %q", ErrUnknownProfile, ref)
	}
	if runtimeID != "" && found.GetRuntimeId() != runtimeID {
		return nil, fmt.Errorf("%w: %s belongs to %s, not %s", ErrProfile, found.GetName(), found.GetRuntimeId(), runtimeID)
	}
	return found, nil
}

// Returns the profile a run of a runtime starts from: the one named by id or
// name, which must belong to the runtime when one is given, else the runtime's
// default, nil when it has none
func (m *Manager) Resolve(runtimeID, ref string) (*v1.Profile, error) {
	if m == nil {
		if ref != "" {
			return nil, fmt.Errorf("%w %q", ErrUnknownProfile, ref)
		}
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ref != "" {
		p, err := m.findLocked(runtimeID, ref)
		if err != nil {
			return nil, err
		}
		return clone(p), nil
	}
	for _, p := range m.rows {
		if p.GetRuntimeId() == runtimeID && p.GetDefault() {
			return clone(p), nil
		}
	}
	return nil, nil
}

// Checks that the runtime exists and accepts every param value
func (m *Manager) check(p *v1.Profile) error {
	if strings.TrimSpace(p.GetName()) == "" {
		return fmt.Errorf("%w: name required", ErrProfile)
	}
	rt, err := m.Runtimes.Get(p.GetRuntimeId())
	if err != nil {
		return err
	}
	if _, err := rt.Params(p.GetParams()); err != nil {
		return fmt.Errorf("%w: %v", ErrProfile, err)
	}
	for _, other := range m.rows {
		if other.GetId() != p.GetId() && other.GetRuntimeId() == p.GetRuntimeId() && strings.EqualFold(other.GetName(), p.GetName()) {
			return fmt.Errorf("%w: %s already has a profile named %q", ErrProfile, p.GetRuntimeId(), p.GetName())
		}
	}
	return nil
}

// Writes a row, the store stepping the runtime's other default down in the same write, then follows it in memory
func (m *Manager) saveLocked(ctx context.Context, p *v1.Profile, action v1.EventAction) error {
	if err := m.DB.PutProfile(ctx, p); err != nil {
		return err
	}
	if p.GetDefault() {
		for _, other := range m.rows {
			if other.GetId() != p.GetId() && other.GetRuntimeId() == p.GetRuntimeId() && other.GetDefault() {
				other.Default = false
				other.UpdatedAt = p.GetUpdatedAt()
				m.publish(v1.EventAction_EVENT_ACTION_UPDATED, other)
			}
		}
	}
	m.rows[p.GetId()] = p
	m.publish(action, p)
	return nil
}

// Creates a profile
func (m *Manager) Create(ctx context.Context, in *v1.Profile) (*v1.Profile, error) {
	p := clone(in)
	p.Id = newID(p.GetRuntimeId())
	p.Name = strings.TrimSpace(p.GetName())
	p.Params = trim(p.GetParams())
	now := timestamppb.Now()
	p.CreatedAt, p.UpdatedAt = now, now
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check(p); err != nil {
		return nil, err
	}
	if err := m.saveLocked(ctx, p, v1.EventAction_EVENT_ACTION_CREATED); err != nil {
		return nil, err
	}
	return clone(p), nil
}

// Replaces the name, description, params, and default flag of a profile; its runtime stays
func (m *Manager) Update(ctx context.Context, in *v1.Profile) (*v1.Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, err := m.findLocked(in.GetRuntimeId(), in.GetId())
	if err != nil {
		return nil, err
	}
	next := clone(row)
	next.Name = strings.TrimSpace(in.GetName())
	next.Description = in.GetDescription()
	next.Params = trim(in.GetParams())
	next.Default = in.GetDefault()
	next.UpdatedAt = timestamppb.Now()
	if err := m.check(next); err != nil {
		return nil, err
	}
	if err := m.saveLocked(ctx, next, v1.EventAction_EVENT_ACTION_UPDATED); err != nil {
		return nil, err
	}
	return clone(next), nil
}

// Removes a profile by id or name, refusing while anything starts from it unless
// forced, which clears those references so they fall back to the runtime's default
func (m *Manager) Delete(ctx context.Context, ref string, force bool) (*v1.Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, err := m.findLocked("", ref)
	if err != nil {
		return nil, err
	}
	if m.Referrers != nil {
		if used := m.Referrers(row, false); len(used) > 0 && !force {
			return nil, fmt.Errorf("%w: %s is named by %s, point them elsewhere or remove with force to clear them", ErrProfileInUse, row.GetName(), strings.Join(used, ", "))
		} else if len(used) > 0 {
			m.Referrers(row, true)
		}
	}
	if _, err := m.DB.DeleteProfile(ctx, row.GetId()); err != nil {
		return nil, err
	}
	delete(m.rows, row.GetId())
	m.publish(v1.EventAction_EVENT_ACTION_DELETED, row)
	return clone(row), nil
}

func (m *Manager) publish(action v1.EventAction, p *v1.Profile) {
	if m.Events != nil {
		m.Events.Publish(v1.EventKind_EVENT_KIND_PROFILE, action, p.GetId(), clone(p))
	}
}

// Keeps the params that carry a value, trimmed
func trim(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if k, v = strings.TrimSpace(k), strings.TrimSpace(v); k != "" && v != "" {
			out[k] = v
		}
	}
	return out
}

func clone(p *v1.Profile) *v1.Profile { return proto.Clone(p).(*v1.Profile) }

func newID(runtimeID string) string {
	var b [4]byte
	rand.Read(b[:])
	return runtimeID + "-" + hex.EncodeToString(b[:])
}
