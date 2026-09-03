package build

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const simplePatch = `diff --git a/src/notes.txt b/src/notes.txt
index 1..2 100644
--- a/src/notes.txt
+++ b/src/notes.txt
@@ -1,3 +1,4 @@
 one
+two
 three
-four
+five
`

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParseUnified(t *testing.T) {
	patches, err := parseUnified([]byte(simplePatch))
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 || patches[0].oldPath != "a/src/notes.txt" || patches[0].newPath != "b/src/notes.txt" || len(patches[0].hunks) != 1 {
		t.Fatalf("parsed %+v", patches)
	}
	h := patches[0].hunks[0]
	if h.oldStart != 1 || h.oldCount != 3 || h.newStart != 1 || h.newCount != 4 || len(h.lines) != 5 {
		t.Fatalf("hunk %+v", h)
	}
	if _, err := parseUnified([]byte("nothing here")); err == nil {
		t.Fatal("no headers should fail")
	}
}

func TestApplyAndAlreadyApplied(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/notes.txt", "one\nthree\nfour\n")
	patches, _ := parseUnified([]byte(simplePatch))
	touched, err := applyPatches(root, patches, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(touched) != 1 || touched[0] != "src/notes.txt" {
		t.Fatalf("touched %v", touched)
	}
	if got := read(t, root, "src/notes.txt"); got != "one\ntwo\nthree\nfive\n" {
		t.Fatalf("applied %q", got)
	}
	if _, err := applyPatches(root, patches, 1); err != nil {
		t.Fatalf("second apply should be tolerated: %v", err)
	}
	if got := read(t, root, "src/notes.txt"); got != "one\ntwo\nthree\nfive\n" {
		t.Fatalf("second apply changed the file: %q", got)
	}
}

func TestApplyWithOffset(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/notes.txt", "header\nheader\nheader\none\nthree\nfour\ntail\n")
	patches, _ := parseUnified([]byte(simplePatch))
	if _, err := applyPatches(root, patches, 1); err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "src/notes.txt"); got != "header\nheader\nheader\none\ntwo\nthree\nfive\ntail\n" {
		t.Fatalf("offset apply %q", got)
	}
}

func TestApplyFails(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/notes.txt", "totally\ndifferent\n")
	patches, _ := parseUnified([]byte(simplePatch))
	_, err := applyPatches(root, patches, 1)
	if !errors.Is(err, ErrHunk) {
		t.Fatalf("want ErrHunk, got %v", err)
	}
}

func TestNewDeleteAndNoNewline(t *testing.T) {
	root := t.TempDir()
	write(t, root, "gone.txt", "bye\n")
	patch := `--- /dev/null
+++ b/created.txt
@@ -0,0 +1,2 @@
+hello
+world
\ No newline at end of file
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
`
	patches, err := parseUnified([]byte(patch))
	if err != nil {
		t.Fatal(err)
	}
	if !patches[0].isNew || !patches[1].isDelete {
		t.Fatalf("flags %+v", patches)
	}
	if _, err := applyPatches(root, patches, 1); err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "created.txt"); got != "hello\nworld" {
		t.Fatalf("created %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); !os.IsNotExist(err) {
		t.Fatal("file should be deleted")
	}
}

func TestStripAndEscape(t *testing.T) {
	if stripPath("a/b/c.txt", 1) != "b/c.txt" || stripPath("a/b/c.txt", 0) != "a/b/c.txt" || stripPath("a/b/c.txt", 9) != "c.txt" {
		t.Fatal("strip")
	}
	root := t.TempDir()
	patch := "--- a/../../escape\n+++ b/../../escape\n@@ -0,0 +1 @@\n+x\n"
	patches, _ := parseUnified([]byte(patch))
	if _, err := applyPatches(root, patches, 1); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escape should fail: %v", err)
	}
}

func TestDiffPathQuotedAndTabs(t *testing.T) {
	if diffPath("a/x.c\t2020-01-01") != "a/x.c" || diffPath(`"a/sp ace.c"`) != "a/sp ace.c" {
		t.Fatal("diff path parsing")
	}
}
