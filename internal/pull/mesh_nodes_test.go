package pull_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/formations"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/mesh"
	"github.com/nickheyer/nebu/internal/pull"
	"github.com/nickheyer/nebu/internal/rpc"
	"github.com/nickheyer/nebu/internal/rpc/services"
	"github.com/nickheyer/nebu/internal/settings"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/formats/all"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/perf"
	"github.com/nickheyer/nebu/pkg/precision"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/transfer"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A nebu node for store sharing: its mesh membership, its store served on /blobs behind the
// member session, its puller looking at the mesh first, and a local directory as its internet
type node struct {
	name     string
	mesh     *mesh.Manager
	store    *store.Store
	puller   *pull.Puller
	tasks    *tasks.Manager
	internet string
}

func newNode(t *testing.T, ctx context.Context, name string) *node {
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
	rts, err := runtimes.New(runtimes.All())
	if err != nil {
		t.Fatal(err)
	}
	table, err := perf.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New()
	set := &settings.Manager{DB: database, Events: bus}
	if err := set.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Update(ctx, &v1.Settings{HostLabel: name}); err != nil {
		t.Fatal(err)
	}
	guard := auth.NewGuard("", nil, nil)
	m := &mesh.Manager{
		DB:        database,
		Config:    &v1.MeshConfig{Listen: "127.0.0.1:0", Advertise: "127.0.0.1", Announce: proto.Bool(false)},
		Host:      host.New(nil, nil, time.Minute),
		Installs:  &installs.Manager{DB: database},
		Runtimes:  rts,
		Store:     blobs,
		Perf:      table,
		Settings:  set,
		Events:    bus,
		Guard:     guard,
		Log:       log,
		Version:   "test",
		APIListen: "127.0.0.1:0",
	}
	if err := m.Open(ctx); err != nil {
		t.Fatal(err)
	}
	internet := filepath.Join(dir, "internet")
	if err := os.MkdirAll(internet, 0o755); err != nil {
		t.Fatal(err)
	}
	reg, err := sources.Build([]*v1.Source{{Id: "disk", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": internet}}})
	if err != nil {
		t.Fatal(err)
	}
	fmts, err := all.Registry()
	if err != nil {
		t.Fatal(err)
	}
	families, err := archs.New(archs.All())
	if err != nil {
		t.Fatal(err)
	}
	memo, err := cache.Open(filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	tm := tasks.New(ctx, log, database, bus)
	puller := &pull.Puller{
		Inspector: &inspect.Inspector{Sources: reg, Formats: fmts, Builder: &descriptor.Builder{Formats: fmts, Archs: families, Scale: precision.Bits{}}, Runtimes: rts, Host: m.Host, Cache: memo, Stored: blobs.ListManifests, Contexts: []uint32{4096}, Log: log},
		Store:     blobs,
		Fetcher:   transfer.New(2, 1<<16, 2, log),
		Tasks:     tm,
		Events:    bus,
		Mesh:      mesh.Source{Mesh: m},
	}
	svc := services.NewMeshService(m, nil, puller, tm, blobs, table, nil, guard)
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewMeshServiceHandler(svc))
	rpc.MountMesh(mux, guard, blobs, filepath.Join(dir, "rpc"))
	m.Handler = h2c.NewHandler(mux, &http2.Server{})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return &node{name: name, mesh: m, store: blobs, puller: puller, tasks: tm, internet: internet}
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return store.Digest(hex.EncodeToString(sum[:]))
}

// Puts a model in the node's store the way a pull leaves it, and lets the mesh know
func (n *node) hold(t *testing.T, source, repo, group string, files map[string][]byte) *v1.StoredModel {
	t.Helper()
	dir, err := n.store.GroupDir(source, repo, group)
	if err != nil {
		t.Fatal(err)
	}
	m := &v1.StoredModel{SourceId: source, Repo: repo, Revision: "local", Group: group, FormatId: "gguf", Path: dir, PulledAt: timestamppb.Now(),
		Descriptor_: &v1.Descriptor{FormatId: "gguf", Group: group, Architecture: "llama", Kind: v1.ModelKind_MODEL_KIND_LANGUAGE, ParameterCount: 7, Params: map[string]float64{"n_layer": 2}}}
	for p, data := range files {
		digest := digestOf(data)
		if err := os.WriteFile(n.store.BlobPath(digest), data, 0o644); err != nil {
			t.Fatal(err)
		}
		link, err := n.store.Link(source, repo, group, p, digest)
		if err != nil {
			t.Fatal(err)
		}
		m.Artifacts = append(m.Artifacts, &v1.StoredArtifact{Artifact: &v1.Artifact{Path: p, SizeBytes: uint64(len(data)), Sha256: store.Hex(digest)}, Digest: digest, Path: link})
		m.Bytes += uint64(len(data))
	}
	if err := n.store.WriteManifest(m); err != nil {
		t.Fatal(err)
	}
	n.mesh.Bump()
	return m
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within 15 s", what)
}

// Whether a node's copy of another's record lists a model
func (n *node) sees(other *node, repo, group string) bool {
	for _, rec := range n.mesh.Nodes() {
		if rec.GetId() != other.mesh.Self() || rec.GetState() != v1.NodeState_NODE_STATE_READY {
			continue
		}
		for _, s := range rec.GetStored() {
			if s.GetRepo() == repo && s.GetGroup() == group {
				return true
			}
		}
	}
	return false
}

// Makes a mesh on a and joins b to it
func join(t *testing.T, ctx context.Context, a, b *node) {
	t.Helper()
	if _, _, err := a.mesh.Init(ctx, "test", false); err != nil {
		t.Fatal(err)
	}
	token, err := a.mesh.Token()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := b.mesh.Join(ctx, token); err != nil {
		t.Fatal(err)
	}
}

func (n *node) finish(t *testing.T, ctx context.Context, task *v1.Task, err error) (*v1.Task, []string) {
	t.Helper()
	if err != nil {
		t.Fatalf("pull refused: %v", err)
	}
	done, err := n.tasks.Wait(ctx, task.GetId())
	if err != nil {
		t.Fatal(err)
	}
	_, logs, err := n.tasks.Get(task.GetId())
	if err != nil {
		t.Fatal(err)
	}
	return done, logs
}

func hasLine(logs []string, want string) bool {
	for _, l := range logs {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// A pull on one node lands from the other node holding the model, over /blobs under the member
// session, with the source silent; the group is taken from what the member holds
func TestPullLandsFromAMemberOverTheMesh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, "alpha")
	b := newNode(t, ctx, "beta")
	files := map[string][]byte{"tiny-Q4.gguf": bytes.Repeat([]byte("weights "), 20000), "README.md": []byte("# tiny")}
	held := a.hold(t, "disk", "org/tiny", "Q4", files)
	join(t, ctx, a, b)
	eventually(t, "beta sees alpha holding the model", func() bool { return b.sees(a, "org/tiny", "Q4") })
	task, err := b.puller.Pull(ctx, &v1.PullRequest{Repo: "org/tiny"})
	done, logs := b.finish(t, ctx, task, err)
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("%s: %s\n%s", done.GetState(), done.GetError(), strings.Join(logs, "\n"))
	}
	if done.GetLabels()["from"] != a.mesh.Self() || done.GetTitle() != "pull org/tiny Q4 from alpha" {
		t.Fatalf("task %v", done)
	}
	for _, sa := range held.GetArtifacts() {
		if !b.store.HasBlob(sa.GetDigest()) || !hasLine(logs, sa.GetArtifact().GetPath()+" verified "+sa.GetDigest()+" from alpha") {
			t.Fatalf("%s lands from alpha:\n%s", sa.GetArtifact().GetPath(), strings.Join(logs, "\n"))
		}
	}
	got, err := b.store.ReadManifest("disk", "org/tiny", "Q4")
	if err != nil {
		t.Fatal(err)
	}
	if store.DescriptorDigest(got.GetDescriptor_()) != store.DescriptorDigest(held.GetDescriptor_()) || got.GetBytes() != held.GetBytes() || got.GetRuntimes() == 0 {
		t.Fatalf("manifest copied with its descriptor: %v", got)
	}
	// A repair after the store lost a blob takes it from the member again
	lost := held.GetArtifacts()[0].GetDigest()
	if err := b.store.RemoveBlob(lost); err != nil {
		t.Fatal(err)
	}
	task, err = b.puller.Pull(ctx, &v1.PullRequest{SourceId: "disk", Repo: "org/tiny", Group: "Q4"})
	done, logs = b.finish(t, ctx, task, err)
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED || !b.store.HasBlob(lost) || !hasLine(logs, "verified "+lost+" from alpha") {
		t.Fatalf("repair: %s %s\n%s", done.GetState(), done.GetError(), strings.Join(logs, "\n"))
	}
	// A node outside the session cannot read the blob channel
	resp, err := http.Get("http://" + a.mesh.Status().GetListen() + "/blobs/" + lost)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("blobs without a member session: %d", resp.StatusCode)
	}
}

// --to pulls onto another member: the member pulls from the mesh, and the task on the requesting
// node follows it to the end with the member's log
func TestPullToAnotherMemberIsFollowed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, ctx, "alpha")
	b := newNode(t, ctx, "beta")
	files := map[string][]byte{"tiny-Q4.gguf": bytes.Repeat([]byte("weights "), 5000)}
	held := a.hold(t, "disk", "org/tiny", "Q4", files)
	join(t, ctx, a, b)
	eventually(t, "beta sees alpha holding the model", func() bool { return b.sees(a, "org/tiny", "Q4") })
	eventually(t, "alpha sees beta", func() bool {
		for _, rec := range a.mesh.Nodes() {
			if rec.GetId() == b.mesh.Self() && rec.GetState() == v1.NodeState_NODE_STATE_READY {
				return true
			}
		}
		return false
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	conductor := &formations.Manager{Mesh: a.mesh, Tasks: a.tasks, Store: a.store, Puller: a.puller, Log: log}
	task, err := conductor.PullTo(ctx, "beta", &v1.PullRequest{SourceId: "disk", Repo: "org/tiny", Group: "Q4"})
	done, logs := a.finish(t, ctx, task, err)
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("%s: %s\n%s", done.GetState(), done.GetError(), strings.Join(logs, "\n"))
	}
	if done.GetTitle() != "pull org/tiny Q4 onto beta" || done.GetLabels()["node"] != b.mesh.Self() {
		t.Fatalf("task %v", done)
	}
	digest := held.GetArtifacts()[0].GetDigest()
	if !b.store.HasBlob(digest) {
		t.Fatal("beta holds the blob")
	}
	if _, err := b.store.ReadManifest("disk", "org/tiny", "Q4"); err != nil {
		t.Fatal(err)
	}
	if !hasLine(logs, "pulling org/tiny Q4 onto beta, task ") || !hasLine(logs, "beta: tiny-Q4.gguf verified "+digest+" from alpha") {
		t.Fatalf("the requesting task carries beta's log:\n%s", strings.Join(logs, "\n"))
	}
	eventually(t, "alpha sees beta holding the model", func() bool { return a.sees(b, "org/tiny", "Q4") })
	// Asked again, the member already holds it and the task says so
	task, err = conductor.PullTo(ctx, b.mesh.Self(), &v1.PullRequest{SourceId: "disk", Repo: "org/tiny", Group: "Q4"})
	done, logs = a.finish(t, ctx, task, err)
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED || !hasLine(logs, "org/tiny Q4 already on beta") {
		t.Fatalf("%s\n%s", done.GetError(), strings.Join(logs, "\n"))
	}
}
