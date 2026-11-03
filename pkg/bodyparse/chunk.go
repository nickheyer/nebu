package bodyparse

import (
	"errors"
	"io"
)

// Splits a body into chunks of at most limit bytes, defaulting to ChunkBytes, an empty body giving one empty chunk
func SplitChunks(body []byte, limit int) [][]byte {
	if limit <= 0 {
		limit = ChunkBytes
	}
	if len(body) == 0 {
		return [][]byte{body}
	}
	out := make([][]byte, 0, (len(body)+limit-1)/limit)
	for len(body) > limit {
		out = append(out, body[:limit:limit])
		body = body[limit:]
	}
	return append(out, body)
}

// Reads a stream as chunks of ChunkBytes, flagging the last and failing once the total passes the limit
type Reader struct {
	r     io.Reader
	limit int64
	total int64

	started bool
	done    bool
	cur     []byte
	curLast bool
	curErr  error
}

// Reads r in chunks, a limit of zero or less reading without a cap
func NewReader(r io.Reader, limit int64) *Reader {
	return &Reader{r: r, limit: limit}
}

// The next chunk and whether it is the last, io.EOF once the last was returned
func (c *Reader) Next() ([]byte, bool, error) {
	if c.done {
		return nil, false, io.EOF
	}
	if !c.started {
		c.started = true
		c.cur, c.curLast, c.curErr = c.fill()
	}
	if c.curErr != nil {
		c.done = true
		return nil, false, c.curErr
	}
	chunk := c.cur
	if c.curLast {
		c.done = true
		return chunk, true, nil
	}
	next, last, err := c.fill()
	if err == nil && last && len(next) == 0 {
		c.done = true
		return chunk, true, nil
	}
	c.cur, c.curLast, c.curErr = next, last, err
	return chunk, false, nil
}

// Reads one full chunk, a short read meaning the stream ended
func (c *Reader) fill() ([]byte, bool, error) {
	buf := make([]byte, ChunkBytes)
	n, err := io.ReadFull(c.r, buf)
	c.total += int64(n)
	if c.limit > 0 && c.total > c.limit {
		return nil, false, ErrTooLarge
	}
	switch {
	case err == nil:
		return buf, false, nil
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return buf[:n], true, nil
	default:
		return nil, false, err
	}
}
