package scorer

import (
	"time"

	"github.com/nickheyer/nebu/pkg/prefixcache"
	"github.com/nickheyer/nebu/pkg/scheduling/framework"
	"github.com/nickheyer/nebu/pkg/scheduling/plugins"
	"github.com/nickheyer/nebu/pkg/scheduling/registry"
)

// Registers every scorer with its parameter and dependency validation
func init() {
	registry.Register(QueueScorerType, plugins.Plain(NewQueueScorer))
	registry.Register(KVCacheUtilizationScorerType, plugins.Plain(NewKVCacheUtilizationScorer))
	registry.Register(LoRAAffinityScorerType, plugins.Plain(NewLoRAAffinityScorer))
	registry.Register(ThroughputScorerType, plugins.Plain(NewThroughputScorer))
	registry.Register(AssumedLoadScorerType, plugins.Plain(NewAssumedLoadScorer))
	registry.Register(ExpertParallelScorerType, plugins.Plain(NewExpertParallelScorer))
	registry.Register(PrefixCacheScorerType, func(name string, params map[string]any, deps registry.Deps) (framework.Plugin, error) {
		p := struct {
			RingReplicas   int               `json:"ringReplicas"`
			RingLoadFactor float64           `json:"ringLoadFactor"`
			RequestTTL     registry.Duration `json:"requestTTL"`
		}{RingReplicas: prefixcache.DefaultReplicas, RingLoadFactor: DefaultRingLoadFactor, RequestTTL: registry.Duration(10 * time.Minute)}
		if err := registry.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.RingReplicas < 1 {
			return nil, registry.Invalid("ringReplicas must be at least 1")
		}
		if p.RingLoadFactor <= 1 {
			return nil, registry.Invalid("ringLoadFactor must be above 1")
		}
		if p.RequestTTL <= 0 {
			return nil, registry.Invalid("requestTTL must be a positive duration")
		}
		if deps.Indexer == nil {
			return nil, registry.Missing("Indexer")
		}
		s := NewPrefixCacheScorer(deps.Indexer, prefixcache.NewHashRing(p.RingReplicas), p.RingLoadFactor, deps.Clock, time.Duration(p.RequestTTL))
		s.SetName(name)
		return s, nil
	})
	registry.Register(LatencyScorerType, func(name string, params map[string]any, deps registry.Deps) (framework.Plugin, error) {
		p := struct {
			RequestTTL registry.Duration `json:"requestTTL"`
		}{RequestTTL: registry.Duration(10 * time.Minute)}
		if err := registry.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.RequestTTL <= 0 {
			return nil, registry.Invalid("requestTTL must be a positive duration")
		}
		if deps.Predictor == nil {
			return nil, registry.Missing("Predictor")
		}
		if deps.Indexer == nil {
			return nil, registry.Missing("Indexer")
		}
		s := NewLatencyScorer(deps.Predictor, deps.Indexer, deps.Clock, time.Duration(p.RequestTTL))
		s.SetName(name)
		return s, nil
	})
	registry.Register(SessionAffinityScorerType, func(name string, params map[string]any, deps registry.Deps) (framework.Plugin, error) {
		p := struct {
			Header      string            `json:"header"`
			Window      registry.Duration `json:"window"`
			MaxSessions int               `json:"maxSessions"`
		}{Window: registry.Duration(10 * time.Minute), MaxSessions: 100_000}
		if err := registry.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.Header == "" {
			return nil, registry.Invalid("header must name the session header")
		}
		if p.Window <= 0 {
			return nil, registry.Invalid("window must be a positive duration")
		}
		if p.MaxSessions < 1 {
			return nil, registry.Invalid("maxSessions must be at least 1")
		}
		s := NewSessionAffinityScorer(p.Header, time.Duration(p.Window), p.MaxSessions, deps.Clock)
		s.SetName(name)
		return s, nil
	})
}
