package eval

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"580.65.06", "550", 1},
		{"12.8", "12.8", 0},
		{"12.4", "12.8", -1},
		{"b6000", "b5999", 1},
		{"", "1", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseNumber(t *testing.T) {
	cases := map[string]float64{"12": 12, "1,5,3": 5, "true": 1, "false": 0, " 2.5 ": 2.5}
	for in, want := range cases {
		got, err := ParseNumber(in)
		if err != nil || got != want {
			t.Errorf("ParseNumber(%q)=%v,%v want %v", in, got, err, want)
		}
	}
	if _, err := ParseNumber(""); err == nil {
		t.Error("empty should fail")
	}
	if _, err := ParseNumber("abc"); err == nil {
		t.Error("text should fail")
	}
}

func TestExpr(t *testing.T) {
	e, err := Compile(`vercmp(facts.driver, "550") >= 0 && any(devices, .vendor == "x") && 2 * MiB == 2097152`)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]any{
		"facts":   map[string]any{"driver": "580.65.06"},
		"devices": []any{map[string]any{"vendor": "x"}},
	}
	ok, err := e.Bool(env)
	if err != nil || !ok {
		t.Fatalf("Bool=%v,%v", ok, err)
	}
	f, err := Compile("n_ctx * cache_per_token * cache_bytes[cache_type] / 2")
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Float(map[string]any{"n_ctx": int64(8), "cache_per_token": 4.0, "cache_bytes": map[string]any{"f16": 2.0}, "cache_type": "f16"})
	if err != nil || got != 32 {
		t.Fatalf("Float=%v,%v", got, err)
	}
	if _, err := Compile("1 +"); err == nil {
		t.Error("syntax error expected")
	}
	if _, err := f.Float(map[string]any{}); err == nil {
		t.Error("missing variables should error")
	}
}

func TestTemplate(t *testing.T) {
	tpl, err := CompileTemplate(`{{.name}}-{{index . "opt"}}`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tpl.Render(map[string]string{"name": "a"})
	if err != nil || out != "a-" {
		t.Fatalf("Render=%q,%v", out, err)
	}
	if _, err := tpl.Render(map[string]string{}); err == nil {
		t.Error("missing key should error")
	}
}

func TestFlatten(t *testing.T) {
	var root any
	dec := json.NewDecoder(strings.NewReader(`{"a":{"b":1,"c":[1,2]},"d":[{"e":"x"}],"f":true,"g":null}`))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	Flatten("", root, out, nil)
	want := map[string]string{"a.b": "1", "a.c": "1,2", "a.c.length": "2", "d.0.e": "x", "d": "", "d.length": "1", "f": "true", "g": ""}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s=%q want %q", k, out[k], v)
		}
	}
}

func TestEnumShort(t *testing.T) {
	if got := EnumShort(v1.DeviceKind_DEVICE_KIND_GPU); got != "gpu" {
		t.Errorf("got %q", got)
	}
	if got := EnumShort(v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS); got != "experts" {
		t.Errorf("got %q", got)
	}
}

func TestSolve(t *testing.T) {
	compile := func(src string) *Expr {
		e, err := Compile(src)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	// b uses a and comes first alphabetically, so the second pass has to pick it up
	env := map[string]any{"x": 2.0}
	if err := Solve(map[string]*Expr{"b": compile("a * 3"), "a": compile("x + 1")}, env); err != nil || env["b"] != 9.0 {
		t.Fatalf("solve %v %v", env, err)
	}
	err := Solve(map[string]*Expr{"c": compile("missing + 1")}, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "formula c") {
		t.Fatalf("unresolved formula should name itself, got %v", err)
	}
}

func TestMissingNamesWhatTheScopeLacks(t *testing.T) {
	e, err := Compile("n_layer / (attn_interval ?? 1) * n_head_kv * (head_dim + head_dim_v) + KiB + vercmp(version, \"1\") + min(n_layer, 2)")
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]any{"n_layer": 2.0, "head_dim": 64.0, "head_dim_v": nil}
	got := e.Missing(env)
	want := []string{"head_dim_v", "n_head_kv", "version"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("missing %v want %v", got, want)
	}
	_, err = e.Float(env)
	var missing *MissingError
	if !errors.As(Explain(e, "cache_per_token", env, err), &missing) || missing.Formula != "cache_per_token" {
		t.Fatalf("explain %v", err)
	}
	full := map[string]any{"n_layer": 2.0, "head_dim": 64.0, "head_dim_v": 64.0, "n_head_kv": 4.0, "version": "2"}
	if len(e.Missing(full)) != 0 {
		t.Fatalf("nothing should be missing: %v", e.Missing(full))
	}
	if err := Solve(map[string]*Expr{"cache_per_token": e}, map[string]any{"n_layer": 2.0}); !errors.As(err, &missing) {
		t.Fatalf("solve should name the missing values: %v", err)
	}
}
