//go:build linux

package instances

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/snapshot"
	"golang.org/x/sys/unix"
)

// The port a re-executed test binary serves health on as the process a fake criu restores
const restoredPortEnv = "NEBU_TEST_RESTORED_PORT"

// Serves health on the restored port in place of running tests when the fake criu re-executes the binary
func TestMain(m *testing.M) {
	if port := os.Getenv(restoredPortEnv); port != "" {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			fmt.Println("restored bind failed:", err)
			os.Exit(2)
		}
		fmt.Println("restored serving on", port)
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		http.Serve(ln, mux)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A fake criu whose dump writes an image and whose restore re-executes the test binary on the recorded port, writing to the inherited fd, detached as criu would leave it
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
    port=$(sed -n 's/.*"port":\([0-9]*\).*/\1/p' "$images/../nebu-snapshot.json")
    NEBU_TEST_RESTORED_PORT=$port setsid "$NEBU_TEST_BINARY" -test.run='^$' >&3 2>&1 3>&- &
    echo $! > "$pidfile"
    ;;
esac
exit 0
`

// A fake cuda-checkpoint recording its calls beside the fake criu's
const fakeCUDACheckpoint = `#!/bin/sh
echo "$@" >> "$FAKE_LOG"
exit 0
`

// A probe recording the checkpoint tool facts a host would
type toolProbe struct{ facts map[string]string }

func (toolProbe) ID() string               { return "tools" }
func (toolProbe) Description() string      { return "checkpoint tools" }
func (toolProbe) Runs(string, string) bool { return true }
func (p toolProbe) Run(context.Context) host.Result {
	return host.Result{Status: v1.ProbeStatus_PROBE_STATUS_OK, Facts: p.facts}
}

// A slot manager owning nothing that records the Snapshotted condition of every record change, in order
type conditionLog struct {
	mu   sync.Mutex
	seen map[string][]*v1.Condition
}

func (*conditionLog) Reservation(context.Context, string) (*Reservation, error) {
	return nil, errors.New("the test holds no slots")
}

func (*conditionLog) Owns(string) bool { return false }

func (c *conditionLog) OnInstance(rec *v1.Instance) {
	cond := ConditionOf(rec.GetConditions(), CondSnapshot)
	if cond == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := c.seen[rec.GetId()]
	if len(seen) == 0 || seen[len(seen)-1].GetReason() != cond.GetReason() {
		c.seen[rec.GetId()] = append(seen, cond)
	}
}

// The Snapshotted conditions an instance went through, one per reason change
func (c *conditionLog) history(id string) []*v1.Condition {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*v1.Condition(nil), c.seen[id]...)
}

// Makes the test process the reaper of the orphans a detached restore leaves, as the restored tree's parent is in a daemon
func subreaper(t *testing.T) {
	t.Helper()
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0) })
}

// Writes a fake criu and cuda-checkpoint logging their calls to the returned file, the test binary named for restores
func fakeTools(t *testing.T) (criu, cuda, calls string) {
	t.Helper()
	bin := t.TempDir()
	criu, cuda, calls = filepath.Join(bin, "criu"), filepath.Join(bin, "cuda-checkpoint"), filepath.Join(bin, "calls.log")
	if err := os.WriteFile(criu, []byte(fakeCRIU), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cuda, []byte(fakeCUDACheckpoint), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_LOG", calls)
	t.Setenv("NEBU_TEST_BINARY", os.Args[0])
	return criu, cuda, calls
}

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

// A manager on a host whose probes found the given tool facts and whatever more probes report
func snapshotManager(t *testing.T, ctx context.Context, n *seatNode, facts map[string]string, more ...host.Probe) *Manager {
	t.Helper()
	m := n.manager(t, ctx, nil)
	m.Host = host.New(append([]host.Probe{toolProbe{facts}}, more...), nil, time.Minute)
	return m
}

// The one image directory under the manager
func snapshotDirOf(t *testing.T, m *Manager) string {
	t.Helper()
	images, err := filepath.Glob(filepath.Join(m.Dir, "snapshots", "snap-*", "nebu-snapshot.json"))
	if err != nil || len(images) != 1 {
		t.Fatalf("images %v %v", images, err)
	}
	return filepath.Dir(images[0])
}

// A snapshot is taken once ready, a later launch of the same run restores it on its port without a cold start, and a host without criu refuses
func TestSnapshotRestoresInsteadOfColdLaunch(t *testing.T) {
	subreaper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	criu, _, calls := fakeTools(t)
	n := newSeatNode(t, ctx)
	m := snapshotManager(t, ctx, n, map[string]string{snapshot.FactCRIU: criu})
	run := soloRun(nil)
	run.Snapshot = true
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	proc := n.launcher.Proc("solo")
	logged, _ := os.ReadFile(calls)
	dump := strings.TrimSpace(string(logged))
	for _, want := range []string{"dump", fmt.Sprintf("--tree %d", proc.Pid()), "--leave-running", "--tcp-close", "--shell-job"} {
		if !strings.Contains(dump, want) {
			t.Fatalf("dump call %q lacks %q", dump, want)
		}
	}
	c := ConditionOf(ready.GetConditions(), CondSnapshot)
	if c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE || c.GetReason() != ReasonSnapshotted {
		t.Fatalf("snapshot condition %v", c)
	}
	snapshotDirOf(t, m)
	port := portOf(t, ready.GetEndpoint())
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
	again, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	restored := waitState(t, m, again.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if got := n.launcher.Launches("solo"); got != 1 {
		t.Fatalf("%d cold launches, the second run restores the image", got)
	}
	if portOf(t, restored.GetEndpoint()) != port || restored.GetPid() == 0 || restored.GetPid() == int32(proc.Pid()) {
		t.Fatalf("restored endpoint %s pid %d, want port %d on a new pid", restored.GetEndpoint(), restored.GetPid(), port)
	}
	if c := ConditionOf(restored.GetConditions(), CondSnapshot); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE || c.GetReason() != ReasonRestored {
		t.Fatalf("restored condition %v", c)
	}
	if !strings.Contains(strings.Join(restored.GetCommand(), " "), "--name solo") || restored.GetInventory().GetAccelerator() != "cpu" {
		t.Fatalf("the restored record carries the image's command and inventory: %v %v", restored.GetCommand(), restored.GetInventory())
	}
	var lines []string
	waitFor(t, "the restored process's log", func() bool {
		lines = nil
		m.Logs(ctx, again.GetId(), false, 0, func(batch []string) error { lines = append(lines, batch...); return nil })
		return strings.Contains(strings.Join(lines, "\n"), "restored serving on")
	})
	logged, _ = os.ReadFile(calls)
	if !strings.Contains(string(logged), "restore --log-file") || !strings.Contains(string(logged), "--restore-sibling") || !strings.Contains(string(logged), "--inherit-fd fd[3]:") {
		t.Fatalf("restore call: %s", logged)
	}
	if eps := n.registrar.Endpoints(ownerOf(again.GetId())); len(eps) != 1 || eps[0].Address.Port != port {
		t.Fatalf("published %+v", eps)
	}
	stopped, err := m.Stop(ctx, again.GetId())
	if err != nil || stopped.GetState() != v1.InstanceState_INSTANCE_STATE_STOPPED || stopped.GetError() != "" {
		t.Fatalf("stop of the restored process: %s %q %v", stopped.GetState(), stopped.GetError(), err)
	}
	waitFor(t, "the restored process reaped", func() bool { return unix.Kill(int(restored.GetPid()), 0) != nil })

	bare := n.manager(t, ctx, nil)
	if _, _, err := bare.Run(ctx, run); err == nil || !strings.Contains(err.Error(), "no criu") {
		t.Fatalf("a host without criu refuses a snapshot run: %v", err)
	}
	seat := seatRun("head", 0, false, n.dir)
	seat.Snapshot = true
	if _, _, err := bare.Run(ctx, seat); err == nil || !strings.Contains(err.Error(), "solo runs") {
		t.Fatalf("a seat refuses a snapshot: %v", err)
	}
}

// On an NVIDIA placement cuda-checkpoint locks and checkpoints the tree before the dump, restores and unlocks it after, and again after a restore
func TestSnapshotParksCUDAStateOnNvidiaPlacement(t *testing.T) {
	subreaper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	criu, cuda, calls := fakeTools(t)
	gpu := memoryProbe{host.Result{
		Status:  v1.ProbeStatus_PROBE_STATUS_OK,
		Devices: []*v1.Device{{Id: "gpu0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia", Name: "fake gpu"}},
		Pools: []*v1.MemoryPool{
			{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 64 << 30, FreeBytes: 32 << 30},
			{Id: "gpu0", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "gpu0", TotalBytes: 24 << 30, FreeBytes: 20 << 30},
		},
	}}
	n := newSeatNode(t, ctx)
	m := snapshotManager(t, ctx, n, map[string]string{snapshot.FactCRIU: criu, snapshot.FactCUDACheckpoint: cuda}, gpu)
	m.CUDAPIDs = func(root int) ([]int, error) { return []int{root}, nil }
	run := soloRun(nil)
	run.Snapshot = true
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	pid := strconv.Itoa(n.launcher.Proc("solo").Pid())
	dumped := []string{
		"--action lock --pid " + pid + " --timeout 120000",
		"--action checkpoint --pid " + pid,
		"dump --log-file",
		"--action restore --pid " + pid,
		"--action unlock --pid " + pid,
	}
	wantCalls(t, callLines(t, calls), dumped)
	if c := ConditionOf(ready.GetConditions(), CondSnapshot); c.GetReason() != ReasonSnapshotted || !strings.Contains(c.GetMessage(), "CUDA state of pids "+pid+" parked and resumed") {
		t.Fatalf("snapshot condition %v", c)
	}
	if _, err := m.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
	again, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	restored := waitState(t, m, again.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	wantCalls(t, callLines(t, calls)[len(dumped):], []string{"restore --log-file", "--action restore --pid " + pid, "--action unlock --pid " + pid})
	if got := n.launcher.Launches("solo"); got != 1 {
		t.Fatalf("%d cold launches, the second run restores the image", got)
	}
	if c := ConditionOf(restored.GetConditions(), CondSnapshot); c.GetReason() != ReasonRestored {
		t.Fatalf("restored condition %v", c)
	}
	stopped, err := m.Stop(ctx, again.GetId())
	if err != nil || stopped.GetError() != "" {
		t.Fatalf("stop of the restored process: %q %v", stopped.GetError(), err)
	}
	waitFor(t, "the restored process reaped", func() bool { return unix.Kill(int(restored.GetPid()), 0) != nil })

	bare := snapshotManager(t, ctx, n, map[string]string{snapshot.FactCRIU: criu}, gpu)
	if _, _, err := bare.Run(ctx, run); err == nil || !strings.Contains(err.Error(), "no cuda-checkpoint") {
		t.Fatalf("a GPU host without cuda-checkpoint refuses a snapshot run: %v", err)
	}
}

// A restore that fails on a broken image records the failure on the Snapshotted condition, then the cold launch takes a fresh image
func TestSnapshotRestoreFailureIsRecordedBeforeColdLaunch(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(t *testing.T, dir string)
		want    string
	}{
		{"the image pages are gone", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "images", "pages-1.img")); err != nil {
				t.Fatal(err)
			}
		}, "criu restore"},
		{"the meta is not json", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "nebu-snapshot.json"), []byte("{broken"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "nebu-snapshot.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			criu, _, _ := fakeTools(t)
			n := newSeatNode(t, ctx)
			m := snapshotManager(t, ctx, n, map[string]string{snapshot.FactCRIU: criu})
			seen := &conditionLog{seen: map[string][]*v1.Condition{}}
			m.Slots = seen
			run := soloRun(nil)
			run.Snapshot = true
			in, _, err := m.Run(ctx, run)
			if err != nil {
				t.Fatal(err)
			}
			waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
			dir := snapshotDirOf(t, m)
			if _, err := m.Stop(ctx, in.GetId()); err != nil {
				t.Fatal(err)
			}
			tc.corrupt(t, dir)
			again, _, err := m.Run(ctx, run)
			if err != nil {
				t.Fatal(err)
			}
			ready := waitState(t, m, again.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
			if got := n.launcher.Launches("solo"); got != 2 {
				t.Fatalf("%d launches, the broken image launches cold", got)
			}
			history := seen.history(again.GetId())
			if len(history) != 2 || history[0].GetReason() != ReasonRestoreFailed || history[1].GetReason() != ReasonSnapshotted {
				t.Fatalf("snapshot condition history %v", history)
			}
			if history[0].GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE || !strings.Contains(history[0].GetMessage(), tc.want) {
				t.Fatalf("restore failure %v", history[0])
			}
			if c := ConditionOf(ready.GetConditions(), CondSnapshot); c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_TRUE || c.GetReason() != ReasonSnapshotted {
				t.Fatalf("snapshot condition %v", c)
			}
			meta, ok, err := snapshot.Read(dir)
			if err != nil || !ok || meta.Pid != n.launcher.Proc("solo").Pid() {
				t.Fatalf("fresh image %+v %v %v", meta, ok, err)
			}
			if _, err := m.Stop(ctx, again.GetId()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A relaunch after a daemon restart on a host that lost criu fails its plan naming the tool instead of launching cold
func TestSnapshotRelaunchWithoutCriuFailsThePlan(t *testing.T) {
	quickBackoff(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	criu, _, _ := fakeTools(t)
	n := newSeatNode(t, ctx)
	m := snapshotManager(t, ctx, n, map[string]string{snapshot.FactCRIU: criu})
	run := soloRun(nil)
	run.Snapshot = true
	in, _, err := m.Run(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	m.Close()
	bare := n.manager(t, ctx, nil)
	if err := bare.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	var failed *v1.Instance
	waitFor(t, "the relaunch to fail", func() bool {
		failed = current(t, bare, in.GetId())
		if failed.GetState() != v1.InstanceState_INSTANCE_STATE_FAILED {
			t.Logf("relaunch is %s, error %q, launches %d", failed.GetState(), failed.GetError(), n.launcher.Launches("solo"))
		}
		return failed.GetState() == v1.InstanceState_INSTANCE_STATE_FAILED
	})
	c := ConditionOf(failed.GetConditions(), CondScheduled)
	if c.GetStatus() != v1.ConditionStatus_CONDITION_STATUS_FALSE || c.GetReason() != ReasonPlanFailed || !strings.Contains(c.GetMessage(), "no criu") {
		t.Fatalf("scheduled condition %v", c)
	}
	if !strings.Contains(failed.GetError(), "no criu") {
		t.Fatalf("error %q names no missing tool", failed.GetError())
	}
	if got := n.launcher.Launches("solo"); got != 1 {
		t.Fatalf("%d launches, a host without criu launches nothing cold", got)
	}
}
