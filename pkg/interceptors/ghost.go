package interceptors

import "strings"

// The header that asks the gateway to keep nothing of a request, 1 or true turning it on
const GhostHeader = "X-Nebu-Ghost"

// Keeps no trace, event, or seat pin of a request whose header asks for ghost mode
type Ghost struct{}

func (Ghost) Stage() Stage { return Inbound }

func (Ghost) Guard() bool { return false }

func (Ghost) Intercept(x *Exchange) (bool, error) {
	on, err := switched(GhostHeader, x.Header.Get(GhostHeader))
	if !on || err != nil {
		return false, err
	}
	x.Ghost = true
	return true, nil
}

// Whether a mode header turns its mode on, 1 or true on, 0, false or unset off, anything else refused
func switched(name, value string) (bool, error) {
	switch {
	case value == "" || value == "0" || strings.EqualFold(value, "false"):
		return false, nil
	case value == "1" || strings.EqualFold(value, "true"):
		return true, nil
	}
	return false, bad("%s is 1, true, 0, false, or unset, not %q", name, value)
}
