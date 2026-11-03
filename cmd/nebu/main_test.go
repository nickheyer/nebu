package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The test binary stands in for the nebu binary when asked to
func TestMain(m *testing.M) {
	if os.Getenv("NEBU_MAIN_TEST") == "1" {
		os.Args = append([]string{"nebu"}, strings.Split(os.Getenv("NEBU_MAIN_ARGS"), " ")...)
		if os.Getenv("NEBU_MAIN_ARGS") == "" {
			os.Args = []string{"nebu"}
		}
		main()
		return
	}
	os.Exit(m.Run())
}

// The binary answers usage with code 2 when given nothing or an unknown command, and its version with code 0
func TestMainExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		code       int
		stderr     string
		stdout     string
	}{
		{"no command", "", 2, "usage: nebu [flags] <command> [args]", ""},
		{"unknown command", "nonsense", 2, `unknown command "nonsense"`, ""},
		{"bad flag", "--nope", 2, "flag provided but not defined", ""},
		{"version", "version", 0, "", "nebu "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Env = append(os.Environ(), "NEBU_MAIN_TEST=1", "NEBU_MAIN_ARGS="+tc.args, "XDG_DATA_HOME="+t.TempDir(), "XDG_CACHE_HOME="+t.TempDir(), "HOME="+t.TempDir())
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.code {
				t.Fatalf("exit %d, want %d\nstdout %s\nstderr %s", code, tc.code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.stderr) || !strings.Contains(stdout.String(), tc.stdout) {
				t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
			}
		})
	}
}
