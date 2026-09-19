package nemo

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	blockSize = 512
	lookahead = 4096
	maxName   = 1 << 16
)

// One member of the archive
type entry struct {
	name   string
	size   int64
	offset int64
	dir    bool
}

// Reads tar headers without member data, caching a small window to reduce repeated reads.
type walker struct {
	ra     io.ReaderAt
	size   int64
	pos    int64
	buf    []byte
	bufOff int64
}

func newWalker(ra io.ReaderAt, size int64) *walker {
	return &walker{ra: ra, size: size}
}

// Reads n bytes at off, through the window when it covers them
func (w *walker) readAt(off, n int64) ([]byte, error) {
	if off >= w.size {
		return nil, io.EOF
	}
	if off >= w.bufOff && off+n <= w.bufOff+int64(len(w.buf)) {
		return w.buf[off-w.bufOff : off-w.bufOff+n], nil
	}
	want := max(n, lookahead)
	if off+want > w.size {
		want = w.size - off
	}
	buf := make([]byte, want)
	read, err := w.ra.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	buf = buf[:read]
	w.buf, w.bufOff = buf, off
	if int64(len(buf)) < n {
		return nil, io.ErrUnexpectedEOF
	}
	return buf[:n], nil
}

// Returns the next file or directory, nil at the end of the archive
func (w *walker) next() (*entry, error) {
	var longName, paxPath string
	for {
		hdr, err := w.readAt(w.pos, blockSize)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if bytes.Equal(hdr, make([]byte, blockSize)) {
			return nil, nil
		}
		if string(hdr[257:262]) != "ustar" && w.pos == 0 {
			return nil, fmt.Errorf("not a tar archive")
		}
		size, err := octal(hdr[124:136])
		if err != nil {
			return nil, err
		}
		typ := hdr[156]
		name := cstring(hdr[0:100])
		if prefix := cstring(hdr[345:500]); prefix != "" && string(hdr[257:262]) == "ustar" && hdr[262] == 0 {
			name = prefix + "/" + name
		}
		data := w.pos + blockSize
		w.pos = data + (size+blockSize-1)/blockSize*blockSize
		switch typ {
		case 'L':
			raw, err := w.small(data, size)
			if err != nil {
				return nil, err
			}
			longName = cstring(raw)
			continue
		case 'x':
			raw, err := w.small(data, size)
			if err != nil {
				return nil, err
			}
			paxPath = paxValue(raw, "path")
			continue
		case 'g', 'K':
			continue
		}
		switch {
		case paxPath != "":
			name = paxPath
		case longName != "":
			name = longName
		}
		return &entry{name: name, size: size, offset: data, dir: typ == '5'}, nil
	}
}

// Returns the bytes of a small member
func (w *walker) contents(e *entry) ([]byte, error) {
	return w.small(e.offset, e.size)
}

func (w *walker) small(off, size int64) ([]byte, error) {
	if size > maxSmall {
		return nil, fmt.Errorf("member of %d bytes is too large to read whole", size)
	}
	buf, err := w.readAt(off, size)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), buf...), nil
}

func octal(b []byte) (int64, error) {
	if len(b) > 0 && b[0]&0x80 != 0 {
		// Base 256 for sizes past 8 GiB
		var n int64
		for _, c := range b[1:] {
			n = n<<8 | int64(c)
		}
		return n, nil
	}
	s := strings.Trim(cstring(b), " ")
	if s == "" {
		return 0, nil
	}
	return strconv.ParseInt(s, 8, 64)
}

func cstring(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// Reads one keyword out of a pax extended header
func paxValue(raw []byte, key string) string {
	for len(raw) > 0 {
		sp := bytes.IndexByte(raw, ' ')
		if sp < 0 {
			return ""
		}
		n, err := strconv.Atoi(string(raw[:sp]))
		if err != nil || n <= 0 || n > len(raw) {
			return ""
		}
		record := raw[sp+1 : n]
		raw = raw[n:]
		if k, v, ok := strings.Cut(strings.TrimSuffix(string(record), "\n"), "="); ok && k == key {
			if len(v) > maxName {
				return ""
			}
			return v
		}
	}
	return ""
}
