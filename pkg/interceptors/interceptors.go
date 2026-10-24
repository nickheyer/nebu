// Package interceptors changes gateway requests at set stages, one interceptor to a file
package interceptors

import (
	"fmt"
	"net/http"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Where on a request's way through the gateway an interceptor runs
type Stage int

const (
	// Before the gateway traces or routes the request, with the client's headers alone
	Inbound Stage = iota
	// On the parsed request before the gateway renders it for the runtime
	Request
)

// One request as interceptors read and change it
type Exchange struct {
	// The client's headers
	Header http.Header
	// The parsed request, set for the request stage
	Req *chat.Request
	// How the route handles system turns after the first
	SystemMode v1.SystemMessages
	// Whether the gateway keeps no trace, publishes no event, and pins no seat for the request
	Ghost bool
	// Whether the gateway attaches no tool server of the route to the request
	Bare bool
	// Whether the client's system prompt stands as sent, turning guards off for the rest of the request
	Pinned bool
}

// Changes requests at one stage
type Interceptor interface {
	Stage() Stage
	// Whether it guards, filters, or replaces on the gateway's behalf, which a pinned system prompt turns off
	Guard() bool
	// Changes the exchange, answering whether it did, an error refusing the request
	Intercept(x *Exchange) (bool, error)
}

// Interceptors in the order they run
type Chain []Interceptor

// The gateway's interceptors, the system prompt leading the request stage so its pin turns off the guards after it
var Default = Chain{Ghost{}, Bare{}, System{}, Fold{}}

// Runs the stage's interceptors in order, answering whether any changed the exchange
func (c Chain) Run(stage Stage, x *Exchange) (bool, error) {
	changed := false
	for _, i := range c {
		if i.Stage() != stage || x.Pinned && i.Guard() {
			continue
		}
		did, err := i.Intercept(x)
		if err != nil {
			return false, err
		}
		changed = changed || did
	}
	return changed, nil
}

// A refusal of the request as the client wrote it
func bad(format string, args ...any) error {
	return fmt.Errorf("bad request: "+format, args...)
}
