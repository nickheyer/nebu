// Package gguf reads GGUF headers without downloading tensor data.
package gguf

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	magic            = "GGUF"
	minVersion       = 2
	defaultAlignment = 32
	maxCount         = 1 << 24
	maxString        = 64 << 20
	maxJoined        = 1024
	alignmentKey     = "general.alignment"
)

const (
	typeUint8 uint32 = iota
	typeInt8
	typeUint16
	typeInt16
	typeUint32
	typeInt32
	typeFloat32
	typeBool
	typeString
	typeArray
	typeUint64
	typeInt64
	typeFloat64
)

const (
	firstChunk = 1 << 20
	maxChunk   = 32 << 20
)

// Reads a blob sequentially in doubling chunks, counting bytes handed out
type chunkReader struct {
	ra    io.ReaderAt
	size  int64
	off   int64
	chunk int64
	buf   []byte
	pos   int
	n     int64
}

func newChunkReader(ra io.ReaderAt, size int64) *chunkReader {
	return &chunkReader{ra: ra, size: size, chunk: firstChunk}
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.buf) {
		if c.off >= c.size {
			return 0, io.EOF
		}
		n := min(c.chunk, c.size-c.off)
		if int64(cap(c.buf)) < n {
			c.buf = make([]byte, n)
		}
		c.buf = c.buf[:n]
		read, err := c.ra.ReadAt(c.buf, c.off)
		if err != nil && err != io.EOF {
			return 0, err
		}
		if read == 0 {
			return 0, io.EOF
		}
		c.buf = c.buf[:read]
		c.off += int64(read)
		c.pos = 0
		c.chunk = min(c.chunk*2, maxChunk)
	}
	n := copy(p, c.buf[c.pos:])
	c.pos += n
	c.n += int64(n)
	return n, nil
}

// GGML tensor types by number, the names quants go by
var dtypes = map[uint32]string{
	0: "F32", 1: "F16", 2: "Q4_0", 3: "Q4_1", 6: "Q5_0", 7: "Q5_1", 8: "Q8_0", 9: "Q8_1",
	10: "Q2_K", 11: "Q3_K", 12: "Q4_K", 13: "Q5_K", 14: "Q6_K", 15: "Q8_K",
	16: "IQ2_XXS", 17: "IQ2_XS", 18: "IQ3_XXS", 19: "IQ1_S", 20: "IQ4_NL", 21: "IQ3_S", 22: "IQ2_S", 23: "IQ4_XS",
	24: "I8", 25: "I16", 26: "I32", 27: "I64", 28: "F64", 29: "IQ1_M", 30: "BF16", 34: "TQ1_0", 35: "TQ2_0", 39: "MXFP4",
}

type tensorEntry struct {
	info   *v1.TensorInfo
	offset uint64
}

func parse(ra io.ReaderAt, size int64) (map[string]string, []*v1.TensorInfo, error) {
	cr := newChunkReader(ra, size)
	var m [4]byte
	if _, err := io.ReadFull(cr, m[:]); err != nil {
		return nil, nil, err
	}
	if string(m[:]) != magic {
		return nil, nil, fmt.Errorf("not a GGUF file")
	}
	version, err := readU32(cr)
	if err != nil {
		return nil, nil, err
	}
	if version < minVersion {
		return nil, nil, fmt.Errorf("unsupported GGUF version %d", version)
	}
	nTensors, err := readU64(cr)
	if err != nil {
		return nil, nil, err
	}
	nKV, err := readU64(cr)
	if err != nil {
		return nil, nil, err
	}
	if nTensors > maxCount || nKV > maxCount {
		return nil, nil, fmt.Errorf("implausible header counts")
	}
	metadata := map[string]string{}
	for i := uint64(0); i < nKV; i++ {
		key, err := readString(cr)
		if err != nil {
			return nil, nil, err
		}
		typ, err := readU32(cr)
		if err != nil {
			return nil, nil, err
		}
		if err := readValue(cr, typ, key, metadata); err != nil {
			return nil, nil, fmt.Errorf("key %s: %w", key, err)
		}
	}
	entries := make([]tensorEntry, 0, nTensors)
	for i := uint64(0); i < nTensors; i++ {
		name, err := readString(cr)
		if err != nil {
			return nil, nil, err
		}
		nDims, err := readU32(cr)
		if err != nil {
			return nil, nil, err
		}
		if nDims > 8 {
			return nil, nil, fmt.Errorf("tensor %s: %d dims", name, nDims)
		}
		shape := make([]uint64, nDims)
		for d := range shape {
			if shape[d], err = readU64(cr); err != nil {
				return nil, nil, err
			}
		}
		typ, err := readU32(cr)
		if err != nil {
			return nil, nil, err
		}
		offset, err := readU64(cr)
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, tensorEntry{
			info:   &v1.TensorInfo{Name: name, Dtype: dtype(typ), Elements: formats.Elements(shape)},
			offset: offset,
		})
	}
	alignment := uint64(defaultAlignment)
	if s, ok := metadata[alignmentKey]; ok {
		if a, err := strconv.ParseUint(s, 10, 64); err == nil && a > 0 {
			alignment = a
		}
	}
	dataStart := (uint64(cr.n) + alignment - 1) / alignment * alignment
	// A shard holding only metadata may end right after the header, before the padding a tensor would follow
	if uint64(size) < dataStart && len(entries) == 0 {
		dataStart = uint64(size)
	}
	if uint64(size) < dataStart {
		return nil, nil, fmt.Errorf("file smaller than header")
	}
	dataSize := uint64(size) - dataStart
	sort.Slice(entries, func(i, j int) bool { return entries[i].offset < entries[j].offset })
	tensors := make([]*v1.TensorInfo, 0, len(entries))
	for i, e := range entries {
		end := dataSize
		if i+1 < len(entries) {
			end = entries[i+1].offset
		}
		if end < e.offset {
			return nil, nil, fmt.Errorf("tensor %s: offset beyond data", e.info.Name)
		}
		e.info.Bytes = end - e.offset
		tensors = append(tensors, e.info)
	}
	return metadata, tensors, nil
}

func dtype(typ uint32) string {
	if name, ok := dtypes[typ]; ok {
		return name
	}
	return "type_" + strconv.FormatUint(uint64(typ), 10)
}

func readValue(r io.Reader, typ uint32, key string, into map[string]string) error {
	if typ == typeArray {
		elemType, err := readU32(r)
		if err != nil {
			return err
		}
		n, err := readU64(r)
		if err != nil {
			return err
		}
		if n > maxCount {
			return fmt.Errorf("array too large")
		}
		into[key+".length"] = strconv.FormatUint(n, 10)
		var parts []string
		for i := uint64(0); i < n; i++ {
			if elemType == typeArray {
				if err := readValue(r, typeArray, key+"."+strconv.FormatUint(i, 10), into); err != nil {
					return err
				}
				continue
			}
			s, err := readScalar(r, elemType)
			if err != nil {
				return err
			}
			if n <= maxJoined {
				parts = append(parts, s)
			}
		}
		if n <= maxJoined && elemType != typeArray {
			into[key] = strings.Join(parts, ",")
		}
		return nil
	}
	s, err := readScalar(r, typ)
	if err != nil {
		return err
	}
	into[key] = s
	return nil
}

func readScalar(r io.Reader, typ uint32) (string, error) {
	switch typ {
	case typeUint8:
		v, err := readN(r, 1)
		return strconv.FormatUint(v, 10), err
	case typeInt8:
		v, err := readN(r, 1)
		return strconv.FormatInt(int64(int8(v)), 10), err
	case typeUint16:
		v, err := readN(r, 2)
		return strconv.FormatUint(v, 10), err
	case typeInt16:
		v, err := readN(r, 2)
		return strconv.FormatInt(int64(int16(v)), 10), err
	case typeUint32:
		v, err := readN(r, 4)
		return strconv.FormatUint(v, 10), err
	case typeInt32:
		v, err := readN(r, 4)
		return strconv.FormatInt(int64(int32(v)), 10), err
	case typeFloat32:
		v, err := readN(r, 4)
		return strconv.FormatFloat(float64(math.Float32frombits(uint32(v))), 'g', -1, 32), err
	case typeBool:
		v, err := readN(r, 1)
		return strconv.FormatBool(v != 0), err
	case typeString:
		return readString(r)
	case typeUint64:
		v, err := readN(r, 8)
		return strconv.FormatUint(v, 10), err
	case typeInt64:
		v, err := readN(r, 8)
		return strconv.FormatInt(int64(v), 10), err
	case typeFloat64:
		v, err := readN(r, 8)
		return strconv.FormatFloat(math.Float64frombits(v), 'g', -1, 64), err
	}
	return "", fmt.Errorf("unknown value type %d", typ)
}

func readN(r io.Reader, n int) (uint64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:n]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

func readU32(r io.Reader) (uint32, error) {
	v, err := readN(r, 4)
	return uint32(v), err
}

func readU64(r io.Reader) (uint64, error) {
	return readN(r, 8)
}

func readString(r io.Reader) (string, error) {
	n, err := readU64(r)
	if err != nil {
		return "", err
	}
	if n > maxString {
		return "", fmt.Errorf("string too large")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}
