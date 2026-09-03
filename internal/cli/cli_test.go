package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/formats/gguf/gguftest"
)

func fixture(t *testing.T, parts ...string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join(append([]string{"..", "..", "test", "fixtures"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// Builds an offline environment with fixture probes and a local model
func setup(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	specDir := filepath.Join(root, "spec", "probes")
	os.MkdirAll(specDir, 0o755)
	base := filepath.Join("..", "..", "spec", "probes")
	for id, file := range map[string]string{"nvidia-smi": "nvidia-smi.csv", "meminfo": "meminfo.txt", "cpuinfo": "cpuinfo.txt", "rocm-smi": "rocm-smi.json"} {
		data, err := os.ReadFile(filepath.Join(base, id+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		i := strings.Index(text, "exec:")
		j := strings.Index(text, "parse:")
		text = text[:i] + "exec:\n  file: " + fixture(t, "probes", file) + "\n" + text[j:]
		text = strings.ReplaceAll(text, "os: [linux]\n", "")
		if err := os.WriteFile(filepath.Join(specDir, id+".yaml"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	models := filepath.Join(root, "models", "stories")
	os.MkdirAll(models, 0o755)
	f, err := os.Create(filepath.Join(models, "stories-Q8_0.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	kv := map[string]any{
		"general.architecture":          "llama",
		"general.name":                  "stories",
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
		name := "blk." + string(rune('0'+i)) + ".attn_q.weight"
		tensors = append(tensors, gguftest.Tensor{Name: name, Dims: []uint64{64, 64}, Type: 8, Bytes: 4096})
	}
	tensors = append(tensors, gguftest.Tensor{Name: "output.weight", Dims: []uint64{64, 2}, Type: 8, Bytes: 1024})
	if err := gguftest.Write(f, kv, tensors, 32); err != nil {
		t.Fatal(err)
	}
	f.Close()
	cfg := "data_dir: " + filepath.Join(root, "data") + "\ncache_dir: " + filepath.Join(root, "cache") + "\nspec_dirs: [" + filepath.Join(root, "spec") + "]\nsources:\n  - id: local\n    kind: SOURCE_KIND_LOCAL\n    path: " + filepath.Join(root, "models") + "\n"
	path := filepath.Join(root, "nebu.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func run(t *testing.T, cfg string, args ...string) (string, string, int) {
	t.Helper()
	var out, errw bytes.Buffer
	code := Main(append([]string{"--config", cfg}, args...), &out, &errw)
	return out.String(), errw.String(), code
}

func TestEndToEnd(t *testing.T) {
	cfg := setup(t)
	out, errw, code := run(t, cfg, "inspect", "stories", "--source", "local", "--ctx", "256")
	if code != 0 {
		t.Fatalf("inspect failed: %s %s", out, errw)
	}
	for _, want := range []string{"Q8_0", "llamacpp", "FITS", "layer 4/4", "output 1/1", "embedding:host"} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect output missing %q:\n%s", want, out)
		}
	}
	out, errw, code = run(t, cfg, "--json", "inspect", "stories")
	if code != 0 {
		t.Fatalf("json inspect failed: %s", errw)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil || parsed["rows"] == nil {
		t.Fatalf("json output invalid: %v\n%s", err, out)
	}
	out, errw, code = run(t, cfg, "doctor")
	if code != 0 || !strings.Contains(out, "devices.gpu") || !strings.Contains(out, "source.local") || strings.Contains(out, "FAIL") {
		t.Fatalf("doctor: code=%d\n%s\n%s", code, out, errw)
	}
	out, _, code = run(t, cfg, "host")
	if code != 0 || !strings.Contains(out, "RTX 3080") || !strings.Contains(out, "card0") {
		t.Fatalf("host:\n%s", out)
	}
	out, _, code = run(t, cfg, "runtimes")
	if code != 0 || !strings.Contains(out, "llamacpp") || !strings.Contains(out, "true") {
		t.Fatalf("runtimes:\n%s", out)
	}
	out, _, code = run(t, cfg, "sources")
	if code != 0 || !strings.Contains(out, "local") {
		t.Fatalf("sources:\n%s", out)
	}
	out, _, code = run(t, cfg, "search", "stor", "--source", "local")
	if code != 0 || !strings.Contains(out, "stories") {
		t.Fatalf("search:\n%s", out)
	}
	if _, _, code = run(t, cfg, "inspect", "missing", "--source", "nope"); code == 0 {
		t.Fatal("unknown source should fail")
	}
	if _, errw, code = run(t, cfg, "bogus"); code != 2 || !strings.Contains(errw, "unknown command") {
		t.Fatalf("bogus command: %d %s", code, errw)
	}
	if _, _, code = run(t, cfg, "version"); code != 0 {
		t.Fatal("version")
	}
}
