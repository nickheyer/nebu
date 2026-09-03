package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/formats/gguf/gguftest"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

// Writes another quant of the stories model locally
func writeQuant(t *testing.T, cfg, group string) {
	t.Helper()
	root := filepath.Dir(cfg)
	f, err := os.Create(filepath.Join(root, "models", "stories", "stories-"+group+".gguf"))
	if err != nil {
		t.Fatal(err)
	}
	kv := map[string]any{
		"general.architecture":          "llama",
		"general.name":                  "stories " + group,
		"llama.block_count":             uint32(4),
		"llama.embedding_length":        uint32(64),
		"llama.attention.head_count":    uint32(8),
		"llama.attention.head_count_kv": uint32(4),
		"llama.context_length":          uint32(512),
		"tokenizer.ggml.tokens":         []string{"a", "b"},
	}
	var tensors []gguftest.Tensor
	tensors = append(tensors, gguftest.Tensor{Name: "token_embd.weight", Dims: []uint64{64, 2}, Type: 8, Bytes: 1024})
	for i := 0; i < 4; i++ {
		tensors = append(tensors, gguftest.Tensor{Name: "blk." + string(rune('0'+i)) + ".attn_q.weight", Dims: []uint64{64, 64}, Type: 8, Bytes: 4096})
	}
	tensors = append(tensors, gguftest.Tensor{Name: "output.weight", Dims: []uint64{64, 2}, Type: 8, Bytes: 1024})
	if err := gguftest.Write(f, kv, tensors, 32); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func firstGPU(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fixture(t, "probes", "nvidia-smi.csv"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`GPU-[0-9a-f-]+`).FindString(string(data))
	if m == "" {
		t.Fatal("no gpu uuid in fixture")
	}
	return m
}

func mustRun(t *testing.T, cfg string, args ...string) string {
	t.Helper()
	out, errw, code := run(t, cfg, args...)
	if code != 0 {
		t.Fatalf("%v failed: %s %s", args, out, errw)
	}
	return out
}

func chat(t *testing.T, addr, model, key string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(body)
}

func slotJSON(t *testing.T, cfg, addr, name string) map[string]any {
	t.Helper()
	out, errw, code := run(t, cfg, "--addr", addr, "--json", "slots", "show", name)
	if code != 0 {
		t.Fatalf("slots show: %s %s", out, errw)
	}
	var resp struct {
		Slot map[string]any `json:"slot"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Slot
}

func waitSlot(t *testing.T, cfg, addr, name, state string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var s map[string]any
	for time.Now().Before(deadline) {
		s = slotJSON(t, cfg, addr, name)
		if s["state"] == state {
			return s
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("slot %s never reached %s: %v", name, state, s)
	return s
}

func TestSlotSwapLifecycle(t *testing.T) {
	cfg := setup(t)
	writeFakeSpecs(t, cfg)
	writeQuant(t, cfg, "Q4_0")
	t.Setenv(fakeEnv, "1")
	addr, _ := startDaemon(t, cfg, "")
	mustRun(t, cfg, "--addr", addr, "pull", "stories", "--source", "local", "--group", "Q8_0")
	mustRun(t, cfg, "--addr", addr, "pull", "stories", "--source", "local", "--group", "Q4_0")
	self, _ := os.Executable()
	mustRun(t, cfg, "--addr", addr, "runtimes", "adopt", "fake", "--path", self)
	gpu := firstGPU(t)
	out := mustRun(t, cfg, "--addr", addr, "slots", "create", "main", "--device", gpu, "--memory", "6GiB", "--runtime", "fake", "--param", "n_ctx=256")
	if !strings.Contains(out, "EMPTY") || !strings.Contains(out, "6.0 GiB") {
		t.Fatalf("slot create: %s", out)
	}
	if code, body := chat(t, addr, "main", ""); code != 503 || !strings.Contains(body, "model_starting") {
		t.Fatalf("empty slot should answer 503: %d %s", code, body)
	}
	out = mustRun(t, cfg, "--addr", addr, "run", "stories", "--group", "Q8_0", "--slot", "main", "--source", "local")
	if !strings.Contains(out, "model main") {
		t.Fatalf("run in slot: %s", out)
	}
	waitSlot(t, cfg, addr, "main", "SLOT_STATE_READY")
	if out := mustRun(t, cfg, "--addr", addr, "logs", "main"); !strings.Contains(out, "visible=0") || !strings.Contains(out, "ctx=256") || !strings.Contains(out, "alias=main") {
		t.Fatalf("slot devices, params, and name should reach the runtime:\n%s", out)
	}
	if _, errw, code := run(t, cfg, "--addr", addr, "run", "stories", "--group", "Q4_0", "--slot", "main", "--source", "local"); code == 0 || !strings.Contains(errw, "use nebu swap") {
		t.Fatalf("occupied slot should refuse run: %s", errw)
	}
	if code, body := chat(t, addr, "main", ""); code != 200 || !strings.Contains(body, "Q8_0") {
		t.Fatalf("gateway through slot: %d %s", code, body)
	}
	first := slotJSON(t, cfg, addr, "main")["instance_id"].(string)
	out = mustRun(t, cfg, "--addr", addr, "swap", "main", "stories", "--group", "Q4_0", "--source", "local")
	if !strings.Contains(out, "blue-green") || !strings.Contains(out, "serves stories:Q4_0") {
		t.Fatalf("blue-green swap: %s", out)
	}
	second := slotJSON(t, cfg, addr, "main")["instance_id"].(string)
	if second == first || second == "" {
		t.Fatal("swap should change the occupant")
	}
	if code, body := chat(t, addr, "main", ""); code != 200 || !strings.Contains(body, "Q4_0") {
		t.Fatalf("gateway after swap: %d %s", code, body)
	}
	waitPs(t, cfg, addr, true, first, "STOPPED")
	out = mustRun(t, cfg, "--addr", addr, "routes")
	if !strings.Contains(out, "main") || !strings.Contains(out, second) || !strings.Contains(out, "READY") {
		t.Fatalf("routes: %s", out)
	}
	out = mustRun(t, cfg, "--addr", addr, "swap", "main", "stories", "--group", "Q8_0", "--source", "local", "--drain-first")
	if !strings.Contains(out, "drain first") || !strings.Contains(out, "serves stories:Q8_0") {
		t.Fatalf("drain first swap: %s", out)
	}
	waitPs(t, cfg, addr, true, second, "STOPPED")
	third := slotJSON(t, cfg, addr, "main")["instance_id"].(string)
	mustRun(t, cfg, "--addr", addr, "routes", "add", "extra", third)
	if code, body := chat(t, addr, "extra", ""); code != 200 || !strings.Contains(body, "Q8_0") {
		t.Fatalf("alias route: %d %s", code, body)
	}
	if _, errw, code := run(t, cfg, "--addr", addr, "routes", "remove", "main"); code == 0 || !strings.Contains(errw, "slot") {
		t.Fatalf("slot route must not be removable: %s", errw)
	}
	mustRun(t, cfg, "--addr", addr, "routes", "remove", "extra")
	if code, _ := chat(t, addr, "extra", ""); code != 404 {
		t.Fatal("alias should be gone")
	}
	out = mustRun(t, cfg, "--addr", addr, "gateway")
	if !strings.Contains(out, "listening http://"+addr+"/v1") || !strings.Contains(out, "auth false") {
		t.Fatalf("gateway status: %s", out)
	}
	if _, errw, code := run(t, cfg, "--addr", addr, "swap", "nope", "stories", "--group", "Q4_0", "--source", "local"); code == 0 || !strings.Contains(errw, "unknown slot") {
		t.Fatalf("unknown slot: %s", errw)
	}
	mustRun(t, cfg, "--addr", addr, "slots", "remove", "main", "--force")
}

func TestSwapRollsBack(t *testing.T) {
	cfg := setup(t)
	writeFakeSpecs(t, cfg)
	writeQuant(t, cfg, "Q4_0")
	t.Setenv(fakeEnv, "1")
	addr, _ := startDaemon(t, cfg, "")
	mustRun(t, cfg, "--addr", addr, "pull", "stories", "--source", "local", "--group", "Q8_0")
	mustRun(t, cfg, "--addr", addr, "pull", "stories", "--source", "local", "--group", "Q4_0")
	self, _ := os.Executable()
	mustRun(t, cfg, "--addr", addr, "runtimes", "adopt", "fake", "--path", self)
	mustRun(t, cfg, "--addr", addr, "slots", "create", "main")
	mustRun(t, cfg, "--addr", addr, "run", "stories", "--group", "Q8_0", "--slot", "main", "--source", "local")
	waitSlot(t, cfg, addr, "main", "SLOT_STATE_READY")
	before := slotJSON(t, cfg, addr, "main")["instance_id"].(string)
	out, errw, code := run(t, cfg, "--addr", addr, "swap", "main", "stories", "--group", "Q4_0", "--source", "local", "--param", "n_ctx=13", "--drain-first")
	if code == 0 {
		t.Fatalf("swap to a failing model should fail: %s", out)
	}
	if !strings.Contains(out, "rolled back") {
		t.Fatalf("rollback should be reported: %s %s", out, errw)
	}
	s := waitSlot(t, cfg, addr, "main", "SLOT_STATE_READY")
	after := s["instance_id"].(string)
	if after == before || after == "" {
		t.Fatal("rollback should relaunch the old model as a new instance")
	}
	if req := s["request"].(map[string]any); req["group"] != "Q8_0" {
		t.Fatalf("slot should serve the old model again: %v", req)
	}
	if !strings.Contains(s["error"].(string), "rolled back") {
		t.Fatalf("slot error should mention the rollback: %v", s["error"])
	}
	if code, body := chat(t, addr, "main", ""); code != 200 || !strings.Contains(body, "Q8_0") {
		t.Fatalf("gateway after rollback: %d %s", code, body)
	}
	mustRun(t, cfg, "--addr", addr, "slots", "remove", "main", "--force")
}

func TestSlotEvictAndRemove(t *testing.T) {
	cfg := setup(t)
	writeFakeSpecs(t, cfg)
	t.Setenv(fakeEnv, "1")
	addr, _ := startDaemon(t, cfg, "")
	mustRun(t, cfg, "--addr", addr, "pull", "stories", "--source", "local")
	self, _ := os.Executable()
	mustRun(t, cfg, "--addr", addr, "runtimes", "adopt", "fake", "--path", self)
	mustRun(t, cfg, "--addr", addr, "slots", "create", "main")
	if _, errw, code := run(t, cfg, "--addr", addr, "slots", "create", "main"); code == 0 || !strings.Contains(errw, "exists") {
		t.Fatalf("duplicate slot: %s", errw)
	}
	if _, errw, code := run(t, cfg, "--addr", addr, "slots", "create", "bad", "--device", "GPU-nope"); code == 0 || !strings.Contains(errw, "not probed") {
		t.Fatalf("unknown device: %s", errw)
	}
	mustRun(t, cfg, "--addr", addr, "run", "stories", "--slot", "main", "--source", "local")
	waitSlot(t, cfg, addr, "main", "SLOT_STATE_READY")
	pid, id := pidOf(t, cfg, addr, "main")
	if _, errw, code := run(t, cfg, "--addr", addr, "slots", "remove", "main"); code == 0 || !strings.Contains(errw, "evict") {
		t.Fatalf("occupied slot remove without force: %s", errw)
	}
	out := mustRun(t, cfg, "--addr", addr, "slots", "evict", "main")
	if !strings.Contains(out, "EMPTY") {
		t.Fatalf("evict: %s", out)
	}
	waitDead(t, pid)
	waitPs(t, cfg, addr, true, id, "STOPPED")
	if code, body := chat(t, addr, "main", ""); code != 503 || !strings.Contains(body, "model_starting") {
		t.Fatalf("evicted slot keeps its name: %d %s", code, body)
	}
	mustRun(t, cfg, "--addr", addr, "slots", "update", "main", "--memory", "1GiB", "--description", "changed")
	if s := slotJSON(t, cfg, addr, "main"); s["description"] != "changed" || s["memory_bytes"] != "1073741824" {
		t.Fatalf("update: %v", s)
	}
	mustRun(t, cfg, "--addr", addr, "run", "stories", "--slot", "main", "--source", "local")
	waitSlot(t, cfg, addr, "main", "SLOT_STATE_READY")
	pid, id = pidOf(t, cfg, addr, "main")
	mustRun(t, cfg, "--addr", addr, "slots", "remove", "main", "--force")
	waitDead(t, pid)
	waitPs(t, cfg, addr, true, id, "STOPPED")
	if out := mustRun(t, cfg, "--addr", addr, "routes"); strings.Contains(out, "main") {
		t.Fatalf("route should be gone with the slot: %s", out)
	}
	if code, _ := chat(t, addr, "main", ""); code != 404 {
		t.Fatal("removed slot answers 404")
	}
}

func TestSlotSurvivesRestart(t *testing.T) {
	cfg := setup(t)
	writeFakeSpecs(t, cfg)
	t.Setenv(fakeEnv, "1")
	listen, cmd := spawnDaemon(t, cfg)
	mustRun(t, cfg, "--addr", listen, "pull", "stories", "--source", "local")
	self, _ := os.Executable()
	mustRun(t, cfg, "--addr", listen, "runtimes", "adopt", "fake", "--path", self)
	mustRun(t, cfg, "--addr", listen, "slots", "create", "main", "--device", firstGPU(t))
	mustRun(t, cfg, "--addr", listen, "run", "stories", "--slot", "main", "--source", "local")
	waitSlot(t, cfg, listen, "main", "SLOT_STATE_READY")
	pid, id := pidOf(t, cfg, listen, "main")
	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()
	waitDead(t, pid)
	listen, _ = spawnDaemon(t, cfg)
	waitPs(t, cfg, listen, false, "main", "READY")
	s := waitSlot(t, cfg, listen, "main", "SLOT_STATE_READY")
	if s["instance_id"] == id || s["instance_id"] == "" {
		t.Fatalf("relaunched instance should be bound to the slot: %v", s)
	}
	if code, body := chat(t, listen, "main", ""); code != 200 || !strings.Contains(body, "Q8_0") {
		t.Fatalf("gateway after restart: %d %s", code, body)
	}
	if out := mustRun(t, cfg, "--addr", listen, "logs", "main"); !strings.Contains(out, "visible=0") {
		t.Fatalf("relaunch should keep the slot devices: %s", out)
	}
	mustRun(t, cfg, "--addr", listen, "slots", "remove", "main", "--force")
}

func TestMonitorLifecycle(t *testing.T) {
	cfg := setup(t)
	writeFakeSpecs(t, cfg)
	t.Setenv(fakeEnv, "1")
	addr, _ := startDaemon(t, cfg, "")
	mustRun(t, cfg, "--addr", addr, "pull", "stories", "--source", "local")
	self, _ := os.Executable()
	mustRun(t, cfg, "--addr", addr, "runtimes", "adopt", "fake", "--path", self)
	mustRun(t, cfg, "--addr", addr, "slots", "create", "main")
	mustRun(t, cfg, "--addr", addr, "run", "stories", "--slot", "main", "--source", "local")
	waitSlot(t, cfg, addr, "main", "SLOT_STATE_READY")
	out := mustRun(t, cfg, "--addr", addr, "monitor", "add", "stories", "--source", "local", "--slot", "main", "--match", "Q5")
	if !strings.Contains(out, "pull+swap") || !strings.Contains(out, "stories") {
		t.Fatalf("monitor add: %s", out)
	}
	if _, errw, code := run(t, cfg, "--addr", addr, "monitor", "add", "stories", "--source", "local"); code == 0 || !strings.Contains(errw, "already watched") {
		t.Fatalf("duplicate watch: %s", errw)
	}
	out = mustRun(t, cfg, "--addr", addr, "monitor", "check")
	if strings.Contains(out, "new weight group") || !strings.Contains(out, "unchanged") {
		t.Fatalf("first check should see nothing new: %s", out)
	}
	writeQuant(t, cfg, "Q5_K_M")
	writeQuant(t, cfg, "Q2_K")
	out = mustRun(t, cfg, "--addr", addr, "monitor", "check")
	for _, want := range []string{"new weight group Q5_K_M", "new weight group Q2_K", "pulling stories Q5_K_M", "swapping slot"} {
		if !strings.Contains(out, want) {
			t.Fatalf("check missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "pulling stories Q2_K") {
		t.Fatalf("Q2_K does not match the pattern:\n%s", out)
	}
	s := waitSlot(t, cfg, addr, "main", "SLOT_STATE_READY")
	if req := s["request"].(map[string]any); req["group"] != "Q5_K_M" {
		t.Fatalf("slot should now serve the new quant: %v", req)
	}
	out = mustRun(t, cfg, "--addr", addr, "monitor", "findings", "--unacked")
	if !strings.Contains(out, "NEW_GROUP") || !strings.Contains(out, "Q5_K_M") || !strings.Contains(out, "Q2_K") {
		t.Fatalf("findings: %s", out)
	}
	if out := mustRun(t, cfg, "--addr", addr, "list"); !strings.Contains(out, "Q5_K_M") || strings.Contains(out, "Q2_K") {
		t.Fatalf("only the matching group is pulled: %s", out)
	}
	var findings struct {
		Findings []map[string]any `json:"findings"`
	}
	json.Unmarshal([]byte(mustRun(t, cfg, "--addr", addr, "--json", "monitor", "findings")), &findings)
	for _, f := range findings.Findings {
		mustRun(t, cfg, "--addr", addr, "monitor", "ack", f["id"].(string))
	}
	if out := mustRun(t, cfg, "--addr", addr, "monitor", "findings", "--unacked"); strings.Contains(out, "NEW_GROUP") {
		t.Fatalf("all acknowledged: %s", out)
	}
	if out := mustRun(t, cfg, "--addr", addr, "monitor", "list"); !strings.Contains(out, "3") {
		t.Fatalf("watch should know three groups: %s", out)
	}
	mustRun(t, cfg, "--addr", addr, "monitor", "remove", "stories")
	if out := mustRun(t, cfg, "--addr", addr, "monitor", "list"); strings.Contains(out, "stories") {
		t.Fatalf("watch removed: %s", out)
	}
	mustRun(t, cfg, "--addr", addr, "slots", "remove", "main", "--force")
}

func TestExportAndMirrorSource(t *testing.T) {
	cfg := setup(t)
	root := filepath.Dir(cfg)
	mirror := filepath.Join(root, "mirror")
	data, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, append(data, []byte("  - id: mirror\n    kind: SOURCE_KIND_MIRROR\n    path: "+mirror+"\n")...), 0o644)
	mustRun(t, cfg, "pull", "stories", "--source", "local")
	out := mustRun(t, cfg, "store", "export", "--dir", mirror)
	if !strings.Contains(out, "exported stories Q8_0") {
		t.Fatalf("export: %s", out)
	}
	if _, err := os.Stat(filepath.Join(mirror, "stories", "stories-Q8_0.gguf")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(mirror, "index.json")); err != nil {
		t.Fatal(err)
	}
	out = mustRun(t, cfg, "search", "--source", "mirror")
	if !strings.Contains(out, "stories") {
		t.Fatalf("mirror search: %s", out)
	}
	out = mustRun(t, cfg, "inspect", "stories", "--source", "mirror", "--ctx", "256")
	if !strings.Contains(out, "Q8_0") || !strings.Contains(out, "FITS") {
		t.Fatalf("mirror inspect: %s", out)
	}
	out = mustRun(t, cfg, "pull", "stories", "--source", "mirror", "--group", "Q8_0")
	if !strings.Contains(out, "already stored") {
		t.Fatalf("mirror pull should share the blob: %s", out)
	}
	if out := mustRun(t, cfg, "list"); !strings.Contains(out, "mirror") {
		t.Fatalf("mirror model listed: %s", out)
	}
}

const buildFakeRecipe = `id: fake
runtime_id: fake
steps:
  - name: copy
    command: [cp, '{{.vars.exe}}', '{{.out}}/fakebin']
vars:
  exe: ''
binary: fakebin
`

func TestBuildRecipeAndRun(t *testing.T) {
	cfg := setup(t)
	writeFakeSpecs(t, cfg)
	root := filepath.Dir(cfg)
	os.MkdirAll(filepath.Join(root, "spec", "recipes"), 0o755)
	os.WriteFile(filepath.Join(root, "spec", "recipes", "fake.yaml"), []byte(buildFakeRecipe), 0o644)
	t.Setenv(fakeEnv, "1")
	self, _ := os.Executable()
	out := mustRun(t, cfg, "runtimes", "recipes")
	if !strings.Contains(out, "fake") || !strings.Contains(out, "default") {
		t.Fatalf("recipes: %s", out)
	}
	out = mustRun(t, cfg, "build", "fake", "--var", "exe="+self)
	if !strings.Contains(out, "SUCCEEDED") || !strings.Contains(out, "install fake-") {
		t.Fatalf("build: %s", out)
	}
	out = mustRun(t, cfg, "builds")
	if !strings.Contains(out, "SUCCEEDED") || !strings.Contains(out, "host") {
		t.Fatalf("builds: %s", out)
	}
	var builds struct {
		Builds []map[string]any `json:"builds"`
	}
	json.Unmarshal([]byte(mustRun(t, cfg, "--json", "builds")), &builds)
	if len(builds.Builds) != 1 {
		t.Fatalf("one build: %v", builds)
	}
	id := builds.Builds[0]["id"].(string)
	install := builds.Builds[0]["install_id"].(string)
	out = mustRun(t, cfg, "build", "fake", "--var", "exe="+self)
	if !strings.Contains(out, "already done") {
		t.Fatalf("cache hit: %s", out)
	}
	out = mustRun(t, cfg, "builds", "show", id)
	if !strings.Contains(out, "variant   default") || !strings.Contains(out, install) {
		t.Fatalf("show: %s", out)
	}
	if out := mustRun(t, cfg, "runtimes", "installs", "fake"); !strings.Contains(out, "built") || !strings.Contains(out, "version=1.2.3") {
		t.Fatalf("built install probed: %s", out)
	}
	if out := mustRun(t, cfg, "doctor"); !strings.Contains(out, "recipe.fake") {
		t.Fatalf("doctor reports recipes: %s", out)
	}
	addr, _ := startDaemon(t, cfg, "")
	mustRun(t, cfg, "--addr", addr, "pull", "stories", "--source", "local")
	out = mustRun(t, cfg, "--addr", addr, "run", "stories", "--source", "local", "--install", install)
	if !strings.Contains(out, "gateway") {
		t.Fatalf("run with built install: %s", out)
	}
	mustRun(t, cfg, "--addr", addr, "stop", "stories:Q8_0")
	mustRun(t, cfg, "--addr", addr, "builds", "remove", id)
	if out := mustRun(t, cfg, "--addr", addr, "runtimes", "installs"); strings.Contains(out, install) {
		t.Fatalf("install removed with build: %s", out)
	}
	if _, _, code := run(t, cfg, "--addr", addr, "build", "fake", "--variant", "nope"); code == 0 {
		t.Fatal("unknown variant should fail")
	}
}

func TestAuthTokenAndGatewayKeys(t *testing.T) {
	cfg := setup(t)
	writeFakeSpecs(t, cfg)
	data, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, append(data, []byte("auth:\n  token: secret\ngateway:\n  api_keys: [k1]\n")...), 0o644)
	t.Setenv(fakeEnv, "1")
	addr, _ := startDaemon(t, cfg, "")
	mustRun(t, cfg, "--addr", addr, "pull", "stories", "--source", "local")
	self, _ := os.Executable()
	mustRun(t, cfg, "--addr", addr, "runtimes", "adopt", "fake", "--path", self)
	mustRun(t, cfg, "--addr", addr, "run", "stories", "--source", "local")
	t.Setenv("NEBU_TOKEN", "wrong")
	if _, errw, code := run(t, cfg, "--addr", addr, "ps"); code == 0 || !strings.Contains(errw, "unauthenticated") {
		t.Fatalf("wrong token: %s", errw)
	}
	t.Setenv("NEBU_TOKEN", "secret")
	mustRun(t, cfg, "--addr", addr, "ps")
	if code, _ := chat(t, addr, "stories:Q8_0", ""); code != 401 {
		t.Fatal("gateway without key")
	}
	if code, body := chat(t, addr, "stories:Q8_0", "k1"); code != 200 || !strings.Contains(body, "hello from fake") {
		t.Fatalf("gateway with key: %d %s", code, body)
	}
	if out := mustRun(t, cfg, "--addr", addr, "gateway"); !strings.Contains(out, "auth true") {
		t.Fatalf("gateway status: %s", out)
	}
	mustRun(t, cfg, "--addr", addr, "stop", "stories:Q8_0")
}

func TestEventStream(t *testing.T) {
	cfg := setup(t)
	writeFakeSpecs(t, cfg)
	t.Setenv(fakeEnv, "1")
	addr, _ := startDaemon(t, cfg, "")
	mustRun(t, cfg, "--addr", addr, "slots", "create", "main")
	client := nebuv1connect.NewEventServiceClient(http.DefaultClient, "http://"+addr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := client.WatchEvents(ctx, connect.NewRequest(&v1.WatchEventsRequest{Snapshot: true, Kinds: []v1.EventKind{v1.EventKind_EVENT_KIND_SLOT, v1.EventKind_EVENT_KIND_ROUTE}}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	seen := map[string]bool{}
	for len(seen) < 3 && stream.Receive() {
		ev := stream.Msg().GetEvent()
		switch {
		case ev.GetSlot() != nil && ev.GetAction() == v1.EventAction_EVENT_ACTION_CREATED:
			seen["slot-snapshot"] = true
			go run(t, cfg, "--addr", addr, "slots", "update", "main", "--description", "live")
		case ev.GetSlot() != nil && ev.GetSlot().GetDescription() == "live":
			seen["slot-live"] = true
		case ev.GetRoute() != nil:
			seen["route"] = true
		}
	}
	if err := stream.Err(); err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
	if len(seen) < 3 {
		t.Fatalf("events seen %v", seen)
	}
	out, _, _ := run(t, cfg, "--addr", addr, "events", "--kind", "bogus")
	if out != "" {
		t.Fatal("bogus kind should not stream")
	}
}
