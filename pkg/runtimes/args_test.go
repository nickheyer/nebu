package runtimes

import (
	"errors"
	"slices"
	"testing"
)

// A run's words stand in place of the slot's, the slot's standing when the run names none
func TestRunArgs(t *testing.T) {
	for _, c := range []struct{ slot, run, want string }{
		{"", "", ""},
		{" --a 1 ", "", "--a 1"},
		{"", " --b 2 ", "--b 2"},
		{"--a 1", "--a 2", "--a 2"},
	} {
		if got := RunArgs(c.slot, c.run); got != c.want {
			t.Fatalf("RunArgs(%q, %q) = %q, want %q", c.slot, c.run, got, c.want)
		}
	}
}

// A run's words render in order over the scope, quoting kept
func TestRenderRunArgs(t *testing.T) {
	got, err := RenderArgs(RunArgs(`--threads 2`, `--alias "run words" --path /c/{{.Name}}`), ArgScope{Name: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--alias", "run words", "--path", "/c/m"}; !slices.Equal(got, want) {
		t.Fatalf("rendered %q, want %q", got, want)
	}
}

// Words that cannot split or parse are refused as a bad parameter
func TestCheckArgsRefuses(t *testing.T) {
	for _, args := range []string{`--a "open`, `--a {{.Name`, `--a {{nope}}`, `--a \`} {
		if err := CheckArgs(args); !errors.Is(err, ErrParam) {
			t.Fatalf("CheckArgs(%q) = %v", args, err)
		}
	}
}
