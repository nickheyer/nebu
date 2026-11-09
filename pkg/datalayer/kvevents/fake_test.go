package kvevents

import (
	"bufio"
	"encoding/binary"
	"io"
	"math"
	"net"
	"sync"
	"testing"
)

// A msgpack writer for test payloads
type writer struct{ b []byte }

func (w *writer) array(n int) *writer {
	if n < 16 {
		w.b = append(w.b, 0x90|byte(n))
	} else {
		w.b = append(w.b, 0xdc)
		w.b = binary.BigEndian.AppendUint16(w.b, uint16(n))
	}
	return w
}

func (w *writer) mapN(n int) *writer {
	if n < 16 {
		w.b = append(w.b, 0x80|byte(n))
	} else {
		w.b = append(w.b, 0xde)
		w.b = binary.BigEndian.AppendUint16(w.b, uint16(n))
	}
	return w
}

func (w *writer) str(s string) *writer {
	if len(s) < 32 {
		w.b = append(w.b, 0xa0|byte(len(s)))
	} else {
		w.b = append(w.b, 0xd9, byte(len(s)))
	}
	w.b = append(w.b, s...)
	return w
}

func (w *writer) bin(b []byte) *writer {
	w.b = append(w.b, 0xc4, byte(len(b)))
	w.b = append(w.b, b...)
	return w
}

func (w *writer) nilV() *writer { w.b = append(w.b, 0xc0); return w }

func (w *writer) boolV(v bool) *writer {
	if v {
		w.b = append(w.b, 0xc3)
	} else {
		w.b = append(w.b, 0xc2)
	}
	return w
}

func (w *writer) int(v int64) *writer {
	switch {
	case v >= 0 && v < 128:
		w.b = append(w.b, byte(v))
	case v < 0 && v >= -32:
		w.b = append(w.b, byte(v))
	case v >= 0 && v <= math.MaxUint32:
		w.b = append(w.b, 0xce)
		w.b = binary.BigEndian.AppendUint32(w.b, uint32(v))
	default:
		w.b = append(w.b, 0xd3)
		w.b = binary.BigEndian.AppendUint64(w.b, uint64(v))
	}
	return w
}

func (w *writer) float(v float64) *writer {
	w.b = append(w.b, 0xcb)
	w.b = binary.BigEndian.AppendUint64(w.b, math.Float64bits(v))
	return w
}

// One event as a test describes it
type event struct {
	kind     string
	hashes   []any
	parent   any
	tokens   []int
	size     int
	lora     any
	loraName any
	extra    map[string]any
}

// Encodes a batch as the runtimes do, an array of timestamp, tagged event maps and rank
func encodeBatch(events ...event) []byte {
	w := &writer{}
	w.array(3).float(1700000000.5).array(len(events))
	for _, e := range events {
		fields := map[string]any{"type": e.kind}
		switch e.kind {
		case "BlockStored":
			fields["block_hashes"] = e.hashes
			fields["parent_block_hash"] = e.parent
			fields["token_ids"] = e.tokens
			fields["block_size"] = e.size
			fields["lora_id"] = e.lora
			fields["medium"] = nil
			if e.loraName != nil {
				fields["lora_name"] = e.loraName
			}
		case "BlockRemoved":
			fields["block_hashes"] = e.hashes
			fields["medium"] = nil
		}
		for k, v := range e.extra {
			fields[k] = v
		}
		w.mapN(len(fields))
		for k, v := range fields {
			w.str(k)
			w.value(v)
		}
	}
	w.int(0)
	return w.b
}

func (w *writer) value(v any) {
	switch x := v.(type) {
	case nil:
		w.nilV()
	case string:
		w.str(x)
	case []byte:
		w.bin(x)
	case int:
		w.int(int64(x))
	case int64:
		w.int(x)
	case bool:
		w.boolV(x)
	case float64:
		w.float(x)
	case []int:
		w.array(len(x))
		for _, i := range x {
			w.int(int64(i))
		}
	case []any:
		w.array(len(x))
		for _, i := range x {
			w.value(i)
		}
	case map[string]any:
		w.mapN(len(x))
		for k, i := range x {
			w.str(k)
			w.value(i)
		}
	default:
		panic("unencodable test value")
	}
}

// A publisher speaking ZMTP as libzmq does, with a replay router over the batches it keeps
type fakePub struct {
	t       *testing.T
	ln      net.Listener
	replay  net.Listener
	minor   byte
	mu      sync.Mutex
	seq     uint64
	buffer  []record
	subs    []net.Conn
	topics  []string
	handoff chan struct{}
}

type record struct {
	seq     uint64
	payload []byte
}

func newFakePub(t *testing.T, minor byte) *fakePub {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePub{t: t, ln: ln, replay: rl, minor: minor, handoff: make(chan struct{}, 64)}
	t.Cleanup(func() { ln.Close(); rl.Close() })
	go p.acceptSubs()
	go p.acceptReplays()
	return p
}

func (p *fakePub) events() string  { return p.ln.Addr().String() }
func (p *fakePub) replays() string { return p.replay.Addr().String() }

// Serves the PUB side of each subscriber connection
func (p *fakePub) acceptSubs() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			r := bufio.NewReader(c)
			topic, ok := p.handshake(c, r, "PUB")
			if !ok {
				c.Close()
				return
			}
			p.mu.Lock()
			p.subs = append(p.subs, c)
			p.topics = append(p.topics, topic)
			p.mu.Unlock()
			select {
			case p.handoff <- struct{}{}:
			default:
			}
			_, _ = io.Copy(io.Discard, r)
		}()
	}
}

// Serves the ROUTER side of each replay connection
func (p *fakePub) acceptReplays() {
	for {
		c, err := p.replay.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			r := bufio.NewReader(c)
			if _, ok := p.handshake(c, r, "ROUTER"); !ok {
				return
			}
			frames, err := readMessage(r)
			if err != nil || len(frames) != 2 || len(frames[1]) != 8 {
				return
			}
			start := binary.BigEndian.Uint64(frames[1])
			p.mu.Lock()
			buffered := append([]record(nil), p.buffer...)
			p.mu.Unlock()
			for _, rec := range buffered {
				if rec.seq < start {
					continue
				}
				var seq [8]byte
				binary.BigEndian.PutUint64(seq[:], rec.seq)
				if err := writeMessage(c, [][]byte{{}, []byte(kvEventsTopic), seq[:], rec.payload}); err != nil {
					return
				}
			}
			end := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
			_ = writeMessage(c, [][]byte{{}, {}, end, {}})
		}()
	}
}

// Reads the client's greeting and ready, answers both, and reads the subscription of a SUB
func (p *fakePub) handshake(c net.Conn, r *bufio.Reader, socketType string) (string, bool) {
	greeting := make([]byte, 64)
	if _, err := io.ReadFull(r, greeting); err != nil || greeting[0] != 0xff || greeting[10] != 3 {
		return "", false
	}
	mine := make([]byte, 64)
	mine[0], mine[9], mine[10], mine[11] = 0xff, 0x7f, 3, p.minor
	copy(mine[12:], "NULL")
	if _, err := c.Write(mine); err != nil {
		return "", false
	}
	frames, command, err := readFrames(r)
	if err != nil || !command || string(frames[0][:6]) != "\x05READY" {
		return "", false
	}
	if _, err := c.Write(frame(readyCommand(socketType), 0x04)); err != nil {
		return "", false
	}
	if socketType != "PUB" {
		return "", true
	}
	frames, command, err = readFrames(r)
	if err != nil {
		return "", false
	}
	switch {
	case p.minor >= 1 && command && len(frames[0]) >= 10 && string(frames[0][:10]) == "\x09SUBSCRIBE":
		return string(frames[0][10:]), true
	case p.minor == 0 && !command && len(frames) == 1 && len(frames[0]) >= 1 && frames[0][0] == 1:
		return string(frames[0][1:]), true
	}
	p.t.Errorf("subscription under ZMTP 3.%d came as command=%v %q", p.minor, command, frames[0])
	return "", false
}

// Publishes a payload to every subscriber whose topic prefixes ours, keeping it for replay
func (p *fakePub) publish(payload []byte) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	seq := p.seq
	p.seq++
	p.buffer = append(p.buffer, record{seq, payload})
	var s [8]byte
	binary.BigEndian.PutUint64(s[:], seq)
	for i, c := range p.subs {
		if len(p.topics[i]) <= len(kvEventsTopic) && kvEventsTopic[:len(p.topics[i])] == p.topics[i] {
			_ = writeMessage(c, [][]byte{[]byte(kvEventsTopic), s[:], payload})
		}
	}
	return seq
}

// Buffers a payload under the next sequence number without sending it, as a dropped batch
func (p *fakePub) drop(payload []byte) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	seq := p.seq
	p.seq++
	p.buffer = append(p.buffer, record{seq, payload})
	return seq
}

// Forgets the oldest n buffered batches
func (p *fakePub) forget(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buffer = p.buffer[min(n, len(p.buffer)):]
}

// Closes every subscriber connection
func (p *fakePub) kick() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.subs {
		c.Close()
	}
	p.subs, p.topics = nil, nil
}

func (p *fakePub) subscribers() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.subs)
}

// Reads one ZMTP message or command
func readFrames(r *bufio.Reader) ([][]byte, bool, error) {
	var frames [][]byte
	for {
		flags, err := r.ReadByte()
		if err != nil {
			return nil, false, err
		}
		var size uint64
		if flags&0x02 != 0 {
			var b [8]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return nil, false, err
			}
			size = binary.BigEndian.Uint64(b[:])
		} else {
			b, err := r.ReadByte()
			if err != nil {
				return nil, false, err
			}
			size = uint64(b)
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, false, err
		}
		frames = append(frames, body)
		if flags&0x04 != 0 {
			return frames, true, nil
		}
		if flags&0x01 == 0 {
			return frames, false, nil
		}
	}
}

func readMessage(r *bufio.Reader) ([][]byte, error) {
	for {
		frames, command, err := readFrames(r)
		if err != nil {
			return nil, err
		}
		if !command {
			return frames, nil
		}
	}
}

func writeMessage(c net.Conn, frames [][]byte) error {
	var out []byte
	for i, f := range frames {
		flags := byte(0)
		if i < len(frames)-1 {
			flags = 0x01
		}
		out = append(out, frame(f, flags)...)
	}
	_, err := c.Write(out)
	return err
}
