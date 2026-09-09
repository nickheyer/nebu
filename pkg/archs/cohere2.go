package archs

import "github.com/nickheyer/nebu/pkg/formats"

// Command R7B, every fourth layer keeps the full cache, the rest a sliding window
type Cohere2 struct{}

func (Cohere2) ID() string { return "cohere2" }
func (Cohere2) Description() string {
	return "Command R7B, every fourth layer keeps the full cache, the rest a sliding window"
}
func (Cohere2) Priority() int                    { return 5 }
func (Cohere2) Matches(architecture string) bool { return is(architecture, "cohere2") }
func (Cohere2) CachePerToken(p formats.Params, run Run) (float64, error) {
	return slidingWindow(p, run, 4)
}
