package runtime

import (
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestRenderAndMeasure(t *testing.T) {
	rt, err := registry(t).Get("llamacpp")
	if err != nil {
		t.Fatal(err)
	}
	params, err := rt.Params(map[string]string{"n_ctx": "4096", "n_gpu_layers": "12", "cache_type_k": "q8_0", "flash_attn": "on"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := rt.Render(RenderInput{
		Name:      "qwen",
		Params:    params,
		Artifacts: map[string]string{"weights": "/store/m.gguf", "weights_dir": "/store"},
		Host:      "127.0.0.1",
		Port:      4242,
		Install:   map[string]string{"path": "/opt/llama-server", "dir": "/opt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Command != "/opt/llama-server" {
		t.Fatalf("command %q", out.Command)
	}
	args := strings.Join(out.Args, " ")
	for _, want := range []string{"--model /store/m.gguf", "--host 127.0.0.1", "--port 4242", "--ctx-size 4096", "--n-gpu-layers 12", "--cache-type-k q8_0", "--flash-attn on", "--threads -1", "--alias qwen"} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q: %s", want, args)
		}
	}
	if strings.Contains(args, "--n-cpu-moe") || strings.Contains(args, "--mmproj") {
		t.Fatalf("unsolved and empty params must be omitted: %s", args)
	}
	if len(out.Env) != 0 {
		t.Fatalf("without a slot no device variable applies: %v", out.Env)
	}
	pinned, err := rt.Render(RenderInput{
		Name: "qwen", Params: params, Artifacts: map[string]string{"weights": "/store/m.gguf"}, Host: "127.0.0.1", Port: 1, Install: map[string]string{"path": "/opt/llama-server"},
		Devices: []map[string]any{
			{"id": "GPU-aaaa", "kind": "gpu", "vendor": "nvidia", "facts": map[string]string{"index": "1"}},
			{"id": "GPU-bbbb", "kind": "gpu", "vendor": "nvidia", "facts": map[string]string{"index": "3"}},
			{"id": "card2", "kind": "gpu", "vendor": "amd", "facts": map[string]string{"index": "2"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Env["CUDA_VISIBLE_DEVICES"] != "GPU-aaaa,GPU-bbbb" || pinned.Env["ROCR_VISIBLE_DEVICES"] != "2" || pinned.Env["GGML_VK_VISIBLE_DEVICES"] != "1,3,2" {
		t.Fatalf("slot devices should pin the process: %v", pinned.Env)
	}
	if out.Params["alias"] != "qwen" || out.Params["n_ctx"] != "4096" {
		t.Fatalf("rendered params should carry final values: %v", out.Params)
	}
	if _, ok := out.Params["mmproj"]; ok {
		t.Fatalf("omitted params must not be reported: %v", out.Params)
	}
	params["mmproj"] = "{{index .artifacts \"projector\"}}"
	out, err = rt.Render(RenderInput{Params: params, Artifacts: map[string]string{"weights": "w", "projector": "/store/mmproj.gguf"}, Install: map[string]string{"path": "x"}})
	if err != nil || !strings.Contains(strings.Join(out.Args, " "), "--mmproj /store/mmproj.gguf") {
		t.Fatalf("projector template: %v %v", out, err)
	}
	if rt.StopGrace().Seconds() != 15 {
		t.Fatalf("grace %v", rt.StopGrace())
	}
	ms := rt.Measure([]string{
		"load_tensors: CUDA0 model buffer size = 1024.00 MiB",
		"load_tensors: CUDA1 model buffer size = 512.00 MiB",
		"load_tensors: CPU_Mapped model buffer size = 100.00 MiB",
		"llama_kv_cache: CUDA0 KV buffer size = 64.00 MiB",
		"llama_context: CUDA0 compute buffer size = 32.00 MiB",
		"unrelated line",
	})
	got := map[string]uint64{}
	for _, m := range ms {
		got[m.GetKey()] = m.GetBytes()
	}
	if got["device.weights"] != 1536<<20 || got["host.weights"] != 100<<20 || got["device.cache"] != 64<<20 || got["device.compute"] != 32<<20 {
		t.Fatalf("measurements %v", got)
	}
}

func devicesManifest() *v1.RuntimeManifest {
	return &v1.RuntimeManifest{
		Id: "rt",
		Launch: &v1.Launch{
			Command: "{{.install.path}}",
			Args:    []string{"--port", "{{.port}}"},
			Env: map[string]string{
				"VISIBLE": `{{range $i, $d := .devices}}{{if $i}},{{end}}{{index $d.facts "index"}}{{end}}`,
				"ALWAYS":  "1",
			},
		},
	}
}

func TestRenderDevicesEnv(t *testing.T) {
	reg, err := New([]*v1.RuntimeManifest{devicesManifest()})
	if err != nil {
		t.Fatal(err)
	}
	rt, _ := reg.Get("rt")
	in := RenderInput{Name: "n", Params: map[string]any{}, Host: "127.0.0.1", Port: 1, Install: map[string]string{"path": "/bin/x"}}
	out, err := rt.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Env["VISIBLE"]; ok {
		t.Fatalf("empty env should be dropped: %v", out.Env)
	}
	if out.Env["ALWAYS"] != "1" {
		t.Fatal("non empty env kept")
	}
	in.Devices = []map[string]any{{"facts": map[string]any{"index": "2"}}, {"facts": map[string]any{"index": "0"}}}
	out, err = rt.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Env["VISIBLE"] != "2,0" {
		t.Fatalf("devices env %q", out.Env["VISIBLE"])
	}
}
