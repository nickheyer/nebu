package archs

import "github.com/nickheyer/nebu/pkg/formats"

// Phi 3, every layer keeps a sliding window
type Phi3 struct{}

func (Phi3) ID() string                       { return "phi3" }
func (Phi3) Description() string              { return "Phi 3, every layer keeps a sliding window" }
func (Phi3) Priority() int                    { return 5 }
func (Phi3) Matches(architecture string) bool { return is(architecture, "phi3") }
func (Phi3) CachePerToken(p formats.Params, run Run) (float64, error) {
	return slidingWindow(p, run, 0)
}
