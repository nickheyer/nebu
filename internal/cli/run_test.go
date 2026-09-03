package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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
    fix:
      n_ctx: '128'
`

// Starts an in process daemon and returns its address and a stop that waits for shutdown
func startDaemon(t *testing.T, cfgPath, listen string) (string, func()) {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	d, err := daemon.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Serve(ctx, ln, nil)
		close(done)
	}()
	stop := func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return ln.Addr().String(), stop
}

// Starts the daemon as a separate process so it can be killed the way a crash would
func spawnDaemon(t *testing.T, cfgPath string, extraEnv ...string) (string, *exec.Cmd) {
	t.Helper()
	listen := freeAddr(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "--config", cfgPath, "serve")
	cmd.Env = append(os.Environ(), serveEnv+"=1", fakeEnv+"=1", config.EnvListen+"="+listen)
	cmd.Env = append(cmd.Env, extraEnv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !reachable(listen) {
		time.Sleep(50 * time.Millisecond)
	}
	if !reachable(listen) {
		t.Fatalf("daemon process did not listen on %s:\n%s", listen, stderr.String())
	}
	return listen, cmd
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func writeFakeSpecs(t *testing.T, cfg string) {
	t.Helper()
	root := filepath.Dir(cfg)
	os.MkdirAll(filepath.Join(root, "spec", "runtimes"), 0o755)
	os.MkdirAll(filepath.Join(root, "spec", "triage"), 0o755)
	os.WriteFile(filepath.Join(root, "spec", "runtimes", "fake.yaml"), []byte(fakeRuntimeSpec), 0o644)
	os.WriteFile(filepath.Join(root, "spec", "triage", "fake.yaml"), []byte(fakeTriageSpec), 0o644)
}

// Polls the instance list on addr until one line matches every want
func waitPs(t *testing.T, cfg, addr string, all bool, wants ...string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var out string
	for time.Now().Before(deadline) {
		args := []string{"--addr", addr, "ps"}
		if all {
			args = append(args, "--all")
		}
		out, _, _ = run(t, cfg, args...)
		for _, line := range strings.Split(out, "\n") {
			hit := true
			for _, want := range wants {
				if !strings.Contains(line, want) {
					hit = false
				}
			}
			if hit {
				return out
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no instance line matching %v within deadline:\n%s", wants, out)
	return out
}

func waitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("pid %d should be dead", pid)
	}
}

func instanceJSON(t *testing.T, cfg, addr, id string) map[string]any {
	t.Helper()
	out, errw, code := run(t, cfg, "--addr", addr, "--json", "show", id)
	if code != 0 {
		t.Fatalf("show %s: %s %s", id, out, errw)
	}
	var resp struct {
		Instance map[string]any `json:"instance"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("show json: %v\n%s", err, out)
	}
	return resp.Instance
}

func pidOf(t *testing.T, cfg, addr, name string) (int, string) {
	t.Helper()
	in := instanceJSON(t, cfg, addr, name)
	return int(in["pid"].(float64)), in["id"].(string)
}

func TestRunLifecycle(t *testing.T) {
	cfg := setup(t)
	writeFakeSpecs(t, cfg)
	t.Setenv(fakeEnv, "1")
	addr, stopA := startDaemon(t, cfg, "")
	self, _ := os.Executable()
	cli := func(args ...string) (string, string, int) {
		return run(t, cfg, append([]string{"--addr", addr}, args...)...)
	}

	out, errw, code := cli("pull", "stories", "--source", "local")
	if code != 0 {
		t.Fatalf("pull: %s %s", out, errw)
	}
	if _, errw, code = cli("run", "stories", "--source", "local", "--runtime", "fake"); code == 0 || !strings.Contains(errw, "no install") {
		t.Fatalf("run without install should fail: %d %s", code, errw)
	}
	out, errw, code = cli("runtimes", "adopt", "fake", "--path", self)
	if code != 0 || !strings.Contains(out, "1.2.3") {
		t.Fatalf("adopt: %d %s %s", code, out, errw)
	}
	out, _, code = cli("runtimes", "installs", "fake")
	if code != 0 || !strings.Contains(out, "adopted") {
		t.Fatalf("installs:\n%s", out)
	}
	out, errw, code = cli("run", "stories", "--source", "local", "--runtime", "fake", "--name", "stories", "--param", "n_ctx=256")
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
	out, _, code = cli("ps")
	if code != 0 || !strings.Contains(out, "READY") || !strings.Contains(out, "stories") {
		t.Fatalf("ps:\n%s", out)
	}
	out, _, code = cli("logs", "stories")
	if code != 0 || !strings.Contains(out, "ctx=256") || !strings.Contains(out, "layers=5") {
		t.Fatalf("logs should show rendered flags:\n%s", out)
	}
	out, _, code = cli("show", "stories")
	if code != 0 || !strings.Contains(out, "relaunch on daemon start  yes") || !strings.Contains(out, "device.weights") {
		t.Fatalf("show:\n%s", out)
	}
	if _, errw, code = cli("run", "stories", "--source", "local", "--runtime", "fake", "--name", "stories"); code == 0 || !strings.Contains(errw, "already running") {
		t.Fatalf("duplicate name should fail: %d %s", code, errw)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfg), "data", "nebu.db")); err != nil {
		t.Fatalf("store should exist: %v", err)
	}
	out, _, code = cli("stop", "stories")
	if code != 0 || !strings.Contains(out, "stopped") {
		t.Fatalf("stop:\n%s", out)
	}
	out, _, code = cli("ps", "--all")
	if code != 0 || !strings.Contains(out, "STOPPED") {
		t.Fatalf("ps --all:\n%s", out)
	}
	if _, _, code = cli("ps"); code != 0 {
		t.Fatal("ps after stop")
	}
	resp, _ = http.Post("http://"+addr+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"stories"}`))
	if resp.StatusCode != 404 {
		t.Fatalf("stopped model should be unrouted, got %d", resp.StatusCode)
	}
	out, _, code = cli("logs", "stories")
	if code != 0 || !strings.Contains(out, "ctx=256") {
		t.Fatalf("logs of a stopped instance come from its output file:\n%s", out)
	}

	t.Setenv(fakeFail, "1")
	out, errw, code = cli("run", "stories", "--source", "local", "--runtime", "fake", "--name", "broken")
	if code == 0 || !strings.Contains(errw, "assertion boom") || !strings.Contains(out, "triage assert") {
		t.Fatalf("failed run should surface triage: %d\n%s\n%s", code, out, errw)
	}
	out, _, _ = cli("ps", "--all")
	if !strings.Contains(out, "FAILED") || !strings.Contains(out, "assertion boom") {
		t.Fatalf("ps --all after failure:\n%s", out)
	}
	out, _, code = cli("show", "broken")
	for _, want := range []string{"FAILED", "assert: assertion boom", "hint: try other params", "try: --param n_ctx=128", "line: GGML_ASSERT: boom"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("show broken missing %q:\n%s", want, out)
		}
	}
	out, _, code = cli("doctor")
	if code != 0 || !strings.Contains(out, "runtime.fake") || !strings.Contains(out, "1 installs") {
		t.Fatalf("doctor:\n%s", out)
	}
	t.Setenv(fakeFail, "")

	// A model running at graceful shutdown comes back when the daemon starts again
	if out, errw, code = cli("run", "stories", "--source", "local", "--runtime", "fake", "--name", "stories"); code != 0 {
		t.Fatalf("run again: %s %s", out, errw)
	}
	firstPid, _ := pidOf(t, cfg, addr, "stories")
	stopA()
	if syscall.Kill(firstPid, 0) == nil {
		t.Fatal("graceful shutdown should stop the runtime")
	}
	addrB, stopB := startDaemon(t, cfg, "")
	waitPs(t, cfg, addrB, false, "stories", "READY")
	all, _, _ := run(t, cfg, "--addr", addrB, "ps", "--all")
	if !strings.Contains(all, "broken") || !strings.Contains(all, "FAILED") || strings.Count(all, "stories") < 2 {
		t.Fatalf("history should survive restart:\n%s", all)
	}
	out, _, code = run(t, cfg, "--addr", addrB, "logs", "broken")
	if code != 0 || !strings.Contains(out, "GGML_ASSERT: boom") {
		t.Fatalf("output file should survive restart:\n%s", out)
	}
	out, _, code = run(t, cfg, "--addr", addrB, "tasks", "list")
	if code != 0 || !strings.Contains(out, "run broken") || !strings.Contains(out, "pull") {
		t.Fatalf("task history should survive restart:\n%s", out)
	}
	resp, err = http.Post("http://"+addrB+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"stories"}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("relaunched model should be routed: %v", err)
	}
	secondPid, _ := pidOf(t, cfg, addrB, "stories")
	if secondPid == firstPid {
		t.Fatal("relaunch should be a new process")
	}
	stopB()

	// A daemon killed outright takes its runtime with it, and the next daemon relaunches
	listenC, procC := spawnDaemon(t, cfg)
	waitPs(t, cfg, listenC, false, "stories", "READY")
	thirdPid, _ := pidOf(t, cfg, listenC, "stories")
	procC.Process.Kill()
	procC.Wait()
	waitDead(t, thirdPid)
	addrD, stopD := startDaemon(t, cfg, "")
	waitPs(t, cfg, addrD, false, "stories", "READY")
	fourthPid, _ := pidOf(t, cfg, addrD, "stories")
	if fourthPid == thirdPid {
		t.Fatal("killed runtime should have been relaunched as a new process")
	}
	all, _, _ = run(t, cfg, "--addr", addrD, "ps", "--all")
	if !strings.Contains(all, "daemon restarted") {
		t.Fatalf("record left by the killed daemon should say why it stopped:\n%s", all)
	}
	stopD()

	// A runtime that survives its daemon is adopted by the next one, not restarted
	listenE, procE := spawnDaemon(t, cfg, fakeIgnoreTerm+"=1")
	waitPs(t, cfg, listenE, false, "stories", "READY")
	fifthPid, fifthID := pidOf(t, cfg, listenE, "stories")
	procE.Process.Kill()
	procE.Wait()
	time.Sleep(300 * time.Millisecond)
	if syscall.Kill(fifthPid, 0) != nil {
		t.Fatal("runtime ignoring the parent death signal should still be alive")
	}
	addrF, stopF := startDaemon(t, cfg, "")
	waitPs(t, cfg, addrF, false, "stories", "READY")
	adoptedPid, adoptedID := pidOf(t, cfg, addrF, "stories")
	if adoptedPid != fifthPid || adoptedID != fifthID {
		t.Fatalf("adoption should keep pid %d id %s, got %d %s", fifthPid, fifthID, adoptedPid, adoptedID)
	}
	if all, _, _ = run(t, cfg, "--addr", addrF, "ps"); strings.Count(all, "stories") != 1 {
		t.Fatalf("adopted instance must not be duplicated:\n%s", all)
	}
	resp, err = http.Post("http://"+addrF+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"stories"}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("adopted model should be routed: %v", err)
	}
	if out, _, code = run(t, cfg, "--addr", addrF, "logs", "stories"); code != 0 || !strings.Contains(out, "listening on") {
		t.Fatalf("adopted output should be readable:\n%s", out)
	}
	start := time.Now()
	if out, _, code = run(t, cfg, "--addr", addrF, "stop", "stories"); code != 0 || !strings.Contains(out, "stopped") {
		t.Fatalf("stop adopted:\n%s", out)
	}
	if time.Since(start) < 1500*time.Millisecond {
		t.Fatal("stopping a runtime that ignores the grace signal should take the grace period before escalating")
	}
	waitDead(t, fifthPid)
	stopF()

	// A model stopped by request stays stopped across restarts
	addrG, _ := startDaemon(t, cfg, "")
	time.Sleep(300 * time.Millisecond)
	out, _, code = run(t, cfg, "--addr", addrG, "ps")
	if code != 0 || strings.Contains(out, "READY") || strings.Contains(out, "STARTING") {
		t.Fatalf("stopped model must not relaunch:\n%s", out)
	}
	out, _, _ = run(t, cfg, "--addr", addrG, "show", "stories")
	if !strings.Contains(out, "relaunch on daemon start  no") {
		t.Fatalf("show after stop:\n%s", out)
	}
}

func TestDaemonAutoDetect(t *testing.T) {
	cfg := setup(t)
	listen := freeAddr(t)
	data, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, append(data, []byte("listen: "+listen+"\n")...), 0o644)
	if _, errw, code := run(t, cfg, "ps"); code == 0 || !strings.Contains(errw, "needs a running daemon") {
		t.Fatalf("ps without a daemon should fail: %d %s", code, errw)
	}
	startDaemon(t, cfg, listen)
	if out, errw, code := run(t, cfg, "ps"); code != 0 {
		t.Fatalf("ps should find the daemon on the listen address: %s %s", out, errw)
	}
}
