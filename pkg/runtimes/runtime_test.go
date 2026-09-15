package runtimes

import (
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/triage"
)

func registry(t *testing.T) *Registry {
	t.Helper()
	r, err := New(All())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Every shipped runtime describes itself, and every param the planner solves says in words what auto means
func TestShippedRuntimesAreComplete(t *testing.T) {
	r := registry(t)
	if len(r.List()) != 4 {
		t.Fatalf("runtimes %d", len(r.List()))
	}
	for _, rt := range r.List() {
		if rt.Name() == "" || rt.Description() == "" || len(rt.Formats()) == 0 || len(rt.Methods()) == 0 || rt.Policy() == nil || len(rt.Triage()) == 0 || rt.Health().Path == "" {
			t.Fatalf("%s is missing something it describes", rt.ID())
		}
		if strings.Contains(strings.ToLower(rt.Description()), "launch_server") || strings.Contains(rt.Description(), "{{") {
			t.Fatalf("%s describes launch mechanics: %s", rt.ID(), rt.Description())
		}
		for _, p := range rt.Params() {
			if p.GetLabel() == "" || p.GetDescription() == "" {
				t.Fatalf("%s param %s needs a label and a description", rt.ID(), p.GetName())
			}
			if p.GetSolved() && (p.GetRule() == "" || !strings.EqualFold(p.GetDefault(), Auto)) {
				t.Fatalf("%s solved param %s needs a rule and an auto default", rt.ID(), p.GetName())
			}
		}
		d := Describe(rt)
		if d.GetId() != rt.ID() || len(d.GetMethods()) != len(rt.Methods()) || len(d.GetParams()) != len(rt.Params()) {
			t.Fatalf("describe %v", d)
		}
	}
	if r.API("llamacpp") != v1.ApiFlavor_API_FLAVOR_OPENAI || r.API("nope") != v1.ApiFlavor_API_FLAVOR_OPENAI {
		t.Fatal("api")
	}
	if _, err := r.Get("nope"); err == nil {
		t.Fatal("unknown runtime")
	}
}

// A runtime shaped by a test, so the registry's checks can be exercised
type shaped struct {
	LlamaCpp
	id      string
	params  []*v1.Param
	methods []Method
}

func (s shaped) ID() string          { return s.id }
func (s shaped) Params() []*v1.Param { return s.params }
func (s shaped) Methods() []Method   { return s.methods }
func (shaped) Policy() *estimate.Policy {
	return nil
}
func (shaped) Triage() []triage.Set { return nil }

func TestRegistryRefusesBrokenRuntimes(t *testing.T) {
	adopt := []Method{{ID: "adopt", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Binaries: []string{"x"}}}
	cases := map[string]shaped{
		"no id":                 {params: nil, methods: adopt},
		"duplicate param":       {id: "a", params: []*v1.Param{{Name: "p"}, {Name: "p"}}, methods: adopt},
		"solved without rule":   {id: "a", params: []*v1.Param{{Name: "p", Type: v1.ParamType_PARAM_TYPE_INT, Default: Auto, Solved: true}}, methods: adopt},
		"solved not auto":       {id: "a", params: []*v1.Param{{Name: "p", Type: v1.ParamType_PARAM_TYPE_INT, Default: "5", Solved: true, Rule: "r"}}, methods: adopt},
		"rule on plain param":   {id: "a", params: []*v1.Param{{Name: "p", Type: v1.ParamType_PARAM_TYPE_INT, Default: "5", Rule: "r"}}, methods: adopt},
		"bad default":           {id: "a", params: []*v1.Param{{Name: "p", Type: v1.ParamType_PARAM_TYPE_INT, Default: "x"}}, methods: adopt},
		"bounds on string":      {id: "a", params: []*v1.Param{{Name: "p", Type: v1.ParamType_PARAM_TYPE_STRING, Min: 1}}, methods: adopt},
		"min above max":         {id: "a", params: []*v1.Param{{Name: "p", Type: v1.ParamType_PARAM_TYPE_INT, Min: 5, Max: 1}}, methods: adopt},
		"adopt without binary":  {id: "a", methods: []Method{{ID: "adopt", Kind: v1.InstallKind_INSTALL_KIND_ADOPTED}}},
		"prebuilt without rule": {id: "a", methods: []Method{{ID: "rel", Kind: v1.InstallKind_INSTALL_KIND_PREBUILT, Releases: "o/r"}}},
		"built without recipe":  {id: "a", methods: []Method{{ID: "src", Kind: v1.InstallKind_INSTALL_KIND_BUILT}}},
		"method without kind":   {id: "a", methods: []Method{{ID: "m"}}},
	}
	for name, rt := range cases {
		if _, err := New([]Runtime{rt}); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	if _, err := New([]Runtime{shaped{id: "a", methods: adopt}, shaped{id: "a", methods: adopt}}); err == nil {
		t.Fatal("duplicate id should be refused")
	}
}

func TestResolveAndFlags(t *testing.T) {
	rt := LlamaCpp{}
	params, err := Resolve(rt, map[string]string{"n_ctx": "4096", "flash_attn": "on", "n_batch": "64"})
	if err != nil {
		t.Fatal(err)
	}
	if params.Int("n_ctx") != 4096 || params.Str("flash_attn") != "on" || !params.IsAuto("n_gpu_layers") || params.Int("n_batch") != 64 || params.Int("log_verbosity") != 4 {
		t.Fatalf("params %v", params)
	}
	for _, bad := range []map[string]string{{"nope": "1"}, {"n_ctx": "many"}, {"flash_attn": "maybe"}, {"cache_type_k": "q9"}} {
		if _, err := Resolve(rt, bad); err == nil {
			t.Errorf("%v should be refused", bad)
		}
	}
	args, env, emitted := Flags(rt.Params(), params)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--ctx-size 4096") || !strings.Contains(joined, "--flash-attn on") || strings.Contains(joined, "--n-gpu-layers") || strings.Contains(joined, "--alias") {
		t.Fatalf("args %v", args)
	}
	if len(env) != 0 || emitted["n_ctx"] != "4096" || emitted["n_gpu_layers"] != "" {
		t.Fatalf("env %v emitted %v", env, emitted)
	}
	cmd, err := rt.Launch(Launch{Name: "m", Params: params, Artifacts: map[string]string{"weights": "/w.gguf", "projector": "/p.gguf"}, Host: "127.0.0.1", Port: 9, Install: Install{Path: "/bin/llama-server"}, Devices: []*v1.Device{{Id: "GPU-1", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia", Facts: map[string]string{"index": "0"}}}})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd.Args, " ")
	if cmd.Command != "/bin/llama-server" || !strings.HasPrefix(line, "--model /w.gguf --host 127.0.0.1 --port 9") || !strings.Contains(line, "--alias m") || !strings.Contains(line, "--mmproj /p.gguf") {
		t.Fatalf("launch %v", cmd)
	}
	if cmd.Env["CUDA_VISIBLE_DEVICES"] != "GPU-1" || cmd.Env["GGML_VK_VISIBLE_DEVICES"] != "0" || cmd.Env["ROCR_VISIBLE_DEVICES"] != "" {
		t.Fatalf("env %v", cmd.Env)
	}
	if _, err := rt.Launch(Launch{Params: params}); err == nil {
		t.Fatal("a launch without weights should fail")
	}
}

// The facts a rule reads come from the devices the probes found, never from a host fact repeating them
func TestDeviceFactsFeedRules(t *testing.T) {
	h := &v1.HostProfile{Os: "windows", Arch: "amd64", Devices: []*v1.Device{
		{Id: "c0", Kind: v1.DeviceKind_DEVICE_KIND_CPU, Facts: map[string]string{"threads": "16"}},
		{Id: "c1", Kind: v1.DeviceKind_DEVICE_KIND_CPU, Facts: map[string]string{"threads": "16"}},
		{Id: "g", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia", Facts: map[string]string{"driver_version": "580.65"}},
	}}
	if cpuThreads(h) != 32 || driverVersion(h, "nvidia") != "580.65" || driverVersion(h, "amd") != "" {
		t.Fatal("device facts")
	}
	release, err := MethodOf(LlamaCpp{}, "release")
	if err != nil {
		t.Fatal(err)
	}
	ids := func(rules []PrebuiltRule) string {
		var out []string
		for _, r := range rules {
			out = append(out, r.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(release.HostRules(h)); got != "windows-cuda13,windows-cuda12,windows-vulkan,windows-cpu" {
		t.Fatalf("a 580 driver takes the cuda 13 build first: %s", got)
	}
	h.Devices[2].Facts["driver_version"] = "550.1"
	if got := ids(release.HostRules(h)); got != "windows-cuda12,windows-vulkan,windows-cpu" {
		t.Fatalf("an older driver skips cuda 13: %s", got)
	}
	if _, err := release.Rule("nope"); err == nil {
		t.Fatal("unknown rule")
	}
	if _, err := MethodOf(LlamaCpp{}, "nope"); err == nil {
		t.Fatal("unknown method")
	}
	if !Accepts(LlamaCpp{}, "gguf") || Accepts(LlamaCpp{}, "safetensors") {
		t.Fatal("accepts")
	}
	if ok, unmet := Compatible(LlamaCpp{}, &v1.HostProfile{}); ok || len(unmet) != 1 {
		t.Fatal("a host without devices is unmet")
	}
	if ids := RecipeIDs(LlamaCpp{}); len(ids) != 1 || ids[0] != "llamacpp" {
		t.Fatalf("recipes %v", ids)
	}
}

func TestMeasureAndProbes(t *testing.T) {
	lines := []string{
		"load_tensors: CUDA0 model buffer size = 5000.00 MiB",
		"load_tensors: CPU_Mapped model buffer size = 275.00 MiB",
		"llama_kv_cache: CUDA0 KV buffer size = 1280.00 MiB",
		"llama_context: CUDA0 compute buffer size = 120.00 MiB",
		"llama_context: CUDA_Host compute buffer size = 28.00 MiB",
		"nothing here",
	}
	llama := LlamaCpp{}
	m := llama.Measure(lines)
	byKey := map[string]uint64{}
	for _, x := range m {
		byKey[x.GetKey()] = x.GetBytes()
	}
	if byKey["device.weights"] != 5000<<20 || byKey["host.weights"] != 275<<20 || byKey["device.cache"] != 1280<<20 || byKey["device.compute"] != 120<<20 || byKey["host.compute"] != 28<<20 || len(m) != 5 {
		t.Fatalf("measurements %v", byKey)
	}
	vllm, sglang := VLLM{}, SGLang{}
	if got := vllm.Measure([]string{"INFO Model loading took 12.5 GiB and 3 seconds"}); len(got) != 1 || got[0].GetBytes() != uint64(12.5*float64(1<<30)) {
		t.Fatalf("vllm %v", got)
	}
	if got := sglang.Measure([]string{"Load weight end. type=X, dtype=bf16, avail mem=10 GB, mem usage=3.50 GB"}); len(got) != 1 || got[0].GetBytes() != uint64(3.5*float64(1<<30)) {
		t.Fatalf("sglang %v", got)
	}
	version := llama.Probes()[0]
	if v, ok := version.Parse("version: 6100 (abc123)\nbuilt with gcc"); !ok || v != "6100" {
		t.Fatalf("version %q %v", v, ok)
	}
	devices := llama.Probes()[1]
	if v, ok := devices.Parse("Available devices:\n  CUDA0: NVIDIA RTX (12000 MiB, 11000 MiB free)\n  Vulkan0: Something (1 MiB)\n"); !ok || v != "CUDA0,Vulkan0" {
		t.Fatalf("devices %q %v", v, ok)
	}
	if v, ok := versionField("vllm 0.10.1 something"); !ok || v != "0.10.1" {
		t.Fatalf("version field %q", v)
	}
	if _, ok := versionField("no numbers"); ok {
		t.Fatal("no version")
	}
	nemo := NeMo{}
	if nemo.PrepareTimeout() != 2*time.Hour || !nemo.Prepares("nemo") || nemo.Prepares("nemo2") {
		t.Fatal("nemo prepare")
	}
}

func TestMerge(t *testing.T) {
	out := Merge(map[string]string{"a": "1", "b": "1"}, nil, map[string]string{"b": "2"})
	if len(out) != 2 || out["a"] != "1" || out["b"] != "2" {
		t.Fatalf("merge %v", out)
	}
}
