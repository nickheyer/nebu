package kvevents

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// ZMTP socket types this client takes on
const (
	socketSub    = "SUB"
	socketDealer = "DEALER"
)

const (
	// Largest frame read from a publisher
	maxFrame = 64 << 20
	// Longest wait for the greeting and ready handshake
	handshakeTimeout = 5 * time.Second
)

var (
	ErrGreeting  = errors.New("peer greeting is not ZMTP 3")
	ErrMechanism = errors.New("peer wants a security mechanism other than NULL")
	ErrHandshake = errors.New("peer answered the ready command with something else")
	ErrFrame     = errors.New("frame exceeds the size cap")
	ErrSocket    = errors.New("peer is not the socket type expected")
)

// A ZMTP 3 connection speaking the NULL mechanism, SUB to a PUB or DEALER to a ROUTER
type zconn struct {
	c  net.Conn
	r  *bufio.Reader
	v3 byte
}

// Dials addr, exchanges greetings and ready commands, and subscribes to topic for a SUB socket
func dialZMTP(ctx context.Context, addr, socketType, topic string) (*zconn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	z := &zconn{c: c, r: bufio.NewReaderSize(c, 1<<16)}
	_ = c.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := z.handshake(socketType, topic); err != nil {
		c.Close()
		return nil, err
	}
	_ = c.SetDeadline(time.Time{})
	return z, nil
}

func (z *zconn) Close() error { return z.c.Close() }

// Sends our greeting, checks the peer's, exchanges NULL ready commands and sends the subscription
func (z *zconn) handshake(socketType, topic string) error {
	greeting := make([]byte, 64)
	greeting[0], greeting[9], greeting[10], greeting[11] = 0xff, 0x7f, 3, 1
	copy(greeting[12:], "NULL")
	if _, err := z.c.Write(greeting); err != nil {
		return err
	}
	peer := make([]byte, 64)
	if _, err := io.ReadFull(z.r, peer); err != nil {
		return fmt.Errorf("greeting: %w", err)
	}
	if peer[0] != 0xff || peer[9] != 0x7f || peer[10] != 3 {
		return ErrGreeting
	}
	if string(peer[12:16]) != "NULL" || peer[16] != 0 {
		return ErrMechanism
	}
	z.v3 = peer[11]
	if err := z.writeCommand(readyCommand(socketType)); err != nil {
		return err
	}
	frames, command, err := z.read()
	if err != nil {
		return fmt.Errorf("ready: %w", err)
	}
	if !command || len(frames) != 1 || len(frames[0]) < 6 || string(frames[0][:6]) != "\x05READY" {
		return ErrHandshake
	}
	if err := checkPeerType(frames[0][6:], socketType); err != nil {
		return err
	}
	if socketType != socketSub {
		return nil
	}
	if z.v3 >= 1 {
		return z.writeCommand(append([]byte("\x09SUBSCRIBE"), topic...))
	}
	return z.write([][]byte{append([]byte{1}, topic...)})
}

// The READY command body naming our socket type
func readyCommand(socketType string) []byte {
	body := []byte("\x05READY")
	body = append(body, byte(len("Socket-Type")))
	body = append(body, "Socket-Type"...)
	body = binary.BigEndian.AppendUint32(body, uint32(len(socketType)))
	return append(body, socketType...)
}

// Refuses a peer whose Socket-Type property is not what our socket pairs with
func checkPeerType(props []byte, socketType string) error {
	want := "PUB"
	if socketType == socketDealer {
		want = "ROUTER"
	}
	for len(props) > 0 {
		n := int(props[0])
		if len(props) < 1+n+4 {
			return ErrHandshake
		}
		name := string(props[1 : 1+n])
		size := int(binary.BigEndian.Uint32(props[1+n:]))
		if len(props) < 1+n+4+size {
			return ErrHandshake
		}
		value := string(props[1+n+4 : 1+n+4+size])
		props = props[1+n+4+size:]
		if name == "Socket-Type" {
			if value != want && !(want == "PUB" && value == "XPUB") {
				return fmt.Errorf("%w: %s", ErrSocket, value)
			}
			return nil
		}
	}
	return ErrHandshake
}

// Writes one command frame
func (z *zconn) writeCommand(body []byte) error {
	_, err := z.c.Write(frame(body, 0x04))
	return err
}

// Writes one message of frames
func (z *zconn) write(frames [][]byte) error {
	var out []byte
	for i, f := range frames {
		flags := byte(0)
		if i < len(frames)-1 {
			flags = 0x01
		}
		out = append(out, frame(f, flags)...)
	}
	_, err := z.c.Write(out)
	return err
}

// One frame with its flags, long form past 255 bytes
func frame(body []byte, flags byte) []byte {
	if len(body) > 255 {
		out := make([]byte, 0, 9+len(body))
		out = append(out, flags|0x02)
		out = binary.BigEndian.AppendUint64(out, uint64(len(body)))
		return append(out, body...)
	}
	return append([]byte{flags, byte(len(body))}, body...)
}

// Reads one message of frames, or one command frame, answering pings itself
func (z *zconn) read() ([][]byte, bool, error) {
	var frames [][]byte
	for {
		flags, err := z.r.ReadByte()
		if err != nil {
			return nil, false, err
		}
		var size uint64
		if flags&0x02 != 0 {
			var b [8]byte
			if _, err := io.ReadFull(z.r, b[:]); err != nil {
				return nil, false, err
			}
			size = binary.BigEndian.Uint64(b[:])
		} else {
			b, err := z.r.ReadByte()
			if err != nil {
				return nil, false, err
			}
			size = uint64(b)
		}
		if size > maxFrame {
			return nil, false, ErrFrame
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(z.r, body); err != nil {
			return nil, false, err
		}
		if flags&0x04 != 0 {
			if len(frames) > 0 {
				return nil, false, fmt.Errorf("%w: command inside a message", ErrHandshake)
			}
			if len(body) >= 5 && string(body[:5]) == "\x04PING" {
				ctx := []byte{}
				if len(body) > 7 {
					ctx = body[7:]
				}
				if err := z.writeCommand(append([]byte("\x04PONG"), ctx...)); err != nil {
					return nil, false, err
				}
				continue
			}
			return [][]byte{body}, true, nil
		}
		frames = append(frames, body)
		if flags&0x01 == 0 {
			return frames, false, nil
		}
	}
}

// Reads the next message, skipping commands
func (z *zconn) message() ([][]byte, error) {
	for {
		frames, command, err := z.read()
		if err != nil {
			return nil, err
		}
		if !command {
			return frames, nil
		}
	}
}
