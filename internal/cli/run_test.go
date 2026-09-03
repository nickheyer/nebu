package cli

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/daemon"
	"github.com/nickheyer/nebu/pkg/config"
)

const fakeRuntimeSpec = `id: fake
name: Fake
formats: [gguf]
probes:
  - key: version
    args: [--version]
    match: 'fake version (?P<value>\S+)'
launch:
  command: '{{.install.path}}'
  args: ['--model', '{{index .artifacts "weights"}}', '--host', '{{.host}}', '--port', '{{.port}}']
  health:
    path: /health
    interval_ms: 50
    timeout_ms: 10000
  api: API_FLAVOR_OPENAI
  stop_grace_ms: 2000
params:
  - name: n_ctx
    type: PARAM_TYPE_INT
    default: '512'
    flag: --ctx-size
  - name: n_ubatch
    type: PARAM_TYPE_INT
    default: '64'
  - name: n_gpu_layers
    type: PARAM_TYPE_INT
    default: auto
    solved: true
    flag: --n-gpu-layers
  - name: cache_type_k
    type: PARAM_TYPE_STRING
    default: f16
  - name: cache_type_v
    type: PARAM_TYPE_STRING
    default: f16
estimate:
  groups:
    - kind: TENSOR_GROUP_KIND_EMBEDDING
      pool: POOL_KIND_HOST
    - kind: TENSOR_GROUP_KIND_LAYER
      pool: POOL_KIND_DEVICE
      param: n_gpu_layers
      spill_priority: 10
    - kind: TENSOR_GROUP_KIND_OUTPUT
      pool: POOL_KIND_DEVICE
      param: n_gpu_layers
      spill_priority: 10
  cache_bytes: n_ctx * cache_per_token * (cache_bytes[cache_type_k] + cache_bytes[cache_type_v]) / 2
  overhead_bytes: 1 * MiB
  cache_element_bytes:
    f16: 2
  margin: 0.05
  context_param: n_ctx
triage: [fake]
report:
  - key: device.weights
    match: 'CUDA\d* model buffer size\s*=\s*(?P<value>[\d.]+) (?P<unit>\w+)'
  - key: device.compute
    match: 'CUDA\d* compute buffer size\s*=\s*(?P<value>[\d.]+) (?P<unit>\w+)'
`

const fakeTriageSpec = `id: fake
rules:
  - id: assert
    match: 'GGML_ASSERT: (?P<what>.*)'
    summary: assertion ${what}
    hint: try other params
`

// Starts a real daemon on a free port for the run tests
func startDaemon(t *testing.T, cfgPath string) string {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	d, err := daemon.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Serve(ctx, ln, nil)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return ln.Addr().String()
}

func TestRunLifecycle(t *testing.T) {
	cfg := setup(t)
	root := filepath.Dir(cfg)
	os.MkdirAll(filepath.Join(root, "spec", "runtimes"), 0o755)
	os.MkdirAll(filepath.Join(root, "spec", "triage"), 0o755)
	os.WriteFile(filepath.Join(root, "spec", "runtimes", "fake.yaml"), []byte(fakeRuntimeSpec), 0o644)
	os.WriteFile(filepath.Join(root, "spec", "triage", "fake.yaml"), []byte(fakeTriageSpec), 0o644)
	t.Setenv(fakeEnv, "1")
	addr := startDaemon(t, cfg)
	self, _ := os.Executable()
	run := func(args ...string) (string, string, int) {
		return run(t, cfg, append([]string{"--addr", addr}, args...)...)
	}

	out, errw, code := run("pull", "stories", "--source", "local")
	if code != 0 {
		t.Fatalf("pull: %s %s", out, errw)
	}
	if _, errw, code = run("run", "stories", "--source", "local", "--runtime", "fake"); code == 0 || !strings.Contains(errw, "no install") {
		t.Fatalf("run without install should fail: %d %s", code, errw)
	}
	out, errw, code = run("runtimes", "adopt", "fake", "--path", self)
	if code != 0 || !strings.Contains(out, "1.2.3") {
		t.Fatalf("adopt: %d %s %s", code, out, errw)
	}
	out, _, code = run("runtimes", "installs", "fake")
	if code != 0 || !strings.Contains(out, "adopted") {
		t.Fatalf("installs:\n%s", out)
	}
	out, errw, code = run("run", "stories", "--source", "local", "--runtime", "fake", "--name", "stories", "--param", "n_ctx=256")
	if code != 0 {
		t.Fatalf("run: %s %s", out, errw)
	}
	for _, want := range []string{"plan FITS", "ready stories at http://127.0.0.1:", "device.weights 10.0 MiB", "gateway http://" + addr + "/v1 model stories"} {
		if !strings.Contains(out, want) {
			t.Errorf("run output missing %q:\n%s", want, out)
		}
	}
	resp, err := http.Post("http://"+addr+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"stories","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "hello from fake") {
		t.Fatalf("gateway %d %s", resp.StatusCode, body)
	}
	resp, _ = http.Get("http://" + addr + "/v1/models")
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"id":"stories"`) {
		t.Fatalf("models %s", body)
	}
	out, _, code = run("ps")
	if code != 0 || !strings.Contains(out, "READY") || !strings.Contains(out, "stories") {
		t.Fatalf("ps:\n%s", out)
	}
	out, _, code = run("logs", "stories")
	if code != 0 || !strings.Contains(out, "ctx=256") || !strings.Contains(out, "layers=5") {
		t.Fatalf("logs should show rendered flags:\n%s", out)
	}
	if _, errw, code = run("run", "stories", "--source", "local", "--runtime", "fake", "--name", "stories"); code == 0 || !strings.Contains(errw, "already running") {
		t.Fatalf("duplicate name should fail: %d %s", code, errw)
	}
	out, _, code = run("stop", "stories")
	if code != 0 || !strings.Contains(out, "stopped") {
		t.Fatalf("stop:\n%s", out)
	}
	out, _, code = run("ps", "--all")
	if code != 0 || !strings.Contains(out, "STOPPED") {
		t.Fatalf("ps --all:\n%s", out)
	}
	if _, _, code = run("ps"); code != 0 {
		t.Fatal("ps after stop")
	}
	resp, _ = http.Post("http://"+addr+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"stories"}`))
	if resp.StatusCode != 404 {
		t.Fatalf("stopped model should be unrouted, got %d", resp.StatusCode)
	}

	t.Setenv(fakeFail, "1")
	out, errw, code = run("run", "stories", "--source", "local", "--runtime", "fake", "--name", "broken")
	if code == 0 || !strings.Contains(errw, "assertion boom") || !strings.Contains(out, "triage assert") {
		t.Fatalf("failed run should surface triage: %d\n%s\n%s", code, out, errw)
	}
	out, _, _ = run("ps", "--all")
	if !strings.Contains(out, "FAILED") || !strings.Contains(out, "assertion boom") {
		t.Fatalf("ps --all after failure:\n%s", out)
	}
	out, _, code = run("doctor")
	if code != 0 || !strings.Contains(out, "runtime.fake") || !strings.Contains(out, "1 installs") {
		t.Fatalf("doctor:\n%s", out)
	}
}
