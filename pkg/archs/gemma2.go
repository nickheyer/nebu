package archs

import "github.com/nickheyer/nebu/pkg/formats"

// Gemma 2, every second layer keeps the full cache, the rest a sliding window
type Gemma2 struct{}

func (Gemma2) ID() string { return "gemma2" }
func (Gemma2) Description() string {
	return "Gemma 2, every second layer keeps the full cache, the rest a sliding window"
}
func (Gemma2) Priority() int                    { return 5 }
func (Gemma2) Matches(architecture string) bool { return is(architecture, "gemma2") }
func (Gemma2) CachePerToken(p formats.Params, run Run) (float64, error) {
	return slidingWindow(p, run, 2)
}
