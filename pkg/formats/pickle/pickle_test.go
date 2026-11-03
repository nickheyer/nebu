package pickle

import (
	"bytes"
	"encoding/binary"
	"math"
	"reflect"
	"strings"
	"testing"
)

// Writes pickle opcodes by hand
type enc struct{ bytes.Buffer }

func (e *enc) op(b ...byte) *enc               { e.Write(b); return e }
func (e *enc) proto(v byte) *enc               { return e.op(0x80, v) }
func (e *enc) stop() []byte                    { e.op('.'); return e.Bytes() }
func (e *enc) mark() *enc                      { return e.op('(') }
func (e *enc) tuple() *enc                     { return e.op('t') }
func (e *enc) emptyDict() *enc                 { return e.op('}') }
func (e *enc) emptyList() *enc                 { return e.op(']') }
func (e *enc) setItems() *enc                  { return e.op('u') }
func (e *enc) setItem() *enc                   { return e.op('s') }
func (e *enc) appends() *enc                   { return e.op('e') }
func (e *enc) reduce() *enc                    { return e.op('R') }
func (e *enc) build() *enc                     { return e.op('b') }
func (e *enc) persid() *enc                    { return e.op('Q') }
func (e *enc) none() *enc                      { return e.op('N') }
func (e *enc) boolean(v bool) *enc             { return e.op(map[bool]byte{true: 0x88, false: 0x89}[v]) }
func (e *enc) binput(n byte) *enc              { return e.op('q', n) }
func (e *enc) binget(n byte) *enc              { return e.op('h', n) }
func (e *enc) memoize() *enc                   { return e.op(0x94) }
func (e *enc) global(module, name string) *enc { return e.op('c').str(module + "\n" + name + "\n") }
func (e *enc) str(s string) *enc               { e.WriteString(s); return e }
func (e *enc) binint(n int32) *enc             { e.op('J'); binary.Write(e, binary.LittleEndian, n); return e }
func (e *enc) binint1(n byte) *enc             { return e.op('K', n) }
func (e *enc) binint2(n uint16) *enc           { e.op('M'); binary.Write(e, binary.LittleEndian, n); return e }
func (e *enc) binfloat(f float64) *enc         { e.op('G'); binary.Write(e, binary.BigEndian, f); return e }
func (e *enc) shortBinUnicode(s string) *enc   { return e.op(0x8c, byte(len(s))).str(s) }
func (e *enc) shortBinString(s string) *enc    { return e.op('U', byte(len(s))).str(s) }
func (e *enc) shortBinBytes(s string) *enc     { return e.op('C', byte(len(s))).str(s) }
func (e *enc) long1(b ...byte) *enc            { return e.op(append([]byte{0x8a, byte(len(b))}, b...)...) }
func (e *enc) tuple1() *enc                    { return e.op(0x85) }
func (e *enc) tuple2() *enc                    { return e.op(0x86) }
func (e *enc) emptyTuple() *enc                { return e.op(')') }
func (e *enc) newobj() *enc                    { return e.op(0x81) }
func (e *enc) stackGlobal() *enc               { return e.op(0x93) }
func (e *enc) frame(n uint64) *enc             { e.op(0x95); binary.Write(e, binary.LittleEndian, n); return e }
func (e *enc) binunicode(s string) *enc {
	e.op('X')
	binary.Write(e, binary.LittleEndian, uint32(len(s)))
	return e.str(s)
}
func (e *enc) textInt(s string) *enc    { return e.op('I').str(s + "\n") }
func (e *enc) textLong(s string) *enc   { return e.op('L').str(s + "\n") }
func (e *enc) textFloat(s string) *enc  { return e.op('F').str(s + "\n") }
func (e *enc) textString(s string) *enc { return e.op('S').str("'" + s + "'\n") }
func (e *enc) textPut(n string) *enc    { return e.op('p').str(n + "\n") }
func (e *enc) textGet(n string) *enc    { return e.op('g').str(n + "\n") }
func (e *enc) dup() *enc                { return e.op('2') }
func (e *enc) pop() *enc                { return e.op('0') }
func (e *enc) popMark() *enc            { return e.op('1') }
func (e *enc) list() *enc               { return e.op('l') }
func (e *enc) dict() *enc               { return e.op('d') }
func (e *enc) appendOne() *enc          { return e.op('a') }
func (e *enc) emptySet() *enc           { return e.op(0x8f) }
func (e *enc) addItems() *enc           { return e.op(0x90) }
func (e *enc) frozenset() *enc          { return e.op(0x91) }
func (e *enc) newobjEx() *enc           { return e.op(0x92) }
func (e *enc) longBinput(n uint32) *enc { e.op('r'); binary.Write(e, binary.LittleEndian, n); return e }
func (e *enc) longBinget(n uint32) *enc { e.op('j'); binary.Write(e, binary.LittleEndian, n); return e }
func (e *enc) textPersid(s string) *enc { return e.op('P').str(s + "\n") }
func (e *enc) binbytes(s string) *enc {
	e.op('B')
	binary.Write(e, binary.LittleEndian, uint32(len(s)))
	return e.str(s)
}
func (e *enc) binstring(s string) *enc {
	e.op('T')
	binary.Write(e, binary.LittleEndian, uint32(len(s)))
	return e.str(s)
}
func (e *enc) binunicode8(s string) *enc {
	e.op(0x8d)
	binary.Write(e, binary.LittleEndian, uint64(len(s)))
	return e.str(s)
}
func (e *enc) long4(b ...byte) *enc {
	e.op(0x8b)
	binary.Write(e, binary.LittleEndian, uint32(len(b)))
	return e.op(b...)
}
func (e *enc) textUnicode(s string) *enc { return e.op('V').str(s + "\n") }
func (e *enc) bytearray8(s string) *enc {
	e.op(0x96)
	binary.Write(e, binary.LittleEndian, uint64(len(s)))
	return e.str(s)
}
func (e *enc) binbytes8(s string) *enc {
	e.op(0x8e)
	binary.Write(e, binary.LittleEndian, uint64(len(s)))
	return e.str(s)
}
func (e *enc) tuple3() *enc { return e.op(0x87) }
func decode(t *testing.T, data []byte) any {
	t.Helper()
	v, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Every scalar, container and memo opcode decodes to the Go value it names
func TestDecodeValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want any
	}{
		{"none", new(enc).proto(2).none().stop(), nil},
		{"true", new(enc).proto(2).boolean(true).stop(), true},
		{"false", new(enc).proto(2).boolean(false).stop(), false},
		{"binint", new(enc).proto(2).binint(-7).stop(), int64(-7)},
		{"binint1", new(enc).proto(2).binint1(200).stop(), int64(200)},
		{"binint2", new(enc).proto(2).binint2(60000).stop(), int64(60000)},
		{"long1", new(enc).proto(2).long1(0x39, 0x30).stop(), int64(12345)},
		{"long1 negative", new(enc).proto(2).long1(0xff).stop(), int64(-1)},
		{"long1 empty", new(enc).proto(2).long1().stop(), int64(0)},
		{"long4", new(enc).proto(2).long4(1, 0, 0, 0, 0, 0, 0, 0, 1).stop(), math.Pow(2, 64) + 1},
		{"text int", new(enc).proto(2).textInt("42").stop(), int64(42)},
		{"text int true", new(enc).proto(2).textInt("01").stop(), true},
		{"text int false", new(enc).proto(2).textInt("00").stop(), false},
		{"text long", new(enc).proto(2).textLong("99L").stop(), int64(99)},
		{"binfloat", new(enc).proto(2).binfloat(2.5).stop(), 2.5},
		{"text float", new(enc).proto(2).textFloat("1.25").stop(), 1.25},
		{"short binunicode", new(enc).proto(4).shortBinUnicode("hi").stop(), "hi"},
		{"short binstring", new(enc).proto(2).shortBinString("hey").stop(), "hey"},
		{"binunicode", new(enc).proto(2).binunicode("héllo").stop(), "héllo"},
		{"binunicode8", new(enc).proto(4).binunicode8("eight").stop(), "eight"},
		{"binstring", new(enc).proto(2).binstring("bs").stop(), "bs"},
		{"text string", new(enc).proto(2).textString("quoted").stop(), "quoted"},
		{"text string with a quote", new(enc).proto(2).textString("it\\'s \"x\"").stop(), "it's \"x\""},
		{"text string unquoted", new(enc).proto(2).op('S').str("bare\n").stop(), "bare"},
		{"text unicode", new(enc).proto(2).textUnicode("uni").stop(), "uni"},
		{"short binbytes", new(enc).proto(3).shortBinBytes("ab").stop(), []byte("ab")},
		{"binbytes", new(enc).proto(3).binbytes("abc").stop(), []byte("abc")},
		{"binbytes8", new(enc).proto(4).binbytes8("abcd").stop(), []byte("abcd")},
		{"bytearray8", new(enc).proto(5).bytearray8("ba").stop(), []byte("ba")},
		{"empty tuple", new(enc).proto(2).emptyTuple().stop(), tuple{}},
		{"tuple", new(enc).proto(2).mark().binint1(1).binint1(2).tuple().stop(), tuple{int64(1), int64(2)}},
		{"tuple1", new(enc).proto(2).binint1(1).tuple1().stop(), tuple{int64(1)}},
		{"tuple2", new(enc).proto(2).binint1(1).binint1(2).tuple2().stop(), tuple{int64(1), int64(2)}},
		{"tuple3", new(enc).proto(2).binint1(1).binint1(2).binint1(3).tuple3().stop(), tuple{int64(1), int64(2), int64(3)}},
		{"list appends", new(enc).proto(2).emptyList().mark().binint1(1).none().appends().stop(), &list{Items: []any{int64(1), nil}}},
		{"list append", new(enc).proto(2).emptyList().binint1(9).appendOne().stop(), &list{Items: []any{int64(9)}}},
		{"list from mark", new(enc).proto(2).mark().binint1(1).list().stop(), &list{Items: []any{int64(1)}}},
		{"dict setitems", new(enc).proto(2).emptyDict().mark().shortBinString("a").binint1(1).shortBinString("b").boolean(true).setItems().stop(), &Dict{Pairs: []Pair{{"a", int64(1)}, {"b", true}}}},
		{"dict setitem", new(enc).proto(2).emptyDict().shortBinString("k").binfloat(1).setItem().stop(), &Dict{Pairs: []Pair{{"k", 1.0}}}},
		{"dict from mark", new(enc).proto(2).mark().shortBinString("k").binint1(2).dict().stop(), &Dict{Pairs: []Pair{{"k", int64(2)}}}},
		{"dict overwrites a key", new(enc).proto(2).emptyDict().shortBinString("k").binint1(1).setItem().shortBinString("k").binint1(2).setItem().stop(), &Dict{Pairs: []Pair{{"k", int64(2)}}}},
		{"set", new(enc).proto(4).emptySet().mark().binint1(3).addItems().stop(), &list{Items: []any{int64(3)}}},
		{"frozenset", new(enc).proto(4).mark().binint1(4).frozenset().stop(), tuple{int64(4)}},
		{"memo binput binget", new(enc).proto(2).shortBinString("m").binput(0).pop().binget(0).stop(), "m"},
		{"memo long", new(enc).proto(2).shortBinString("m").longBinput(7).pop().longBinget(7).stop(), "m"},
		{"memo text", new(enc).proto(2).shortBinString("m").textPut("3").pop().textGet("3").stop(), "m"},
		{"memoize", new(enc).proto(4).shortBinUnicode("z").memoize().pop().binget(0).stop(), "z"},
		{"dup", new(enc).proto(2).binint1(5).dup().tuple2().stop(), tuple{int64(5), int64(5)}},
		{"pop mark", new(enc).proto(2).binint1(1).mark().binint1(2).popMark().stop(), int64(1)},
		{"frame", new(enc).proto(4).frame(3).binint1(1).stop(), int64(1)},
		{"global", new(enc).proto(2).global("torch", "FloatStorage").stop(), Global{Module: "torch", Name: "FloatStorage"}},
		{"stack global", new(enc).proto(4).shortBinUnicode("torch").shortBinUnicode("Size").stackGlobal().stop(), Global{Module: "torch", Name: "Size"}},
		{"persistent id", new(enc).proto(2).mark().shortBinString("storage").global("torch", "HalfStorage").tuple().persid().stop(), Persistent{ID: tuple{"storage", Global{Module: "torch", Name: "HalfStorage"}}}},
		{"text persistent id", new(enc).proto(2).textPersid("pid").stop(), Persistent{ID: "pid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decode(t, tc.data)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decoded %#v, want %#v", got, tc.want)
			}
		})
	}
}

// Callables are recorded as objects with their arguments and state, the few standard ones built instead
func TestDecodeCallables(t *testing.T) {
	size := func(e *enc) *enc {
		return e.global("torch", "Size").mark().binint1(2).binint1(3).tuple().tuple1().reduce()
	}
	for _, tc := range []struct {
		name string
		data []byte
		want any
	}{
		{"reduce records the call", new(enc).proto(2).global("torch._utils", "_rebuild_tensor_v2").mark().binint1(0).none().tuple().reduce().stop(),
			&Object{Class: Global{Module: "torch._utils", Name: "_rebuild_tensor_v2"}, Args: []any{int64(0), nil}}},
		{"reduce with no args", new(enc).proto(2).global("m", "f").none().reduce().stop(), &Object{Class: Global{Module: "m", Name: "f"}}},
		{"reduce with one bare arg", new(enc).proto(2).global("m", "f").binint1(4).reduce().stop(), &Object{Class: Global{Module: "m", Name: "f"}, Args: []any{int64(4)}}},
		{"newobj", new(enc).proto(2).global("m", "C").emptyTuple().newobj().stop(), &Object{Class: Global{Module: "m", Name: "C"}, Args: []any{}}},
		{"newobj ex", new(enc).proto(4).global("m", "C").emptyTuple().emptyDict().newobjEx().stop(), &Object{Class: Global{Module: "m", Name: "C"}, Args: []any{}}},
		{"build sets the state", new(enc).proto(2).global("m", "C").emptyTuple().newobj().emptyDict().shortBinString("k").binint1(1).setItem().build().stop(),
			&Object{Class: Global{Module: "m", Name: "C"}, Args: []any{}, State: &Dict{Pairs: []Pair{{"k", int64(1)}}}}},
		{"build merges into a dict", new(enc).proto(2).emptyDict().emptyDict().shortBinString("k").binint1(1).setItem().build().stop(), &Dict{Pairs: []Pair{{"k", int64(1)}}}},
		{"ordered dict", new(enc).proto(2).global("collections", "OrderedDict").emptyTuple().reduce().stop(), &Dict{}},
		{"dict builtin", new(enc).proto(2).global("builtins", "dict").emptyTuple().reduce().stop(), &Dict{}},
		{"torch size", size(new(enc).proto(2)).stop(), tuple{int64(2), int64(3)}},
		{"torch size of a list", new(enc).proto(2).global("torch", "Size").emptyList().mark().binint1(4).appends().tuple1().reduce().stop(), tuple{int64(4)}},
		{"torch size without args", new(enc).proto(2).global("torch", "Size").emptyTuple().reduce().stop(), tuple{}},
		{"list of a tuple", new(enc).proto(2).global("builtins", "list").mark().binint1(1).tuple().tuple1().reduce().stop(), &list{Items: []any{int64(1)}}},
		{"set of a list", new(enc).proto(2).global("builtins", "set").emptyList().mark().binint1(1).appends().tuple1().reduce().stop(), &list{Items: []any{int64(1)}}},
		{"list without args", new(enc).proto(2).global("builtins", "list").emptyTuple().reduce().stop(), &list{}},
		{"tuple of a list", new(enc).proto(2).global("builtins", "tuple").emptyList().mark().binint1(1).appends().tuple1().reduce().stop(), tuple{int64(1)}},
		{"tuple without args", new(enc).proto(2).global("builtins", "tuple").emptyTuple().reduce().stop(), tuple{}},
		{"codecs encode", new(enc).proto(2).global("_codecs", "encode").shortBinString("raw").shortBinString("latin1").tuple2().reduce().stop(), []byte("raw")},
		{"getattr names an attribute", new(enc).proto(2).global("builtins", "getattr").global("torch", "nn").shortBinString("Linear").tuple2().reduce().stop(), Global{Module: "torch.nn", Name: "Linear"}},
		{"reduce through an object", new(enc).proto(2).global("m", "C").emptyTuple().newobj().binint1(1).tuple1().reduce().stop(), &Object{Class: Global{Module: "m", Name: "C"}, Args: []any{int64(1)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decode(t, tc.data)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decoded %#v, want %#v", got, tc.want)
			}
		})
	}
}

// A checkpoint shaped stream decodes with its tensor rebuild call, storage reference and memo references intact
func TestDecodeCheckpointShape(t *testing.T) {
	e := new(enc).proto(2).emptyDict().binput(0).mark().
		shortBinString("weight").
		global("torch._utils", "_rebuild_tensor_v2").mark().
		mark().shortBinString("storage").global("torch", "FloatStorage").shortBinString("0").shortBinString("cpu").binint1(6).tuple().binput(1).persid().
		binint1(0).
		global("torch", "Size").mark().binint1(2).binint1(3).tuple().tuple1().reduce().
		mark().binint1(3).binint1(1).tuple().
		boolean(false).
		global("collections", "OrderedDict").emptyTuple().reduce().
		tuple().reduce().
		shortBinString("again").binget(1).
		setItems()
	root := decode(t, e.stop())
	dict, ok := root.(*Dict)
	if !ok || len(dict.Pairs) != 2 {
		t.Fatalf("root %#v", root)
	}
	w, _ := dict.Get("weight")
	obj, ok := w.(*Object)
	if !ok || obj.Class.Name != "_rebuild_tensor_v2" || len(obj.Args) != 6 {
		t.Fatalf("weight %#v", w)
	}
	storage, ok := obj.Args[0].(Persistent)
	if !ok {
		t.Fatalf("storage %#v", obj.Args[0])
	}
	id, _ := Items(storage.ID)
	if len(id) != 5 || id[1] != (Global{Module: "torch", Name: "FloatStorage"}) {
		t.Fatalf("storage id %#v", id)
	}
	if size, _ := Items(obj.Args[2]); len(size) != 2 {
		t.Fatalf("size %#v", obj.Args[2])
	}
	if again, _ := dict.Get("again"); !reflect.DeepEqual(again, storage.ID) {
		t.Fatalf("memo reference %#v", again)
	}
	if _, ok := dict.Get("missing"); ok {
		t.Fatal("missing key found")
	}
	if _, ok := (*Dict)(nil).Get("k"); ok {
		t.Fatal("nil dict found a key")
	}
}

// Truncated, unknown and ill formed streams fail instead of returning something
func TestDecodeErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		msg  string
	}{
		{"empty", nil, "EOF"},
		{"no stop", new(enc).proto(2).binint1(1).Bytes(), "EOF"},
		{"unknown opcode", []byte{0x80, 2, 0x7f, '.'}, "opcode"},
		{"truncated string", []byte{0x80, 2, 'U', 9, 'a', '.'}, ""},
		{"stack global needs strings", new(enc).proto(4).binint1(1).binint1(2).stackGlobal().stop(), "STACK_GLOBAL"},
		{"reduce on a number", new(enc).proto(2).binint1(1).emptyTuple().reduce().stop(), "apply"},
		{"pop empty stack", new(enc).proto(2).pop().stop(), ""},
		{"tuple without mark", new(enc).proto(2).binint1(1).tuple().stop(), ""},
		{"setitems on a list", new(enc).proto(2).emptyList().mark().binint1(1).binint1(2).setItems().stop(), ""},
		{"get unknown memo", new(enc).proto(2).binget(9).stop(), ""},
		{"bad text int", new(enc).proto(2).textInt("x").stop(), ""},
		{"bad text float", new(enc).proto(2).textFloat("x").stop(), ""},
		{"bad text put", new(enc).proto(2).binint1(1).textPut("x").stop(), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(bytes.NewReader(tc.data))
			if err == nil {
				t.Fatal("decoded")
			}
			if tc.msg != "" && !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("error %v lacks %q", err, tc.msg)
			}
		})
	}
}

// Integers come out of ints, whole floats and booleans, items out of tuples and lists alone
func TestIntAndItems(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want int64
		ok   bool
	}{
		{int64(3), 3, true}, {float64(4), 4, true}, {float64(4.5), 0, false}, {true, 1, true}, {false, 0, true}, {"3", 0, false}, {nil, 0, false}, {math.Pow(2, 63), 0, false},
	} {
		if got, ok := Int(tc.in); got != tc.want || ok != tc.ok {
			t.Fatalf("Int(%v) = %d, %v", tc.in, got, ok)
		}
	}
	if items, ok := Items(tuple{1, 2}); !ok || len(items) != 2 {
		t.Fatalf("tuple items %v %v", items, ok)
	}
	if items, ok := Items(&list{Items: []any{1}}); !ok || len(items) != 1 {
		t.Fatalf("list items %v %v", items, ok)
	}
	if _, ok := Items("no"); ok {
		t.Fatal("a string has no items")
	}
	if (Global{Module: "a.b", Name: "c"}).String() != "a.b.c" {
		t.Fatal("global name")
	}
}
