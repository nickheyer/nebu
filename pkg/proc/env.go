package proc

import (
	"os"
	"strings"
	"sync"
)

// Prefix of the daemon's own settings, which no child receives
const ownPrefix = "NEBU_"

var (
	hiddenMu sync.Mutex
	hidden   = map[string]bool{}
)

// Keeps the named variables out of every child's environment
func Hide(names ...string) {
	hiddenMu.Lock()
	defer hiddenMu.Unlock()
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			hidden[strings.ToUpper(n)] = true
		}
	}
}

// The environment children start with, the daemon's settings and hidden secrets left out
func Environ() []string {
	hiddenMu.Lock()
	defer hiddenMu.Unlock()
	all := os.Environ()
	out := make([]string, 0, len(all))
	for _, kv := range all {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, ownPrefix) || hidden[upper] {
			continue
		}
		out = append(out, kv)
	}
	return out
}
