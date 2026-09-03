package sandbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestHostRunner(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "src", "sub"), 0o755)
	h := NewHost(root)
	if h.Kind() != v1.SandboxKind_SANDBOX_KIND_HOST || h.Root() != root || h.Path("src/x") != filepath.Join(root, "src", "x") {
		t.Fatal("host runner paths")
	}
	var out bytes.Buffer
	err := h.Run(context.Background(), Step{Name: "s", Command: []string{"sh", "-c", "pwd; echo $GREETING"}, Env: map[string]string{"GREETING": "hi"}, Dir: "src/sub"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), filepath.Join(root, "src", "sub")) || !strings.Contains(out.String(), "hi") {
		t.Fatalf("output %q", out.String())
	}
	if err := h.Run(context.Background(), Step{Name: "empty"}, &out); err == nil {
		t.Fatal("empty command should fail")
	}
	if err := h.Run(context.Background(), Step{Name: "bad", Command: []string{"sh", "-c", "exit 2"}}, &out); err == nil {
		t.Fatal("non zero exit should fail")
	}
}

func TestOCIRunnerArgs(t *testing.T) {
	root := t.TempDir()
	fake := filepath.Join(root, "fakecli")
	os.WriteFile(fake, []byte("#!/bin/sh\necho \"$@\"\n"), 0o755)
	o := NewOCI(root, fake, "img:1", []string{"--gpus", "all"})
	if o.Kind() != v1.SandboxKind_SANDBOX_KIND_OCI || o.Path("src") != Mount+"/src" || o.Path("") != Mount || o.CLI() != fake || o.Image() != "img:1" {
		t.Fatal("oci runner paths")
	}
	var out bytes.Buffer
	if err := o.Run(context.Background(), Step{Name: "s", Command: []string{"make", "-j2"}, Env: map[string]string{"B": "2", "A": "1"}, Dir: "src"}, &out); err != nil {
		t.Fatal(err)
	}
	// The container name is unique per run, so it is checked by shape and stripped
	got := strings.TrimSpace(out.String())
	name := regexp.MustCompile(` --name nebu-build-\d+-\d+`)
	if !name.MatchString(got) {
		t.Fatalf("args lack a container name: %q", got)
	}
	got = name.ReplaceAllString(got, "")
	want := "run --rm -v " + root + ":" + Mount + " -w " + Mount + "/src -e A=1 -e B=2 --gpus all img:1 make -j2"
	if got != want {
		t.Fatalf("args\n got %q\nwant %q", got, want)
	}
	noImage := NewOCI(root, fake, "", nil)
	if err := noImage.Run(context.Background(), Step{Name: "s", Command: []string{"x"}}, &out); err == nil {
		t.Fatal("missing image should fail")
	}
}

func TestDetectAndMissing(t *testing.T) {
	if _, ok := Detect([]string{"no-such-cli-1", "sh"}); !ok {
		t.Fatal("sh should be found")
	}
	if _, ok := Detect([]string{"no-such-cli-1"}); ok {
		t.Fatal("missing cli detected")
	}
	if m := MissingTools([]string{"sh", "no-such-tool-2"}); len(m) != 1 || m[0] != "no-such-tool-2" {
		t.Fatalf("missing %v", m)
	}
}
