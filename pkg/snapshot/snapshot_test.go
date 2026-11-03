//go:build unix

package snapshot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A fake criu recording its arguments, dump writing an image, restore starting a sleeping child on the inherited fd whose pid it writes
const fakeCRIU = `#!/bin/sh
echo "$@" >> "$FAKE_LOG"
verb=$1; shift
images=""; pidfile=""; inherit=""
while [ $# -gt 0 ]; do
  case "$1" in
    --images-dir) images=$2; shift ;;
    --pidfile) pidfile=$2; shift ;;
    --inherit-fd) inherit=$2; shift ;;
  esac
  shift
done
case "$verb" in
  dump) echo image > "$images/pages-1.img" ;;
  restore)
    [ -f "$images/pages-1.img" ] || exit 1
    case "$inherit" in "fd[3]:"*) ;; *) exit 1 ;; esac
    sh -c 'echo restored; sleep 1000' >&3 2>&1 3>&- &
    echo $! > "$pidfile"
    ;;
esac
exit 0
`

// A fake cuda-checkpoint recording its arguments, failing the action FAKE_CUDA_FAILS names
const fakeCUDA = `#!/bin/sh
echo "$@" >> "$FAKE_LOG"
[ -n "$FAKE_CUDA_FAILS" ] && [ "$2" = "$FAKE_CUDA_FAILS" ] && exit 1
exit 0
`

func writeTool(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// The tool calls logged so far, one per line
func callLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// Fails unless the calls match want line for line, each starting with its want
func wantCalls(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d tool calls, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Fatalf("call %d is %q, want it to start with %q", i, got[i], want[i])
		}
	}
}

// Waits until cond holds, failing with what after five seconds
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A dump parks nothing for a tree without a GPU, runs criu with the tree left running and tcp closed, and writes the meta a restore reads back
func TestDumpAndRestoreDriveTheTools(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	t.Setenv("FAKE_LOG", logPath)
	s := &Snapshotter{Tools: Tools{CRIU: writeTool(t, dir, "criu", fakeCRIU), CUDACheckpoint: writeTool(t, dir, "cuda-checkpoint", fakeCUDA)}, Log: quiet()}
	image := filepath.Join(dir, "image")
	outLog := filepath.Join(dir, "instance.log")
	meta, err := s.Dump(context.Background(), Request{Pid: os.Getpid(), Dir: image, Port: 4321, LogPath: outLog})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != os.Getpid() || meta.Port != 4321 || len(meta.CUDA) != 0 || meta.LogPath != outLog {
		t.Fatalf("meta %+v", meta)
	}
	calls, _ := os.ReadFile(logPath)
	dumpLine := strings.TrimSpace(string(calls))
	for _, want := range []string{"dump", "--tree " + itoa(os.Getpid()), "--images-dir " + filepath.Join(image, imageDir), "--leave-running", "--tcp-close", "--shell-job"} {
		if !strings.Contains(dumpLine, want) {
			t.Fatalf("dump call %q lacks %q", dumpLine, want)
		}
	}
	if strings.Contains(dumpLine, "--action") {
		t.Fatalf("cuda-checkpoint ran without a GPU: %s", dumpLine)
	}
	read, ok, err := Read(image)
	if err != nil || !ok || read.Port != 4321 {
		t.Fatalf("read %+v %v %v", read, ok, err)
	}
	if _, ok, _ := Read(filepath.Join(dir, "nowhere")); ok {
		t.Fatal("a missing image reads as none")
	}
	newLog := filepath.Join(dir, "restored.log")
	pid, err := s.Restore(context.Background(), image, newLog)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	calls, _ = os.ReadFile(logPath)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	restoreLine := lines[len(lines)-1]
	for _, want := range []string{"restore", "--restore-detached", "--restore-sibling", "--pidfile " + filepath.Join(image, pidFile), "--tcp-close", "--shell-job", "--inherit-fd fd[3]:" + strings.TrimPrefix(outLog, "/")} {
		if !strings.Contains(restoreLine, want) {
			t.Fatalf("restore call %q lacks %q", restoreLine, want)
		}
	}
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		t.Fatalf("restored pid %d is not alive", pid)
	}
	waitFor(t, "the restored output on the inherited fd", func() bool {
		data, _ := os.ReadFile(newLog)
		return strings.Contains(string(data), "restored")
	})
	if _, err := s.Restore(context.Background(), filepath.Join(dir, "nowhere"), newLog); err == nil {
		t.Fatal("a missing image cannot be restored")
	}
}

// Every pid holding the GPU is locked and checkpointed before the dump and restored and unlocked after it, and again after a restore
func TestDumpParksCUDAStateAroundTheDump(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	t.Setenv("FAKE_LOG", logPath)
	tools := Tools{CRIU: writeTool(t, dir, "criu", fakeCRIU), CUDACheckpoint: writeTool(t, dir, "cuda-checkpoint", fakeCUDA)}
	holders := func(root int) ([]int, error) { return []int{root, 4242}, nil }
	s := &Snapshotter{Tools: tools, Log: quiet(), CUDAPIDs: holders}
	image := filepath.Join(dir, "image")
	meta, err := s.Dump(context.Background(), Request{Pid: os.Getpid(), Dir: image, Port: 4321, LogPath: filepath.Join(dir, "instance.log")})
	if err != nil {
		t.Fatal(err)
	}
	me := itoa(os.Getpid())
	if !slices.Equal(meta.CUDA, []int{os.Getpid(), 4242}) {
		t.Fatalf("meta cuda pids %v", meta.CUDA)
	}
	dumped := []string{
		"--action lock --pid " + me + " --timeout 120000",
		"--action checkpoint --pid " + me,
		"--action lock --pid 4242 --timeout 120000",
		"--action checkpoint --pid 4242",
		"dump --log-file " + filepath.Join(image, logFile) + " --tree " + me,
		"--action restore --pid " + me,
		"--action unlock --pid " + me,
		"--action restore --pid 4242",
		"--action unlock --pid 4242",
	}
	wantCalls(t, callLines(t, logPath), dumped)
	pid, err := s.Restore(context.Background(), image, filepath.Join(dir, "restored.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	wantCalls(t, callLines(t, logPath)[len(dumped):], []string{
		"restore --log-file " + filepath.Join(image, logFile),
		"--action restore --pid " + me,
		"--action unlock --pid " + me,
		"--action restore --pid 4242",
		"--action unlock --pid 4242",
	})
	bare := &Snapshotter{Tools: Tools{CRIU: tools.CRIU}, Log: quiet(), CUDAPIDs: holders}
	other := filepath.Join(dir, "other")
	if _, err := bare.Dump(context.Background(), Request{Pid: os.Getpid(), Dir: other, Port: 1}); !errors.Is(err, ErrMissingTool) {
		t.Fatalf("dump without cuda-checkpoint: %v", err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("a refused dump leaves no image behind")
	}
	if _, err := bare.Restore(context.Background(), image, filepath.Join(dir, "other.log")); !errors.Is(err, ErrMissingTool) {
		t.Fatalf("restore without cuda-checkpoint: %v", err)
	}
	t.Setenv("FAKE_CUDA_FAILS", "restore")
	if _, err := s.Restore(context.Background(), image, filepath.Join(dir, "failed.log")); err == nil || !strings.Contains(err.Error(), "cuda-checkpoint restore of pid "+me) {
		t.Fatalf("restore with a failing cuda resume: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(image, pidFile))
	if err != nil {
		t.Fatal(err)
	}
	left, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the restored tree whose CUDA state failed to be killed", func() bool { return syscall.Kill(left, 0) != nil })
}

// A failing criu dump removes the half written image and reports the tool's output
func TestDumpFailureLeavesNoImage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FAKE_LOG", filepath.Join(dir, "calls.log"))
	s := &Snapshotter{Tools: Tools{CRIU: writeTool(t, dir, "criu", "#!/bin/sh\necho 'Error (criu/cr-dump.c): no caps'; exit 1\n")}, Log: quiet()}
	image := filepath.Join(dir, "image")
	if _, err := s.Dump(context.Background(), Request{Pid: os.Getpid(), Dir: image, Port: 1}); err == nil || !strings.Contains(err.Error(), "no caps") {
		t.Fatalf("dump error %v", err)
	}
	if _, err := os.Stat(image); !os.IsNotExist(err) {
		t.Fatal("a failed dump leaves no image behind")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
