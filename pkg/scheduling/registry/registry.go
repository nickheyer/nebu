// Package registry maps plugin type names to the factories that build them from configuration
package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/latency"
	"github.com/nickheyer/nebu/pkg/prefixcache"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
)

var (
	// Returned when no factory is registered under a type
	ErrUnknownType = errors.New("unknown plugin type")
	// Returned when parameters name an unknown field or carry an invalid value
	ErrInvalidParams = errors.New("invalid plugin parameters")
	// Returned when a factory needs a dependency the caller left empty
	ErrMissingDependency = errors.New("missing plugin dependency")
)

// Reads the current time
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// The wall clock
var SystemClock Clock = systemClock{}

// What factories may build plugins with
type Deps struct {
	Store     datalayer.Store
	Indexer   prefixcache.Indexer
	Predictor latency.Predictor
	Logger    *slog.Logger
	Clock     Clock
	// Receives every profile run's filter drops and scores, nil observing nothing
	Observer framework.Observer
}

// Builds one plugin instance from its configured name and parameters
type Factory func(name string, params map[string]any, deps Deps) (framework.Plugin, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Registers a factory under a plugin type, panicking on a second registration of the same type
func Register(typ string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[typ]; dup {
		panic(fmt.Sprintf("plugin type %q registered twice", typ))
	}
	factories[typ] = f
}

// Every registered plugin type in sorted order
func Types() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for t := range factories {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Builds a plugin of a registered type, naming it after its type when name is empty
func New(typ, name string, params map[string]any, deps Deps) (framework.Plugin, error) {
	mu.RLock()
	f, ok := factories[typ]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w %q, known types are %v", ErrUnknownType, typ, Types())
	}
	if name == "" {
		name = typ
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Clock == nil {
		deps.Clock = SystemClock
	}
	p, err := f(name, params, deps)
	if err != nil {
		return nil, fmt.Errorf("plugin %s of type %s: %w", name, typ, err)
	}
	return p, nil
}

// Decodes parameters into a struct by its JSON tags, refusing any name the struct does not declare
func Decode(params map[string]any, into any) error {
	if len(params) == 0 {
		return nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidParams, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidParams, err)
	}
	return nil
}

// Wraps a parameter value error in ErrInvalidParams
func Invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidParams, fmt.Sprintf(format, args...))
}

// Wraps the name of an absent dependency in ErrMissingDependency
func Missing(dep string) error {
	return fmt.Errorf("%w: %s", ErrMissingDependency, dep)
}

// A duration parameter written as a Go duration string such as 2s or 150ms
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string such as 2s, got %s", b)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}
