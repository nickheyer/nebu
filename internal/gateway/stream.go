package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Silence on a proxied stream before a keepalive goes out
const keepaliveEvery = 15 * time.Second

// Serializes writes of a proxied answer, flushing streams and sending the protocol's keepalive at event boundaries
type pulse struct {
	mu    sync.Mutex
	w     *writer
	flush bool
	// The keepalive written after silence, nil for answers that are not streams
	ping []byte
	// What the bytes written so far must end with for a keepalive to fit between events
	boundary []byte
	tail     []byte
	last     time.Time
	err      error
	quit     chan struct{}
	done     chan struct{}
}

// A writer for an answer, pinging SSE with a comment or Anthropic ping and NDJSON with an empty chunk
func (x *exchange) pulse(h http.Header, creq *chat.Request) *pulse {
	sse, ndjson := streamed(h.Get("Content-Type"))
	p := &pulse{w: x.w, flush: sse || ndjson, last: time.Now(), quit: make(chan struct{}), done: make(chan struct{})}
	switch {
	case sse && x.api == v1.ApiFlavor_API_FLAVOR_ANTHROPIC:
		p.ping, p.boundary = []byte("event: ping\ndata: {\"type\": \"ping\"}\n\n"), []byte("\n\n")
	case sse:
		p.ping, p.boundary = []byte(": keepalive\n\n"), []byte("\n\n")
	case ndjson:
		p.ping, p.boundary = ollamaPing(x.model, creq != nil && creq.Kind == chat.Generate || x.r.URL.Path == chat.OllamaGenerate), []byte("\n")
	}
	if p.ping == nil {
		close(p.done)
		return p
	}
	go p.run()
	return p
}

// An Ollama chunk with empty content that is not done, which every Ollama client reads as nothing said yet
func ollamaPing(model string, generate bool) []byte {
	chunk := map[string]any{"model": model, "created_at": time.Now().UTC().Format(time.RFC3339Nano), "done": false}
	if generate {
		chunk["response"] = ""
	} else {
		chunk["message"] = map[string]string{"role": "assistant", "content": ""}
	}
	b, _ := json.Marshal(chunk)
	return append(b, '\n')
}

// Wakes when the silence would reach the keepalive interval, pinging only at an event boundary
func (p *pulse) run() {
	defer close(p.done)
	timer := time.NewTimer(keepaliveEvery)
	defer timer.Stop()
	for {
		select {
		case <-p.quit:
			return
		case <-timer.C:
			p.mu.Lock()
			wait := keepaliveEvery - time.Since(p.last)
			if wait <= 0 && p.err == nil && bytes.HasSuffix(p.tail, p.boundary) {
				p.err = p.put(p.ping)
				wait = keepaliveEvery
			}
			failed := p.err != nil
			p.mu.Unlock()
			if failed {
				return
			}
			timer.Reset(max(wait, time.Second))
		}
	}
}

// Writes one chunk of the answer
func (p *pulse) write(b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.err = p.put(b)
	return p.err
}

// Writes under the lock, flushing streams and keeping the last bytes for the boundary check
func (p *pulse) put(b []byte) error {
	if _, err := p.w.Write(b); err != nil {
		return err
	}
	if p.flush {
		p.w.Flush()
	}
	p.last = time.Now()
	if len(b) >= 2 {
		p.tail = append(p.tail[:0], b[len(b)-2:]...)
	} else {
		p.tail = append(p.tail, b...)
		if len(p.tail) > 2 {
			p.tail = p.tail[len(p.tail)-2:]
		}
	}
	return nil
}

// Writes a last event after ending the event the answer so far left open, if any
func (p *pulse) end(event []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	have := 0
	for have < len(p.boundary) && have < len(p.tail) && p.tail[len(p.tail)-1-have] == '\n' {
		have++
	}
	b := make([]byte, 0, len(p.boundary)+len(event))
	if len(p.tail) > 0 {
		b = append(b, p.boundary[have:]...)
	}
	p.err = p.put(append(b, event...))
	return p.err
}

// The number the next event of a Responses stream takes, following the highest one the relayed bytes carried
type sequence struct {
	on   bool
	next int
	// The last bytes seen, so a number split between two writes is read whole
	carry []byte
}

// Bytes kept between writes, longer than the key, its separator and any event number
const sequenceCarry = 64

var sequenceKey = []byte(`"sequence_number"`)

func newSequence(on bool) *sequence { return &sequence{on: on} }

// Raises the next number past every sequence number the bytes carry
func (s *sequence) see(b []byte) {
	if !s.on || len(b) == 0 {
		return
	}
	text := append(s.carry, b...)
	for rest := text; ; {
		i := bytes.Index(rest, sequenceKey)
		if i < 0 {
			break
		}
		rest = rest[i+len(sequenceKey):]
		v := bytes.TrimLeft(rest, " \t")
		if len(v) == 0 || v[0] != ':' {
			continue
		}
		v = bytes.TrimLeft(v[1:], " \t")
		n, digits := 0, 0
		for digits < len(v) && v[digits] >= '0' && v[digits] <= '9' {
			n = n*10 + int(v[digits]-'0')
			digits++
		}
		if digits > 0 {
			s.next = max(s.next, n+1)
		}
	}
	s.carry = append(s.carry[:0], text[max(0, len(text)-sequenceCarry):]...)
}

// Stops keepalives and waits for one in progress
func (p *pulse) stop() {
	select {
	case <-p.quit:
	default:
		close(p.quit)
	}
	<-p.done
}
