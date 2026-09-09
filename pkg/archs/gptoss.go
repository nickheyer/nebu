package archs

import (
	"strings"

	"github.com/nickheyer/nebu/pkg/formats"
)

// GPT-OSS, every second layer keeps the full cache, the rest a sliding window
type GPTOSS struct{}

func (GPTOSS) ID() string { return "gptoss" }
func (GPTOSS) Description() string {
	return "GPT-OSS, every second layer keeps the full cache, the rest a sliding window"
}
func (GPTOSS) Priority() int { return 5 }
func (GPTOSS) Matches(architecture string) bool {
	a := strings.ToLower(architecture)
	return a == "gptoss" || a == "gpt-oss" || a == "gpt_oss"
}
func (GPTOSS) CachePerToken(p formats.Params, run Run) (float64, error) {
	return slidingWindow(p, run, 2)
}
