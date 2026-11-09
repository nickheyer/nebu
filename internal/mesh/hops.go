package mesh

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const (
	// The header a forwarded request carries the gateways it crossed in
	HopHeader = "Nebu-Hops"
	// Gateways a request may cross before the next one refuses it
	MaxHops = 3
)

// Returned when a request already crossed the most gateways allowed
var ErrHopLimit = errors.New("the request crossed the most gateways allowed")

// Returns the hop count to forward with, refusing a malformed count or one at the limit
func NextHop(h http.Header) (int, error) {
	values := h.Values(HopHeader)
	if len(values) > 1 {
		return 0, fmt.Errorf("%w: %s appears %d times", ErrMesh, HopHeader, len(values))
	}
	hops := 0
	if len(values) == 1 {
		n, err := strconv.Atoi(strings.TrimSpace(values[0]))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%w: %s is %q, not a count", ErrMesh, HopHeader, values[0])
		}
		hops = n
	}
	if hops >= MaxHops {
		return 0, fmt.Errorf("%w: %d of %d", ErrHopLimit, hops, MaxHops)
	}
	return hops + 1, nil
}
