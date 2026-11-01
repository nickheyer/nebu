package runtimes

import (
	"errors"
	"slices"
	"testing"
)

// A run's words follow the slot's, and either side may be empty
func TestJoinArgs(t *testing.T) {
	for _, c := range []struct{ slot, run, want string }{
		{"", "", ""},
		{" --a 1 ", "", "--a 1"},
		{"", " --b 2 ", "--b 2"},
		{"--a 1", "--a 2", "--a 1 --a 2"},
	} {
		if got := JoinArgs(c.slot, c.run); got != c.want {
			t.Fatalf("JoinArgs(%q, %q) = %q, want %q", c.slot, c.run, got, c.want)
		}
	}
}

// Joined words render in order over the scope, quoting kept within each side
func TestRenderJoinedArgs(t *testing.T) {
	got, err := RenderArgs(JoinArgs(`--alias "slot words"`, `--path /c/{{.Name}}`), ArgScope{Name: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--alias", "slot words", "--path", "/c/m"}; !slices.Equal(got, want) {
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
