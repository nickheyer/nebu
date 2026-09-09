package archs

import "github.com/nickheyer/nebu/pkg/formats"

// Gemma 3n, every fifth layer keeps the full cache, the rest a sliding window
type Gemma3n struct{}

func (Gemma3n) ID() string { return "gemma3n" }
func (Gemma3n) Description() string {
	return "Gemma 3n, every fifth layer keeps the full cache, the rest a sliding window"
}
func (Gemma3n) Priority() int                    { return 5 }
func (Gemma3n) Matches(architecture string) bool { return is(architecture, "gemma3n") }
func (Gemma3n) CachePerToken(p formats.Params, run Run) (float64, error) {
	return slidingWindow(p, run, 5)
}
