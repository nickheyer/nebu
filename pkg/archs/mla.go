package archs

import (
	"strings"

	"github.com/nickheyer/nebu/pkg/formats"
)

// Multi head latent attention, which stores one compressed latent plus the rope part per layer
type MLA struct{}

func (MLA) ID() string { return "mla" }
func (MLA) Description() string {
	return "Multi head latent attention storing a compressed cache per layer"
}
func (MLA) Priority() int { return 10 }
func (MLA) Matches(architecture string) bool {
	return strings.HasPrefix(strings.ToLower(architecture), "deepseek")
}

func (MLA) CachePerToken(p formats.Params, _ Run) (float64, error) {
	if needs := missing(p.Layers, "n_layer", p.KVLoraRank, "kv_lora_rank", p.RopeDim, "rope_dim"); needs != nil {
		return 0, needs
	}
	return p.Layers * (p.KVLoraRank + p.RopeDim), nil
}
