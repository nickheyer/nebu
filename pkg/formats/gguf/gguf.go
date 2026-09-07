// Package gguf reads GGUF headers without downloading tensor data.
package gguf

import (
	"context"
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

type reader struct {
	dtypes map[string]string
}

// Builds a GGUF reader using dtype names from the spec
func New(spec *v1.FormatSpec) (formats.Reader, error) {
	return &reader{dtypes: spec.GetDtypes()}, nil
}

func (r *reader) Read(ctx context.Context, open formats.Opener, group *formats.Group) (*v1.RawModel, error) {
	raw, err := formats.EachWeight(ctx, open, group, r.parse)
	if err != nil {
		return nil, err
	}
	// The projector a run loads beside the weights counts with them, the one a launch picks, its
	// tensors joining the table while its header, which describes the encoder alone, stays out
	if files := group.Files[v1.ArtifactRole_ARTIFACT_ROLE_PROJECTOR]; len(files) > 0 {
		blob, err := open(ctx, files[0])
		if err != nil {
			return nil, err
		}
		_, tensors, err := r.parse(blob, blob.Size())
		blob.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", files[0].GetPath(), err)
		}
		raw.Tensors = append(raw.Tensors, tensors...)
	}
	return raw, nil
}

type tensorEntry struct {
	info   *v1.TensorInfo
	offset uint64
}

func (r *reader) parse(ra io.ReaderAt, size int64) (map[string]string, []*v1.TensorInfo, error) {
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
			info:   &v1.TensorInfo{Name: name, Dtype: r.dtype(typ), Elements: formats.Elements(shape)},
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

func (r *reader) dtype(typ uint32) string {
	if name, ok := r.dtypes[strconv.FormatUint(uint64(typ), 10)]; ok {
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
