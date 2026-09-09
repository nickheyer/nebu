package archs

import "github.com/nickheyer/nebu/pkg/formats"

// Llama 4, every fourth layer keeps the full cache, the rest chunked attention over a window
type Llama4 struct{}

func (Llama4) ID() string { return "llama4" }
func (Llama4) Description() string {
	return "Llama 4, every fourth layer keeps the full cache, the rest chunked attention"
}
func (Llama4) Priority() int                    { return 5 }
func (Llama4) Matches(architecture string) bool { return is(architecture, "llama4") }
func (Llama4) CachePerToken(p formats.Params, run Run) (float64, error) {
	return slidingWindow(p, run, 4)
}
