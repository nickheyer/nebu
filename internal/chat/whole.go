package chat

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"
)

// Gathers an answer's events and writes it whole at stop, in the protocol's shape
type whole struct {
	w      http.ResponseWriter
	req    *Request
	model  string
	client Client
	render func(req *Request, res *Result) ([]byte, error)
	// Whether the protocol writes several choices, events for a later choice being refused otherwise
	choices  bool
	gathered answerGather
}

func (p *whole) Write(ev Event) error {
	if !p.choices {
		if err := oneChoice(ev, "client's"); err != nil {
			return err
		}
	}
	if p.gathered == nil {
		p.gathered = answerGather{}
	}
	switch ev.Kind {
	case Said, Thought, Called:
		p.gathered.add(ev)
	case Failed:
		p.client.Error(p.w, *ev.Failure)
	case Stopped:
		data, err := p.render(p.req, p.gathered.result(ev.Res, p.model))
		if err != nil {
			return err
		}
		p.w.Header().Set("Content-Type", "application/json")
		p.w.WriteHeader(http.StatusOK)
		_, err = p.w.Write(data)
		return err
	}
	return nil
}

func (*whole) Close() error { return nil }

// Twelve random hex characters, the clock when randomness fails
func randomID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%012x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
