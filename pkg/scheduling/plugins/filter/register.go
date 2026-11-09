package filter

import (
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// Registers every filter with its parameter validation
func init() {
	registry.Register(ReadyFilterType, plugins.Plain(NewReadyFilter))
	registry.Register(FreshMetricsFilterType, func(name string, params map[string]any, deps registry.Deps) (framework.Plugin, error) {
		var p struct {
			Threshold *registry.Duration `json:"threshold"`
		}
		if err := registry.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.Threshold == nil || *p.Threshold <= 0 {
			return nil, registry.Invalid("threshold must be a positive duration")
		}
		f := NewFreshMetricsFilter(time.Duration(*p.Threshold), deps.Clock)
		f.SetName(name)
		return f, nil
	})
	registry.Register(SubsetFilterType, plugins.Plain(NewSubsetFilter))
	registry.Register(RoleFilterType, func(name string, params map[string]any, _ registry.Deps) (framework.Plugin, error) {
		var p struct {
			Role string `json:"role"`
		}
		if err := registry.Decode(params, &p); err != nil {
			return nil, err
		}
		switch p.Role {
		case datalayer.RolePrefill, datalayer.RoleDecode, datalayer.RoleBoth:
		default:
			return nil, registry.Invalid("role must be %s, %s or %s, got %q", datalayer.RolePrefill, datalayer.RoleDecode, datalayer.RoleBoth, p.Role)
		}
		f := NewRoleFilter(p.Role)
		f.SetName(name)
		return f, nil
	})
	registry.Register(KVThresholdFilterType, func(name string, params map[string]any, _ registry.Deps) (framework.Plugin, error) {
		var p struct {
			Max *float64 `json:"max"`
		}
		if err := registry.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.Max == nil || *p.Max < 0 || *p.Max > 1 {
			return nil, registry.Invalid("max must be a utilization between 0 and 1")
		}
		f := NewKVThresholdFilter(*p.Max)
		f.SetName(name)
		return f, nil
	})
	registry.Register(QueueThresholdFilterType, func(name string, params map[string]any, _ registry.Deps) (framework.Plugin, error) {
		var p struct {
			Max *int `json:"max"`
		}
		if err := registry.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.Max == nil || *p.Max < 0 {
			return nil, registry.Invalid("max must be a queue length of zero or more")
		}
		f := NewQueueThresholdFilter(*p.Max)
		f.SetName(name)
		return f, nil
	})
	registry.Register(LoRAAffinityFilterType, plugins.Plain(NewLoRAAffinityFilter))
	registry.Register(CapacityFilterType, plugins.Plain(NewCapacityFilter))
	registry.Register(ObjectiveFilterType, func(name string, params map[string]any, deps registry.Deps) (framework.Plugin, error) {
		if err := registry.Decode(params, &struct{}{}); err != nil {
			return nil, err
		}
		if deps.Predictor == nil {
			return nil, registry.Missing("Predictor")
		}
		if deps.Indexer == nil {
			return nil, registry.Missing("Indexer")
		}
		f := NewObjectiveFilter(deps.Predictor, deps.Indexer)
		f.SetName(name)
		return f, nil
	})
	registry.Register(LeastQueueFilterType, func(name string, params map[string]any, _ registry.Deps) (framework.Plugin, error) {
		var p struct {
			Margin int `json:"margin"`
		}
		if err := registry.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.Margin < 0 {
			return nil, registry.Invalid("margin must be zero or more")
		}
		f := NewLeastQueueFilter(p.Margin)
		f.SetName(name)
		return f, nil
	})
}
