package archs

import (
	"github.com/nickheyer/nebu/pkg/formats"
)

// Full KV cache on each attention layer. Hybrid models use the configured attention interval.
type Default struct{}

func (Default) ID() string { return "default" }
func (Default) Description() string {
	return "Standard attention with a full key and value cache per layer, on one layer in attn_interval for a hybrid model, or on the layers a per layer KV head count gives heads"
}
func (Default) Priority() int       { return 0 }
func (Default) Matches(string) bool { return true }

// Full cache per layer, sized by each layer's KV heads when listed, else one layer per attention interval
func (Default) CacheLayers(p formats.Params, _ Run) ([]float64, error) {
	if needs := missing(p.Layers, "n_layer", p.HeadsKV, "n_head_kv", p.HeadDim, "head_dim"); needs != nil {
		return nil, needs
	}
	interval := int(p.AttentionInterval)
	if interval <= 0 {
		interval = 1
	}
	dims := p.HeadDim + p.HeadDimV
	return eachLayer(p, func(il int) float64 {
		if len(p.HeadsKVLayers) > 0 {
			return kvHeads(p, il) * dims
		}
		if (il+1)%interval == 0 {
			return p.HeadsKV * dims
		}
		return 0
	}), nil
}
