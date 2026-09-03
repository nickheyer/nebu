package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/spec"
)

func registry(t *testing.T) *Registry {
	t.Helper()
	c, err := spec.Load(os.DirFS(filepath.Join("..", "..", "spec")))
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(c.Runtimes)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParams(t *testing.T) {
	rt, err := registry(t).Get("llamacpp")
	if err != nil {
		t.Fatal(err)
	}
	if !rt.Accepts("gguf") || rt.Accepts("safetensors") || rt.Policy == nil {
		t.Fatal("manifest basics")
	}
	p, err := rt.Params(map[string]string{"n_ctx": "4096", "cache_type_k": "q8_0"})
	if err != nil {
		t.Fatal(err)
	}
	if p["n_ctx"] != int64(4096) || p["n_gpu_layers"] != Auto || p["cache_type_k"] != "q8_0" || p["threads"] != int64(-1) {
		t.Fatalf("params %v", p)
	}
	if _, err := rt.Params(map[string]string{"bogus": "1"}); !errors.Is(err, ErrParam) {
		t.Fatalf("unknown param: %v", err)
	}
	if _, err := rt.Params(map[string]string{"cache_type_k": "q3_k"}); !errors.Is(err, ErrParam) {
		t.Fatalf("bad choice: %v", err)
	}
	if _, err := rt.Params(map[string]string{"n_ctx": "lots"}); !errors.Is(err, ErrParam) {
		t.Fatalf("bad int: %v", err)
	}
	p, err = rt.Params(map[string]string{"n_gpu_layers": "12"})
	if err != nil || p["n_gpu_layers"] != int64(12) {
		t.Fatalf("solved override %v %v", p, err)
	}
	if _, err := registry(t).Get("nope"); !errors.Is(err, ErrUnknownRuntime) {
		t.Fatal("unknown runtime")
	}
}

func TestCompatible(t *testing.T) {
	rt, _ := registry(t).Get("llamacpp")
	ok, unmet := rt.Compatible(&v1.HostProfile{})
	if ok || len(unmet) != 1 {
		t.Fatalf("empty host should be incompatible: %v", unmet)
	}
	ok, _ = rt.Compatible(&v1.HostProfile{Devices: []*v1.Device{{Kind: v1.DeviceKind_DEVICE_KIND_CPU}}})
	if !ok {
		t.Fatal("cpu should satisfy the device constraint")
	}
	vllm, _ := registry(t).Get("vllm")
	if ok, _ := vllm.Compatible(&v1.HostProfile{Devices: []*v1.Device{{Kind: v1.DeviceKind_DEVICE_KIND_CPU}}}); ok {
		t.Fatal("cpu only should not satisfy gpu constraint")
	}
	st := vllm.Status(&v1.HostProfile{Devices: []*v1.Device{{Kind: v1.DeviceKind_DEVICE_KIND_GPU}}})
	if !st.GetCompatible() || st.GetManifest().GetId() != "vllm" {
		t.Fatalf("status %+v", st)
	}
}

func TestBadManifest(t *testing.T) {
	_, err := New([]*v1.RuntimeManifest{{Id: "x", Constraints: []*v1.Constraint{{Expr: "1 +"}}}})
	if err == nil {
		t.Fatal("bad constraint should fail")
	}
	_, err = New([]*v1.RuntimeManifest{{Id: "x", Params: []*v1.Param{{Name: "n", Type: v1.ParamType_PARAM_TYPE_INT, Default: "abc"}}}})
	if err == nil {
		t.Fatal("bad default should fail")
	}
}
