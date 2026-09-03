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

type reader struct {
	dtypes map[string]string
}

// Builds a GGUF reader using dtype names from the spec
func New(spec *v1.FormatSpec) (formats.Reader, error) {
	return &reader{dtypes: spec.GetDtypes()}, nil
}

func (r *reader) Read(ctx context.Context, open formats.Opener, group *formats.Group) (*v1.RawModel, error) {
	raw := &v1.RawModel{FormatId: group.FormatID, Group: group.Name, Metadata: map[string]string{}}
	for _, a := range group.Weights {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		blob, err := open(ctx, a)
		if err != nil {
			return nil, err
		}
		h, err := r.parse(blob, blob.Size())
		blob.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.GetPath(), err)
		}
		for k, v := range h.metadata {
			if _, exists := raw.Metadata[k]; !exists {
				raw.Metadata[k] = v
			}
		}
		raw.Tensors = append(raw.Tensors, h.tensors...)
	}
	return raw, nil
}

type header struct {
	metadata map[string]string
	tensors  []*v1.TensorInfo
}

type tensorEntry struct {
	info   *v1.TensorInfo
	offset uint64
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (r *reader) parse(ra io.ReaderAt, size int64) (*header, error) {
	cr := &countingReader{r: formats.NewChunkReader(ra, size)}
	var m [4]byte
	if _, err := io.ReadFull(cr, m[:]); err != nil {
		return nil, err
	}
	if string(m[:]) != magic {
		return nil, fmt.Errorf("not a GGUF file")
	}
	version, err := readU32(cr)
	if err != nil {
		return nil, err
	}
	if version < minVersion {
		return nil, fmt.Errorf("unsupported GGUF version %d", version)
	}
	nTensors, err := readU64(cr)
	if err != nil {
		return nil, err
	}
	nKV, err := readU64(cr)
	if err != nil {
		return nil, err
	}
	if nTensors > maxCount || nKV > maxCount {
		return nil, fmt.Errorf("implausible header counts")
	}
	h := &header{metadata: map[string]string{}}
	for i := uint64(0); i < nKV; i++ {
		key, err := readString(cr)
		if err != nil {
			return nil, err
		}
		typ, err := readU32(cr)
		if err != nil {
			return nil, err
		}
		if err := readValue(cr, typ, key, h.metadata); err != nil {
			return nil, fmt.Errorf("key %s: %w", key, err)
		}
	}
	entries := make([]tensorEntry, 0, nTensors)
	for i := uint64(0); i < nTensors; i++ {
		name, err := readString(cr)
		if err != nil {
			return nil, err
		}
		nDims, err := readU32(cr)
		if err != nil {
			return nil, err
		}
		if nDims > 8 {
			return nil, fmt.Errorf("tensor %s: %d dims", name, nDims)
		}
		elements := uint64(1)
		for d := uint32(0); d < nDims; d++ {
			dim, err := readU64(cr)
			if err != nil {
				return nil, err
			}
			elements *= dim
		}
		typ, err := readU32(cr)
		if err != nil {
			return nil, err
		}
		offset, err := readU64(cr)
		if err != nil {
			return nil, err
		}
		entries = append(entries, tensorEntry{
			info:   &v1.TensorInfo{Name: name, Dtype: r.dtype(typ), Elements: elements},
			offset: offset,
		})
	}
	alignment := uint64(defaultAlignment)
	if s, ok := h.metadata[alignmentKey]; ok {
		if a, err := strconv.ParseUint(s, 10, 64); err == nil && a > 0 {
			alignment = a
		}
	}
	dataStart := (uint64(cr.n) + alignment - 1) / alignment * alignment
	dataSize := uint64(size) - dataStart
	if uint64(size) < dataStart {
		return nil, fmt.Errorf("file smaller than header")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].offset < entries[j].offset })
	for i, e := range entries {
		end := dataSize
		if i+1 < len(entries) {
			end = entries[i+1].offset
		}
		if end < e.offset {
			return nil, fmt.Errorf("tensor %s: offset beyond data", e.info.Name)
		}
		e.info.Bytes = end - e.offset
		h.tensors = append(h.tensors, e.info)
	}
	return h, nil
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
