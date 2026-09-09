package archs

import (
	"github.com/nickheyer/nebu/pkg/formats"
)

// Standard attention with a full key and value cache per layer, on one layer in every
// attention interval for a hybrid model whose other layers keep a fixed recurrent state
type Default struct{}

func (Default) ID() string { return "default" }
func (Default) Description() string {
	return "Standard attention with a full key and value cache per layer, on one layer in attn_interval for a hybrid model"
}
func (Default) Priority() int       { return 0 }
func (Default) Matches(string) bool { return true }

func (Default) CachePerToken(p formats.Params, _ Run) (float64, error) {
	if needs := missing(p.Layers, "n_layer", p.HeadsKV, "n_head_kv", p.HeadDim, "head_dim"); needs != nil {
		return 0, needs
	}
	interval := p.AttentionInterval
	if interval <= 0 {
		interval = 1
	}
	return p.Layers / interval * p.HeadsKV * (p.HeadDim + p.HeadDimV), nil
}
