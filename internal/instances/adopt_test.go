package instances

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A conductor record for adopted stages, answering as the test says
type fakeFormations struct {
	err   error
	asked chan time.Time
}

func (f *fakeFormations) HeadReady(ctx context.Context, formationID string, since time.Time) error {
	f.asked <- since
	return f.err
}

// One daemon's instances manager over a database, the fake runtime installed, a store with the
// model, and the fake launcher whose processes outlive the manager
type seatNode struct {
	dir      string
	db       *db.DB
	store    *store.Store
	launcher *fake.Launcher
	install  *v1.Install
}

func newSeatNode(t *testing.T, ctx context.Context) *seatNode {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	blobs, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	install := &v1.Install{Id: db.NewID(), RuntimeId: fake.RuntimeID, Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Path: filepath.Join(dir, "fake"), Dir: dir, Version: "1", CreatedAt: timestamppb.Now()}
	if err := database.PutInstall(ctx, install); err != nil {
		t.Fatal(err)
	}
	modelDir := filepath.Join(dir, "model")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	weights := filepath.Join(modelDir, "weights.fake")
	if err := os.WriteFile(weights, []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := blobs.WriteManifest(&v1.StoredModel{SourceId: "src", Repo: "org/model", Group: "q4", FormatId: fake.Format, Path: modelDir, Descriptor_: &v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_LANGUAGE, Architecture: "fake"}, Artifacts: []*v1.StoredArtifact{{Digest: "sha256:m", Path: weights, Artifact: &v1.Artifact{Path: "weights.fake", Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS}}}}); err != nil {
		t.Fatal(err)
	}
	return &seatNode{dir: dir, db: database, store: blobs, launcher: fake.New(), install: install}
}

// A manager as one daemon would hold it
func (n *seatNode) manager(t *testing.T, ctx context.Context, formations Formations) *Manager {
	t.Helper()
	rts, err := runtimes.New([]runtimes.Runtime{fake.Runtime{}})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return &Manager{
		DB:         n.db,
		Dir:        filepath.Join(n.dir, "instances"),
		Store:      n.store,
		Runtimes:   rts,
		Installs:   &installs.Manager{DB: n.db},
		Inspector:  &inspect.Inspector{Stored: n.store.ListManifests},
		Launcher:   n.launcher,
		Tasks:      tasks.New(ctx, log, n.db, bus),
		Host:       host.New(nil, nil, time.Minute),
		Events:     bus,
		Formations: formations,
		Log:        log,
	}
}

// A seat launch of the chain shape on the fake runtime
func seatRun(role string, rank uint32, exposed bool, dir string) *v1.RunRequest {
	return &v1.RunRequest{
		SourceId: "src", Repo: "org/model", Group: "q4", RuntimeId: fake.RuntimeID, Name: "chain/" + role,
		Seat: &v1.SeatSpec{FormationId: "f1", Shape: v1.Shape_SHAPE_CHAIN, Role: role, Rank: rank, Conductor: "conductor", Name: "chain", Address: "127.0.0.1", Admit: []string{"127.0.0.1"}, CacheDir: filepath.Join(dir, "cache"), CacheKey: "model-key", Exposed: exposed},
	}
}

func waitState(t *testing.T, m *Manager, id string, want v1.InstanceState) *v1.Instance {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		in, err := m.Get(id)
		if err == nil && in.GetState() == want {
			return in
		}
		if err == nil && Terminal(in.GetState()) && !Terminal(want) {
			t.Fatalf("instance %s ended %s wanting %s: %s", in.GetName(), in.GetState(), want, in.GetError())
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("instance %s did not reach %s", id, want)
	return nil
}

// Closes the guard of a seat as the old daemon dying would, freeing its port for the next one
func dropGuard(t *testing.T, m *Manager, id string) {
	t.Helper()
	in, err := m.find(id)
	if err != nil {
		t.Fatal(err)
	}
	if g := in.guardOf(); g != nil {
		g.Close()
	}
}

// A stage adopted after a restart with no client on its guard is verified by a hello on its
// loopback port, and its guard listener comes back on the port peers know
func TestAdoptIdleStageByHello(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	first := n.manager(t, ctx, nil)
	in, _, err := first.Run(ctx, seatRun(runtimes.RoleStage, 1, false, n.dir))
	if err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, first, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	dropGuard(t, first, in.GetId())
	second := n.manager(t, ctx, nil)
	if err := second.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	adopted := waitState(t, second, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if adopted.GetPid() != ready.GetPid() || adopted.GetSeat().GetPort() != ready.GetSeat().GetPort() {
		t.Fatalf("the same process is adopted with its guard on the same port: %v then %v", ready.GetSeat(), adopted.GetSeat())
	}
	_, logs, err := second.Tasks.Get(adopted.GetTaskId())
	if err != nil {
		t.Fatal(err)
	}
	if !containsLine(logs, "rpc server speaks protocol") {
		t.Fatalf("the adoption verified by a hello: %v", logs)
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(adopted.GetSeat().GetPort()))))
	if err != nil {
		t.Fatalf("the guard is back on its port: %v", err)
	}
	conn.Close()
	if _, err := second.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// A stage a head holds cannot take a hello, so its adoption is confirmed by the conductor's
// record of the head, and fails when the record says otherwise
func TestAdoptHeldStageAsksTheConductor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, tc := range []struct {
		name string
		err  error
		want v1.InstanceState
	}{
		{"confirmed", nil, v1.InstanceState_INSTANCE_STATE_READY},
		{"refused", errors.New("the head is failed"), v1.InstanceState_INSTANCE_STATE_FAILED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newSeatNode(t, ctx)
			first := n.manager(t, ctx, nil)
			in, _, err := first.Run(ctx, seatRun(runtimes.RoleStage, 1, true, n.dir))
			if err != nil {
				t.Fatal(err)
			}
			waitState(t, first, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
			conductor := &fakeFormations{err: tc.err, asked: make(chan time.Time, 1)}
			before := time.Now()
			second := n.manager(t, ctx, conductor)
			if err := second.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case since := <-conductor.asked:
				if since.Before(before) {
					t.Fatalf("the conductor's record must be fresher than the adoption: %s", since)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("an exposed stage in use asks the conductor's record")
			}
			adopted := waitState(t, second, in.GetId(), tc.want)
			if tc.err != nil && !strings.Contains(adopted.GetError(), "did not confirm") {
				t.Fatalf("the refusal names the conductor: %s", adopted.GetError())
			}
			if tc.err == nil {
				if _, err := second.Stop(ctx, in.GetId()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// A head adopted after a restart is probed for its template again before it is ready
func TestAdoptHeadProbesTemplate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := newSeatNode(t, ctx)
	first := n.manager(t, ctx, nil)
	in, _, err := first.Run(ctx, seatRun(runtimes.RoleHead, 0, false, n.dir))
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, first, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	proc := n.launcher.Proc("chain/head")
	probed := proc.Chats.Load()
	if probed == 0 {
		t.Fatal("the launch probed the template")
	}
	dropGuard(t, first, in.GetId())
	second := n.manager(t, ctx, nil)
	if err := second.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	adopted := waitState(t, second, in.GetId(), v1.InstanceState_INSTANCE_STATE_READY)
	if proc.Chats.Load() <= probed {
		t.Fatal("the adoption probed the template again")
	}
	if adopted.GetTemplate() == nil {
		t.Fatal("the adopted head keeps its template probe")
	}
	if _, err := second.Stop(ctx, in.GetId()); err != nil {
		t.Fatal(err)
	}
}

// The guard refuses every connection when the role admits no address
func TestGuardAdmitsNothingWhenUnnamed(t *testing.T) {
	g, err := newForwarder("127.0.0.1:0", "127.0.0.1:1", nil, false, "stage", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	conn, err := net.Dial("tcp", g.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("a guard admitting no address closes every connection")
	}
	if g.Active() != 0 {
		t.Fatalf("active %d", g.Active())
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}
