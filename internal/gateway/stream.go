package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/pkg/bodyparse"
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

// The body asking for the usage of its stream, whatever else the client set of its stream options kept beside it
func withUsage(doc bodyparse.Document) (bodyparse.Document, error) {
	opts := map[string]json.RawMessage{}
	if raw, ok := doc.Field("stream_options"); ok && json.Unmarshal(raw, &opts) != nil || opts == nil {
		opts = map[string]json.RawMessage{}
	}
	opts["include_usage"] = json.RawMessage("true")
	raw, err := json.Marshal(opts)
	if err != nil {
		return nil, err
	}
	return doc.WithField("stream_options", raw)
}

// The body without one top level field, every other member as the client wrote it and in its order
func withoutField(doc bodyparse.Document, name string) (bodyparse.Document, error) {
	dec := json.NewDecoder(bytes.NewReader(doc.Bytes()))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, bodyparse.ErrNotJSON
	}
	out := []byte{'{'}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		if key == name {
			continue
		}
		if len(out) > 1 {
			out = append(out, ',')
		}
		quoted, _ := json.Marshal(key)
		out = append(append(append(out, quoted...), ':'), value...)
	}
	return bodyparse.Parse(doc.Kind(), append(out, '}'))
}

// Passes a stream on event by event without the chunk of usage alone a provider ends it with when asked, which a
// client that did not ask would not expect, an event held back until it is whole
type usageDrop struct {
	held []byte
}

// What of the stream so far may go to the client now that b arrived
func (d *usageDrop) pass(b []byte) []byte {
	d.held = append(d.held, b...)
	var out []byte
	for {
		end := eventEnd(d.held)
		if end < 0 {
			return out
		}
		if !usageAlone(d.held[:end]) {
			out = append(out, d.held[:end]...)
		}
		d.held = d.held[end:]
	}
}

// Where the first event of a stream ends, past its blank line, negative while it is not whole
func eventEnd(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] != '\n' {
			continue
		}
		if b[i+1] == '\n' {
			return i + 2
		}
		if b[i+1] == '\r' && i+2 < len(b) && b[i+2] == '\n' {
			return i + 3
		}
	}
	return -1
}

// Whether an event is the chunk that carries a stream's usage and no choice, as stream_options.include_usage adds one before the end
func usageAlone(event []byte) bool {
	data, ok := bytes.CutPrefix(bytes.TrimSpace(event), []byte("data:"))
	if !ok {
		return false
	}
	var chunk struct {
		Choices *[]json.RawMessage `json:"choices"`
		Usage   json.RawMessage    `json:"usage"`
	}
	if json.Unmarshal(data, &chunk) != nil {
		return false
	}
	return chunk.Choices != nil && len(*chunk.Choices) == 0 && len(chunk.Usage) > 0 && string(chunk.Usage) != "null"
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
