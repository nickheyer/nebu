// Package gguftest writes small GGUF files for tests.
package gguftest

import (
	"bytes"
	"encoding/binary"
	"io"
	"sort"
)

// Metadata value types supported by the writer
const (
	TypeUint32  uint32 = 4
	TypeFloat32 uint32 = 6
	TypeBool    uint32 = 7
	TypeString  uint32 = 8
	TypeArray   uint32 = 9
	TypeUint64  uint32 = 10
)

// One tensor to write with real data bytes
type Tensor struct {
	Name  string
	Dims  []uint64
	Type  uint32
	Bytes uint64
}

// Writes a version 3 GGUF with metadata and tensors
func Write(w io.Writer, kv map[string]any, tensors []Tensor, alignment uint64) error {
	var buf bytes.Buffer
	buf.WriteString("GGUF")
	le := binary.LittleEndian
	binary.Write(&buf, le, uint32(3))
	binary.Write(&buf, le, uint64(len(tensors)))
	binary.Write(&buf, le, uint64(len(kv)))
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeString(&buf, k)
		writeValue(&buf, kv[k])
	}
	var offset uint64
	for _, t := range tensors {
		writeString(&buf, t.Name)
		binary.Write(&buf, le, uint32(len(t.Dims)))
		for _, d := range t.Dims {
			binary.Write(&buf, le, d)
		}
		binary.Write(&buf, le, t.Type)
		binary.Write(&buf, le, offset)
		offset = align(offset+t.Bytes, alignment)
	}
	for uint64(buf.Len())%alignment != 0 {
		buf.WriteByte(0)
	}
	var data uint64
	for _, t := range tensors {
		buf.Write(make([]byte, t.Bytes))
		data += t.Bytes
		for data%alignment != 0 {
			buf.WriteByte(0)
			data++
		}
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func align(n, a uint64) uint64 { return (n + a - 1) / a * a }

func writeString(buf *bytes.Buffer, s string) {
	binary.Write(buf, binary.LittleEndian, uint64(len(s)))
	buf.WriteString(s)
}

func writeValue(buf *bytes.Buffer, v any) {
	le := binary.LittleEndian
	switch t := v.(type) {
	case string:
		binary.Write(buf, le, TypeString)
		writeString(buf, t)
	case uint32:
		binary.Write(buf, le, TypeUint32)
		binary.Write(buf, le, t)
	case int:
		binary.Write(buf, le, TypeUint32)
		binary.Write(buf, le, uint32(t))
	case uint64:
		binary.Write(buf, le, TypeUint64)
		binary.Write(buf, le, t)
	case float32:
		binary.Write(buf, le, TypeFloat32)
		binary.Write(buf, le, t)
	case bool:
		binary.Write(buf, le, TypeBool)
		b := uint8(0)
		if t {
			b = 1
		}
		binary.Write(buf, le, b)
	case []string:
		binary.Write(buf, le, TypeArray)
		binary.Write(buf, le, TypeString)
		binary.Write(buf, le, uint64(len(t)))
		for _, s := range t {
			writeString(buf, s)
		}
	case []uint32:
		binary.Write(buf, le, TypeArray)
		binary.Write(buf, le, TypeUint32)
		binary.Write(buf, le, uint64(len(t)))
		for _, n := range t {
			binary.Write(buf, le, n)
		}
	default:
		panic("gguftest: unsupported value type")
	}
}
