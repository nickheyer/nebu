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

// A param named env.NAME is an environment variable any runtime takes: it sets NAME over what the runtime set, renders
// as a template, is recorded with the launch's params, and is left unset when empty
func TestEnvParamsReachTheCommand(t *testing.T) {
	rt := LlamaCpp{}
	params, err := Resolve(rt, map[string]string{"n_ctx": "4096", "env.CUDA_VISIBLE_DEVICES": "1", "env.LOG_FILE": "/tmp/{{.Name}}-{{.Port}}.log", "env.UNSET": ""})
	if err != nil {
		t.Fatal(err)
	}
	if params.Str("env.CUDA_VISIBLE_DEVICES") != "1" || params["env.UNSET"] != nil {
		t.Fatalf("resolved %v", params)
	}
	in := Launch{Name: "m", Port: 9000, Params: params}
	cmd := &Command{Command: "server", Args: []string{"--port", "9000"}, Env: map[string]string{"CUDA_VISIBLE_DEVICES": "0", "KEPT": "yes"}, Params: map[string]string{"n_ctx": "4096"}}
	if err := Extend(in, cmd, `--alias {{.Name}}`); err != nil {
		t.Fatal(err)
	}
	if want := []string{"--port", "9000", "--alias", "m"}; !slices.Equal(cmd.Args, want) {
		t.Fatalf("args %q, want %q", cmd.Args, want)
	}
	if cmd.Env["CUDA_VISIBLE_DEVICES"] != "1" || cmd.Env["LOG_FILE"] != "/tmp/m-9000.log" || cmd.Env["KEPT"] != "yes" || len(cmd.Env) != 3 {
		t.Fatalf("env %v", cmd.Env)
	}
	if cmd.Params["env.LOG_FILE"] != "/tmp/m-9000.log" || cmd.Params["env.CUDA_VISIBLE_DEVICES"] != "1" || cmd.Params["n_ctx"] != "4096" {
		t.Fatalf("recorded params %v", cmd.Params)
	}
	for _, bad := range []map[string]string{{"env.": "x"}, {"env.NOT A NAME": "x"}, {"env.A=B": "x"}, {"env.OPEN": "{{.Name"}} {
		if _, err := Resolve(rt, bad); !errors.Is(err, ErrParam) {
			t.Fatalf("Resolve(%v) = %v", bad, err)
		}
	}
	if err := Extend(Launch{Params: map[string]any{"env.MISSING": "{{.Nowhere}}"}}, &Command{}, ""); !errors.Is(err, ErrParam) {
		t.Fatalf("a variable reading what no launch has: %v", err)
	}
}
