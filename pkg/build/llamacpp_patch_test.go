package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/recipes"
)

// The slot patch lands on the llama.cpp server source it was written against, once, and is tolerated when already there
func TestLlamaCppEmptySlotPatchApplies(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "llamacpp_get_available_slot.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "tools", "server", "server-context.cpp")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	var patch recipes.Patch
	for _, p := range (recipes.LlamaCpp{}).Patches() {
		if p.ID == "server-empty-slot-first" {
			patch = p
		}
	}
	if len(patch.Content) == 0 {
		t.Fatal("the llama.cpp recipe carries no server-empty-slot-first patch")
	}
	parsed, err := parseUnified(patch.Content)
	if err != nil {
		t.Fatal(err)
	}
	for round := range 2 {
		touched, err := applyPatches(root, parsed, 1)
		if err != nil || len(touched) != 1 || touched[0] != "tools/server/server-context.cpp" {
			t.Fatalf("round %d: %v %v", round, touched, err)
		}
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if strings.Count(got, "selected empty slot over a weak match") != 1 || strings.Count(got, "ret = nullptr;") != 2 {
		t.Fatalf("patched source:\n%s", got)
	}
	// The yield sits after the weak match is saved to the prompt cache and before the LRU pick runs
	save, yield, lru := strings.Index(got, "update_cache = true;"), strings.Index(got, "selected empty slot over a weak match"), strings.Index(got, "get_free_slots(1, -1)")
	if !(save < yield && yield < lru) {
		t.Fatalf("yield at %d is not between the cache save at %d and the LRU pick at %d", yield, save, lru)
	}
}
