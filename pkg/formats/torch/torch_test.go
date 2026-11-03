package torch

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// Writes the pickle opcodes a torch.save checkpoint uses
type pk struct{ bytes.Buffer }

func (p *pk) op(b ...byte) *pk       { p.Write(b); return p }
func (p *pk) str(s string) *pk       { return p.op('U', byte(len(s))).op([]byte(s)...) }
func (p *pk) global(m, n string) *pk { p.op('c'); p.WriteString(m + "\n" + n + "\n"); return p }
func (p *pk) int(n int32) *pk        { p.op('J'); binary.Write(p, binary.LittleEndian, n); return p }
func (p *pk) float(f float64) *pk    { p.op('G'); binary.Write(p, binary.BigEndian, f); return p }
func (p *pk) boolean(v bool) *pk     { return p.op(map[bool]byte{true: 0x88, false: 0x89}[v]) }
func (p *pk) mark() *pk              { return p.op('(') }
func (p *pk) tuple() *pk             { return p.op('t') }
func (p *pk) emptyTuple() *pk        { return p.op(')') }
func (p *pk) reduce() *pk            { return p.op('R') }
func (p *pk) emptyDict() *pk         { return p.op('}') }
func (p *pk) setItems() *pk          { return p.op('u') }
func (p *pk) persid() *pk            { return p.op('Q') }

// A storage reference naming its class, as torch persists them
func (p *pk) storage(class string) *pk {
	return p.mark().str("storage").global("torch", class).str("0").str("cpu").int(99).tuple().persid()
}

// The size tuple torch.Size carries
func (p *pk) size(dims ...int32) *pk {
	p.global("torch", "Size").mark()
	for _, d := range dims {
		p.int(d)
	}
	return p.tuple().op(0x85).reduce()
}

// A _rebuild_tensor_v2 call over a storage of the class with the shape
func (p *pk) tensor(class string, dims ...int32) *pk {
	p.global("torch._utils", "_rebuild_tensor_v2").mark().storage(class).int(0).size(dims...).mark().int(1).tuple().boolean(false).global("collections", "OrderedDict").emptyTuple().reduce().tuple()
	return p.reduce()
}

// A _rebuild_tensor_v3 call carrying its dtype after the hooks
func (p *pk) tensorV3(class, dtype string, dims ...int32) *pk {
	p.global("torch._utils", "_rebuild_tensor_v3").mark().storage(class).int(0).size(dims...).mark().int(1).tuple().boolean(false).global("collections", "OrderedDict").emptyTuple().reduce().global("torch", dtype).tuple()
	return p.reduce()
}

// A zip holding the pickle as data.pkl under the archive directory
func checkpoint(t *testing.T, pickle []byte, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{{"archive/version", "3\n"}, {name, string(pickle)}, {"archive/data/0", "weights"}} {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(f.body))
	}
	zw.Close()
	return buf.Bytes()
}

// Tensors at the top, under state_dict, wrapped as parameters or rebuilt from a type, and nested dicts all read with their dtype and shape
func TestReadZip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pickle  func() []byte
		tensors map[string][]uint64
		dtypes  map[string]string
		meta    map[string]string
	}{
		{"top level tensors and scalars", func() []byte {
			p := new(pk).op(0x80, 2).emptyDict().mark()
			p.str("weight").tensor("FloatStorage", 2, 3)
			p.str("bias").tensor("BFloat16Storage", 3)
			p.str("epoch").int(3).str("name").str("m").str("lr").float(0.5).str("flag").boolean(true).str("skip").mark().int(1).tuple()
			return p.setItems().op('.').Bytes()
		}, map[string][]uint64{"weight": {2, 3}, "bias": {3}}, map[string]string{"weight": "F32", "bias": "BF16"}, map[string]string{"epoch": "3", "name": "m", "lr": "0.5", "flag": "true"}},
		{"state dict entry", func() []byte {
			p := new(pk).op(0x80, 2).emptyDict().mark().str("state_dict").emptyDict().mark()
			p.str("layer.weight").tensor("HalfStorage", 4, 4)
			return p.setItems().str("step").int(7).setItems().op('.').Bytes()
		}, map[string][]uint64{"layer.weight": {4, 4}}, map[string]string{"layer.weight": "F16"}, map[string]string{"step": "7"}},
		{"parameter wrappers and rebuild from type", func() []byte {
			p := new(pk).op(0x80, 2).emptyDict().mark()
			p.str("p").global("torch._utils", "_rebuild_parameter").mark().tensor("DoubleStorage", 2).boolean(true).global("collections", "OrderedDict").emptyTuple().reduce().tuple().reduce()
			p.str("q").global("torch._utils", "_rebuild_from_type_v2").mark().global("torch._utils", "_rebuild_tensor_v2").global("torch", "Tensor").mark().storage("LongStorage").int(0).size(5).mark().int(1).tuple().boolean(false).global("collections", "OrderedDict").emptyTuple().reduce().tuple().emptyDict().tuple().reduce()
			p.str("v3").tensorV3("FloatStorage", "bfloat16", 6)
			p.str("empty").global("torch._utils", "_rebuild_parameter").emptyTuple().reduce()
			return p.setItems().op('.').Bytes()
		}, map[string][]uint64{"p": {2}, "q": {5}, "v3": {6}}, map[string]string{"p": "F64", "q": "I64", "v3": "BF16"}, map[string]string{}},
		{"nested dicts when nothing sits at the top", func() []byte {
			p := new(pk).op(0x80, 2).emptyDict().mark()
			p.str("model").emptyDict().mark().str("encoder").emptyDict().mark().str("w").tensor("FloatStorage", 1, 2).setItems().setItems()
			p.str("other").emptyDict().mark().str("w").tensor("ByteStorage", 8).setItems()
			return p.setItems().op('.').Bytes()
		}, map[string][]uint64{"model.encoder.w": {1, 2}, "other.w": {8}}, map[string]string{"model.encoder.w": "F32", "other.w": "U8"}, map[string]string{}},
		{"tensors at the top hide nested ones", func() []byte {
			p := new(pk).op(0x80, 2).emptyDict().mark()
			p.str("w").tensor("FloatStorage", 2)
			p.str("child").emptyDict().mark().str("w").tensor("FloatStorage", 9).setItems()
			return p.setItems().op('.').Bytes()
		}, map[string][]uint64{"w": {2}}, map[string]string{"w": "F32"}, map[string]string{}},
		{"unknown storage leaves the dtype empty", func() []byte {
			p := new(pk).op(0x80, 2).emptyDict().mark().str("w").tensor("MysteryStorage", 3)
			return p.setItems().op('.').Bytes()
		}, map[string][]uint64{"w": {3}}, map[string]string{"w": ""}, map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := checkpoint(t, tc.pickle(), "archive/data.pkl")
			ck, err := ReadZip(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if len(ck.Tensors) != len(tc.tensors) || len(ck.Shapes) != len(ck.Tensors) {
				t.Fatalf("tensors %v shapes %v", ck.Tensors, ck.Shapes)
			}
			for i, info := range ck.Tensors {
				shape, ok := tc.tensors[info.GetName()]
				if !ok {
					t.Fatalf("unexpected tensor %v", info)
				}
				var elements uint64 = 1
				for _, d := range shape {
					elements *= d
				}
				if info.GetDtype() != tc.dtypes[info.GetName()] || info.GetElements() != elements || len(ck.Shapes[i]) != len(shape) {
					t.Fatalf("tensor %v shape %v, want dtype %s shape %v", info, ck.Shapes[i], tc.dtypes[info.GetName()], shape)
				}
				if info.GetDtype() != "" && info.GetBytes() == 0 {
					t.Fatalf("tensor %v has no bytes", info)
				}
			}
			for k, v := range tc.meta {
				if ck.Metadata[k] != v {
					t.Fatalf("metadata %s = %q, want %q (all %v)", k, ck.Metadata[k], v, ck.Metadata)
				}
			}
			if len(ck.Metadata) != len(tc.meta) {
				t.Fatalf("metadata %v, want %v", ck.Metadata, tc.meta)
			}
		})
	}
}

// What is not a zip, a zip without a pickle, and a pickle without a dict are refused
func TestReadZipErrors(t *testing.T) {
	root := new(pk).op(0x80, 2).mark().int(1).tuple().op('.').Bytes()
	for _, tc := range []struct {
		name string
		data []byte
		msg  string
	}{
		{"not a zip", []byte("plain"), "not a zip"},
		{"no pickle", checkpoint(t, root, "archive/other.pkl"), "no data.pkl"},
		{"root is a tuple", checkpoint(t, root, "archive/data.pkl"), "not a dict"},
		{"broken pickle", checkpoint(t, []byte{0x80, 2, 0x7f}, "archive/data.pkl"), "opcode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadZip(bytes.NewReader(tc.data), int64(len(tc.data)))
			if err == nil || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("error %v lacks %q", err, tc.msg)
			}
		})
	}
}
