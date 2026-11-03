package text

import (
	"encoding/json"
	"reflect"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Enum names lose their type prefix and case, multi word types included
func TestEnum(t *testing.T) {
	if got := Enum(v1.PoolKind_POOL_KIND_HOST); got != "host" {
		t.Fatalf("PoolKind host = %q", got)
	}
	if got := Enum(v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER); got != "layer" {
		t.Fatalf("TensorGroupKind layer = %q", got)
	}
	if got := Enum(v1.InstanceState_INSTANCE_STATE_UNSPECIFIED); got != "unspecified" {
		t.Fatalf("unspecified = %q", got)
	}
	for in, want := range map[string]string{"PoolKind": "POOL_KIND", "TensorGroupKind": "TENSOR_GROUP_KIND", "API": "API", "ApiFlavor": "API_FLAVOR"} {
		if got := snakeUpper(in); got != want {
			t.Fatalf("snakeUpper(%s) = %q, want %q", in, got, want)
		}
	}
}

// JSON flattens to dotted keys, scalar lists to comma lists with a length, object lists to indexed keys
func TestFlattenJSON(t *testing.T) {
	into := map[string]string{}
	err := FlattenJSON([]byte(`{"a": {"b": 1.50, "c": "x", "d": null}, "list": [1, 2, 3], "objs": [{"k": true}, {"k": false}], "big": 12345678901234567890, "mixed": [1, {"n": 2}]}`), "root", into)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"root.a.b": "1.50", "root.a.c": "x", "root.a.d": "",
		"root.list": "1,2,3", "root.list.length": "3",
		"root.objs.0.k": "true", "root.objs.1.k": "false", "root.objs": "", "root.objs.length": "2",
		"root.big":   "12345678901234567890",
		"root.mixed": "1", "root.mixed.length": "2", "root.mixed.1.n": "2",
	}
	if !reflect.DeepEqual(into, want) {
		t.Fatalf("flattened\n got %v\nwant %v", into, want)
	}
	if err := FlattenJSON([]byte(`{`), "", into); err == nil {
		t.Fatal("broken JSON must fail")
	}
	bare := map[string]string{}
	Flatten("", map[string]any{"k": "v"}, bare)
	if bare["k"] != "v" {
		t.Fatalf("no prefix %v", bare)
	}
	if v, err := DecodeJSON([]byte(`7`)); err != nil || v.(json.Number) != "7" {
		t.Fatalf("DecodeJSON kept %T %v %v", v, v, err)
	}
	for in, want := range map[any]string{nil: "", "s": "s", json.Number("2.5"): "2.5", 3: "3", true: "true"} {
		if got := Scalar(in); got != want {
			t.Fatalf("Scalar(%v) = %q", in, got)
		}
	}
}

// Every scalar kind becomes a float, strings through the parser, nothing else at all
func TestNumber(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want float64
		bad  bool
	}{
		{float64(1.5), 1.5, false}, {float32(2), 2, false}, {int(3), 3, false}, {int64(4), 4, false},
		{uint64(5), 5, false}, {uint32(6), 6, false}, {int32(7), 7, false}, {true, 1, false}, {false, 0, false},
		{"8", 8, false}, {"1,9,3", 9, false}, {"true", 1, false}, {"false", 0, false}, {" 2.5 ", 2.5, false},
		{nil, 0, true}, {"", 0, true}, {"x", 0, true}, {"1,x", 0, true}, {[]int{1}, 0, true},
	} {
		got, err := Number(tc.in)
		if (err != nil) != tc.bad || got != tc.want {
			t.Fatalf("Number(%v) = %v, %v", tc.in, got, err)
		}
	}
}

// Byte counts take an inline unit over the given one, commas and exponents read as numbers, unknown units fail
func TestBytes(t *testing.T) {
	for _, tc := range []struct {
		in, unit string
		want     uint64
		bad      bool
	}{
		{"5123.45 MiB", "", 5372326707, false},
		{"5 GiB", "MiB", 5 << 30, false},
		{"5", "GB", 5 << 30, false},
		{"1,024", "", 1024, false},
		{"2e3", "", 2000, false},
		{"3 KB", "", 3 << 10, false},
		{"4t", "", 4 << 40, false},
		{"7 bytes", "", 7, false},
		{"12", "", 12, false},
		{"abc", "", 0, true},
		{"5 parsecs", "", 0, true},
		{"", "", 0, true},
	} {
		got, err := Bytes(tc.in, tc.unit)
		if (err != nil) != tc.bad || got != tc.want {
			t.Fatalf("Bytes(%q, %q) = %d, %v", tc.in, tc.unit, got, err)
		}
	}
}

// Dotted versions compare numerically field by field, trailing words ignored
func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"580.65", "550", 1}, {"550", "580.65", -1}, {"1.2.3", "1.2.3", 0}, {"1.2", "1.2.0", 0},
		{"1.10", "1.9", 1}, {"2.0-1", "2.0", 1}, {"2.0-rc", "2.0", 0}, {"v12.4", "v12.3+cuda", 1}, {"", "1", -1}, {"x", "y", 0},
	} {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Fatalf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
