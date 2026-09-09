package archs

import "github.com/nickheyer/nebu/pkg/formats"

// Gemma 3, every sixth layer keeps the full cache, the rest a sliding window
type Gemma3 struct{}

func (Gemma3) ID() string { return "gemma3" }
func (Gemma3) Description() string {
	return "Gemma 3, every sixth layer keeps the full cache, the rest a sliding window"
}
func (Gemma3) Priority() int                    { return 5 }
func (Gemma3) Matches(architecture string) bool { return is(architecture, "gemma3") }
func (Gemma3) CachePerToken(p formats.Params, run Run) (float64, error) {
	return slidingWindow(p, run, 6)
}
