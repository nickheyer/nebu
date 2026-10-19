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
	said   textGather
	tools  toolGather
}

func (p *whole) Write(ev Event) error {
	switch ev.Kind {
	case Said, Thought:
		p.said.add(ev)
	case Called:
		p.tools.add(ev)
	case Failed:
		p.client.Error(p.w, *ev.Failure)
	case Stopped:
		res := ev.Res
		if res == nil {
			res = &Result{}
		}
		out := *res
		out.Model, out.Text, out.Thinking, out.ToolCalls = p.model, p.said.text.String(), p.said.thinking.String(), p.tools.list()
		out.Stop = StopOf(res.Stop, len(out.ToolCalls))
		data, err := p.render(p.req, &out)
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
