package interceptors

import "strings"

// The header that asks the gateway to keep nothing of a request, 1 or true turning it on
const GhostHeader = "X-Nebu-Ghost"

// Keeps no trace, event, or seat pin of a request whose header asks for ghost mode
type Ghost struct{}

func (Ghost) Stage() Stage { return Inbound }

func (Ghost) Guard() bool { return false }

func (Ghost) Intercept(x *Exchange) (bool, error) {
	switch v := x.Header.Get(GhostHeader); {
	case v == "" || v == "0" || strings.EqualFold(v, "false"):
		return false, nil
	case v == "1" || strings.EqualFold(v, "true"):
		x.Ghost = true
		return true, nil
	default:
		return false, bad("%s is 1, true, 0, false, or unset, not %q", GhostHeader, v)
	}
}
