package kvevents

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

var (
	ErrTruncated = errors.New("msgpack document ends inside a value")
	ErrType      = errors.New("msgpack value is not of the expected type")
)

// A cursor over one msgpack document
type reader struct {
	buf []byte
	pos int
}

// The next byte, failing at the end of the document
func (r *reader) byte() (byte, error) {
	if r.pos >= len(r.buf) {
		return 0, ErrTruncated
	}
	b := r.buf[r.pos]
	r.pos++
	return b, nil
}

// The next n bytes, failing at the end of the document
func (r *reader) bytes(n int) ([]byte, error) {
	if n < 0 || r.pos+n > len(r.buf) {
		return nil, ErrTruncated
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b, nil
}

// An unsigned big endian integer of n bytes
func (r *reader) uint(n int) (uint64, error) {
	b, err := r.bytes(n)
	if err != nil {
		return 0, err
	}
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v, nil
}

// Whether the next value is nil, consuming it when so
func (r *reader) nilValue() (bool, error) {
	if r.pos >= len(r.buf) {
		return false, ErrTruncated
	}
	if r.buf[r.pos] == 0xc0 {
		r.pos++
		return true, nil
	}
	return false, nil
}

// The element count of an array header
func (r *reader) arrayLen() (int, error) {
	b, err := r.byte()
	if err != nil {
		return 0, err
	}
	switch {
	case b>>4 == 0x9:
		return int(b & 0x0f), nil
	case b == 0xdc:
		n, err := r.uint(2)
		return int(n), err
	case b == 0xdd:
		n, err := r.uint(4)
		return int(n), err
	}
	return 0, fmt.Errorf("%w: array, byte %#x", ErrType, b)
}

// The pair count of a map header
func (r *reader) mapLen() (int, error) {
	b, err := r.byte()
	if err != nil {
		return 0, err
	}
	switch {
	case b>>4 == 0x8:
		return int(b & 0x0f), nil
	case b == 0xde:
		n, err := r.uint(2)
		return int(n), err
	case b == 0xdf:
		n, err := r.uint(4)
		return int(n), err
	}
	return 0, fmt.Errorf("%w: map, byte %#x", ErrType, b)
}

// A signed integer of any width
func (r *reader) int() (int64, error) {
	b, err := r.byte()
	if err != nil {
		return 0, err
	}
	switch {
	case b < 0x80:
		return int64(b), nil
	case b >= 0xe0:
		return int64(int8(b)), nil
	case b == 0xcc:
		v, err := r.uint(1)
		return int64(v), err
	case b == 0xcd:
		v, err := r.uint(2)
		return int64(v), err
	case b == 0xce:
		v, err := r.uint(4)
		return int64(v), err
	case b == 0xcf:
		v, err := r.uint(8)
		return int64(v), err
	case b == 0xd0:
		v, err := r.uint(1)
		return int64(int8(v)), err
	case b == 0xd1:
		v, err := r.uint(2)
		return int64(int16(v)), err
	case b == 0xd2:
		v, err := r.uint(4)
		return int64(int32(v)), err
	case b == 0xd3:
		v, err := r.uint(8)
		return int64(v), err
	}
	return 0, fmt.Errorf("%w: integer, byte %#x", ErrType, b)
}

// A float of either width, or an integer widened to one
func (r *reader) float() (float64, error) {
	if r.pos >= len(r.buf) {
		return 0, ErrTruncated
	}
	switch r.buf[r.pos] {
	case 0xca:
		r.pos++
		v, err := r.uint(4)
		return float64(math.Float32frombits(uint32(v))), err
	case 0xcb:
		r.pos++
		v, err := r.uint(8)
		return math.Float64frombits(v), err
	}
	v, err := r.int()
	return float64(v), err
}

// A string value
func (r *reader) str() (string, error) {
	b, err := r.byte()
	if err != nil {
		return "", err
	}
	var n uint64
	switch {
	case b>>5 == 0x5:
		n = uint64(b & 0x1f)
	case b == 0xd9:
		n, err = r.uint(1)
	case b == 0xda:
		n, err = r.uint(2)
	case b == 0xdb:
		n, err = r.uint(4)
	default:
		return "", fmt.Errorf("%w: string, byte %#x", ErrType, b)
	}
	if err != nil {
		return "", err
	}
	s, err := r.bytes(int(n))
	return string(s), err
}

// A block hash as the publisher spells it, raw bytes kept and an integer written as eight big endian bytes
func (r *reader) hash() (string, error) {
	if r.pos >= len(r.buf) {
		return "", ErrTruncated
	}
	b := r.buf[r.pos]
	var n uint64
	var err error
	switch b {
	case 0xc4:
		r.pos++
		n, err = r.uint(1)
	case 0xc5:
		r.pos++
		n, err = r.uint(2)
	case 0xc6:
		r.pos++
		n, err = r.uint(4)
	default:
		if b>>5 == 0x5 || b == 0xd9 || b == 0xda || b == 0xdb {
			return r.str()
		}
		v, err := r.int()
		if err != nil {
			return "", err
		}
		var out [8]byte
		binary.BigEndian.PutUint64(out[:], uint64(v))
		return string(out[:]), nil
	}
	if err != nil {
		return "", err
	}
	s, err := r.bytes(int(n))
	return string(s), err
}

// Steps over one value of any type
func (r *reader) skip() error {
	b, err := r.byte()
	if err != nil {
		return err
	}
	var n uint64
	switch {
	case b < 0x80, b >= 0xe0, b == 0xc0, b == 0xc2, b == 0xc3:
		return nil
	case b>>5 == 0x5:
		_, err = r.bytes(int(b & 0x1f))
		return err
	case b>>4 == 0x9:
		return r.skipN(int(b & 0x0f))
	case b>>4 == 0x8:
		return r.skipN(2 * int(b&0x0f))
	case b == 0xcc, b == 0xd0, b == 0xd4:
		_, err = r.bytes(1 + boolInt(b == 0xd4))
		return err
	case b == 0xcd, b == 0xd1, b == 0xd5:
		_, err = r.bytes(2 + boolInt(b == 0xd5))
		return err
	case b == 0xce, b == 0xd2, b == 0xca, b == 0xd6:
		_, err = r.bytes(4 + boolInt(b == 0xd6))
		return err
	case b == 0xcf, b == 0xd3, b == 0xcb, b == 0xd7:
		_, err = r.bytes(8 + boolInt(b == 0xd7))
		return err
	case b == 0xd8:
		_, err = r.bytes(17)
		return err
	case b == 0xc4, b == 0xd9, b == 0xc7:
		n, err = r.uint(1)
	case b == 0xc5, b == 0xda, b == 0xc8:
		n, err = r.uint(2)
	case b == 0xc6, b == 0xdb, b == 0xc9:
		n, err = r.uint(4)
	case b == 0xdc, b == 0xde:
		n, err = r.uint(2)
		if err != nil {
			return err
		}
		return r.skipN(int(n) * (1 + boolInt(b == 0xde)))
	case b == 0xdd, b == 0xdf:
		n, err = r.uint(4)
		if err != nil {
			return err
		}
		return r.skipN(int(n) * (1 + boolInt(b == 0xdf)))
	default:
		return fmt.Errorf("%w: byte %#x", ErrType, b)
	}
	if err != nil {
		return err
	}
	if b == 0xc7 || b == 0xc8 || b == 0xc9 {
		n++
	}
	_, err = r.bytes(int(n))
	return err
}

// Steps over n values
func (r *reader) skipN(n int) error {
	for i := 0; i < n; i++ {
		if err := r.skip(); err != nil {
			return err
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
