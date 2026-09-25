package pull

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/all"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/mirror"
	"github.com/nickheyer/nebu/pkg/precision"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"github.com/nickheyer/nebu/pkg/transfer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Chunk size of the test fetcher, so a blob of a few dozen bytes lands in several chunks
const testChunk = 16

func fixture(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i*7 + i/13)
	}
	return data
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return store.Digest(hex.EncodeToString(sum[:]))
}

// A member in the test: the blobs it serves and the manifests it hands over
type member struct {
	name      string
	srv       *httptest.Server
	mu        sync.Mutex
	blobs     map[string][]byte
	manifests map[string]*v1.StoredModel
	// Ranges past this offset are cut mid transfer, zero serves whole
	cutAt int64
	// Every range the member was asked for, in order
	ranges []string
}

func newMember(t *testing.T, name string) *member {
	t.Helper()
	m := &member{name: name, blobs: map[string][]byte{}, manifests: map[string]*v1.StoredModel{}}
	m.srv = httptest.NewServer(m)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *member) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	digest := strings.TrimPrefix(r.URL.Path, "/blobs/")
	m.mu.Lock()
	data, ok := m.blobs[digest]
	cut := m.cutAt
	if r.Method == http.MethodGet {
		m.ranges = append(m.ranges, digest+" "+r.Header.Get("Range"))
	}
	m.mu.Unlock()
	if !ok {
		http.Error(w, "this node holds no blob "+digest, http.StatusNotFound)
		return
	}
	var start int64
	if rng := r.Header.Get("Range"); rng != "" {
		fmt.Sscanf(rng, "bytes=%d-", &start)
	}
	if cut > 0 && r.Method == http.MethodGet && start >= cut {
		// Declare the range and drop the connection before the bytes arrive
		w.Header().Set("Content-Length", strconv.Itoa(len(data)-int(start)))
		w.WriteHeader(http.StatusPartialContent)
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.(*net.TCPConn).SetLinger(0)
				conn.Close()
			}
		}
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, digest, time.Time{}, bytes.NewReader(data))
}

// Ranges the member was asked for
func (m *member) asked() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.ranges...)
}

// Gives the member a model: its manifest and, unless withheld, its blobs
func (m *member) hold(t *testing.T, source, repo, group string, files map[string][]byte, d *v1.Descriptor, withhold ...string) *v1.StoredModel {
	t.Helper()
	held := map[string]bool{}
	for _, w := range withhold {
		held[w] = true
	}
	dir := "/" + m.name + "/models/" + source + "/" + repo + "/" + group
	st := &v1.StoredModel{SourceId: source, Repo: repo, Revision: "main", Commit: "c0ffee", Group: group, FormatId: "gguf", Descriptor_: d, Path: dir, PulledAt: timestamppb.Now()}
	var paths []string
	for p := range files {
		paths = append(paths, p)
	}
	// Keep artifact order stable across runs
	for i := range paths {
		for j := i + 1; j < len(paths); j++ {
			if paths[j] < paths[i] {
				paths[i], paths[j] = paths[j], paths[i]
			}
		}
	}
	for _, p := range paths {
		data := files[p]
		digest := digestOf(data)
		if !held[p] {
			m.mu.Lock()
			m.blobs[digest] = data
			m.mu.Unlock()
		}
		st.Artifacts = append(st.Artifacts, &v1.StoredArtifact{Artifact: &v1.Artifact{Path: p, SizeBytes: uint64(len(data)), Sha256: store.Hex(digest)}, Digest: digest, Path: dir + "/" + p})
		st.Bytes += uint64(len(data))
	}
	m.mu.Lock()
	m.manifests[store.Key(source, repo, group)] = st
	m.mu.Unlock()
	return st
}

// What the member advertises: one summary per manifest, digested the way members do
func (m *member) summaries() []*v1.StoredSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*v1.StoredSummary
	for _, s := range m.manifests {
		out = append(out, &v1.StoredSummary{
			SourceId: s.GetSourceId(), Repo: s.GetRepo(), Revision: s.GetRevision(), Commit: s.GetCommit(), Group: s.GetGroup(), FormatId: s.GetFormatId(), Bytes: s.GetBytes(),
			Kind: s.GetDescriptor_().GetKind(), Architecture: s.GetDescriptor_().GetArchitecture(), DescriptorDigest: store.DescriptorDigest(s.GetDescriptor_()),
		})
	}
	return out
}

// The mesh as the puller sees it in tests: members in the order given, best link first
type fakeMesh struct {
	members []*member
	// Summaries advertised in place of a member's own, to stage a record behind its store
	advertise map[string][]*v1.StoredSummary
}

func (f *fakeMesh) holder(m *member) Holder {
	stored := m.summaries()
	if f.advertise != nil && f.advertise[m.name] != nil {
		stored = f.advertise[m.name]
	}
	return Holder{NodeID: m.name, Name: m.name, Base: m.srv.URL, HTTP: m.srv.Client(), Stored: stored}
}

func (f *fakeMesh) Holders(ctx context.Context, sourceID, repo, group string) []Holder {
	var out []Holder
	for _, m := range f.members {
		h := f.holder(m)
		for _, s := range h.Stored {
			if s.GetRepo() == repo && (group == "" || s.GetGroup() == group) && (sourceID == "" || s.GetSourceId() == sourceID) {
				out = append(out, h)
				break
			}
		}
	}
	return out
}

func (f *fakeMesh) Manifest(ctx context.Context, h Holder, sourceID, repo, group string) (*v1.StoredModel, error) {
	for _, m := range f.members {
		if m.name != h.NodeID {
			continue
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if st, ok := m.manifests[store.Key(sourceID, repo, group)]; ok {
			return st, nil
		}
		return nil, fmt.Errorf("%s holds no %s", m.name, store.Key(sourceID, repo, group))
	}
	return nil, errors.New("unknown member " + h.NodeID)
}

// A mirror on the internet: a repo index and its files, served with ranges
type mirrorSite struct {
	srv   *httptest.Server
	repo  string
	files map[string][]byte
	mu    sync.Mutex
	// Every range asked of the site, in order
	ranges []string
}

func newMirror(t *testing.T, repo string, files map[string][]byte) *mirrorSite {
	t.Helper()
	s := &mirrorSite{repo: repo, files: files}
	s.srv = httptest.NewServer(s)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *mirrorSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/"+s.repo+"/")
	if rel == mirror.IndexFile {
		idx := mirror.Index{Repo: s.repo, Revision: "main", Commit: "c0ffee"}
		for p, data := range s.files {
			idx.Files = append(idx.Files, mirror.File{Path: p, Size: uint64(len(data)), Sha256: store.Hex(digestOf(data))})
		}
		json.NewEncoder(w).Encode(idx)
		return
	}
	data, ok := s.files[rel]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		s.mu.Lock()
		s.ranges = append(s.ranges, rel+" "+r.Header.Get("Range"))
		s.mu.Unlock()
	}
	http.ServeContent(w, r, rel, time.Time{}, bytes.NewReader(data))
}

func (s *mirrorSite) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ranges...)
}

// A puller over a fresh store with the given sources as its internet
func testPuller(t *testing.T, ctx context.Context, srcs []*v1.Source) *Puller {
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
	reg, err := sources.Build(srcs)
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
	rts, err := runtimes.New(runtimes.All())
	if err != nil {
		t.Fatal(err)
	}
	memo, err := cache.Open(filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New()
	inspector := &inspect.Inspector{
		Sources:  reg,
		Formats:  fmts,
		Builder:  &descriptor.Builder{Formats: fmts, Archs: families, Scale: precision.Bits{}},
		Runtimes: rts,
		Host:     host.New(nil, nil, time.Minute),
		Cache:    memo,
		Stored:   blobs.ListManifests,
		Contexts: []uint32{4096},
		Log:      log,
	}
	return &Puller{
		Inspector: inspector,
		Store:     blobs,
		Fetcher:   &transfer.Fetcher{Workers: 1, Chunk: testChunk, Retries: 1, Log: log},
		Tasks:     tasks.New(ctx, log, database, bus),
		Events:    bus,
	}
}

// Runs a pull to its end and returns the task with its log
func finish(t *testing.T, ctx context.Context, p *Puller, task *v1.Task, err error) (*v1.Task, []string) {
	t.Helper()
	if err != nil {
		t.Fatalf("pull refused: %v", err)
	}
	done, err := p.Tasks.Wait(ctx, task.GetId())
	if err != nil {
		t.Fatal(err)
	}
	_, logs, err := p.Tasks.Get(task.GetId())
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

func llamaDescriptor(group string) *v1.Descriptor {
	return &v1.Descriptor{FormatId: "gguf", Group: group, Architecture: "llama", Kind: v1.ModelKind_MODEL_KIND_LANGUAGE, ParameterCount: 42, Params: map[string]float64{"n_layer": 2}}
}

// A request naming no group takes the one group members hold, a repo held as several must be
// named, and a revision picks the members holding it
func TestMeshHoldersNameTheGroup(t *testing.T) {
	ctx := context.Background()
	a := newMember(t, "a")
	a.hold(t, "hf", "org/model", "Q4", map[string][]byte{"m.gguf": fixture(40)}, llamaDescriptor("Q4"))
	p := testPuller(t, ctx, nil)
	p.Mesh = &fakeMesh{members: []*member{a}}
	named, holders, err := p.meshHolders(ctx, &v1.PullRequest{Repo: "org/model"})
	if err != nil || len(holders) != 1 || named.GetGroup() != "Q4" || named.GetSourceId() != "hf" {
		t.Fatalf("named %v holders %d err %v", named, len(holders), err)
	}
	if _, holders, err := p.meshHolders(ctx, &v1.PullRequest{Repo: "org/model", Revision: "v2"}); err != nil || len(holders) != 0 {
		t.Fatalf("a revision nobody holds finds no holder: %d %v", len(holders), err)
	}
	if _, holders, err := p.meshHolders(ctx, &v1.PullRequest{Repo: "org/model", Revision: "c0ffee"}); err != nil || len(holders) != 1 {
		t.Fatalf("a revision matches by commit: %d %v", len(holders), err)
	}
	a.hold(t, "hf", "org/model", "Q8", map[string][]byte{"m.gguf": fixture(48)}, llamaDescriptor("Q8"))
	_, _, err = p.meshHolders(ctx, &v1.PullRequest{Repo: "org/model"})
	if !errors.Is(err, formats.ErrUnknownGroup) || !strings.Contains(err.Error(), "hf Q4, hf Q8") {
		t.Fatalf("two groups must be named: %v", err)
	}
	named, holders, err = p.meshHolders(ctx, &v1.PullRequest{Repo: "org/model", Group: "Q8"})
	if err != nil || len(holders) != 1 || named.GetGroup() != "Q8" {
		t.Fatalf("named group %v %d %v", named, len(holders), err)
	}
}

// A pull lands from the member holding the model, with the source silent, and copies the manifest
// with its descriptor so the receiving node's planner sees the model
func TestPullLandsFromTheMemberBeforeTheInternet(t *testing.T) {
	ctx := context.Background()
	a := newMember(t, "a")
	files := map[string][]byte{"m.gguf": fixture(40), "config.json": []byte(`{"arch":"llama"}`)}
	held := a.hold(t, "disk", "org/model", "Q4", files, llamaDescriptor("Q4"))
	p := testPuller(t, ctx, []*v1.Source{{Id: "disk", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": t.TempDir()}}})
	p.Mesh = &fakeMesh{members: []*member{a}}
	task, err := p.Pull(ctx, &v1.PullRequest{Repo: "org/model"})
	done, logs := finish(t, ctx, p, task, err)
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("%s: %s\n%s", done.GetState(), done.GetError(), strings.Join(logs, "\n"))
	}
	if done.GetLabels()["from"] != "a" || !strings.HasPrefix(done.GetTitle(), "pull org/model Q4 from a") {
		t.Fatalf("task %v", done)
	}
	for _, sa := range held.GetArtifacts() {
		if !p.Store.HasBlob(sa.GetDigest()) || !hasLine(logs, sa.GetArtifact().GetPath()+" verified "+sa.GetDigest()+" from a") {
			t.Fatalf("%s came from a:\n%s", sa.GetArtifact().GetPath(), strings.Join(logs, "\n"))
		}
	}
	if !hasLine(logs, "disk did not answer") {
		t.Fatalf("the silent source is noted:\n%s", strings.Join(logs, "\n"))
	}
	m, err := p.Store.ReadManifest("disk", "org/model", "Q4")
	if err != nil {
		t.Fatal(err)
	}
	if store.DescriptorDigest(m.GetDescriptor_()) != store.DescriptorDigest(held.GetDescriptor_()) || m.GetRuntimes() == 0 || len(m.GetArtifacts()) != 2 || m.GetBytes() != held.GetBytes() {
		t.Fatalf("manifest %v", m)
	}
	for _, sa := range m.GetArtifacts() {
		data, err := os.ReadFile(sa.GetPath())
		if err != nil || !bytes.Equal(data, files[sa.GetArtifact().GetPath()]) {
			t.Fatalf("%s links its blob: %v", sa.GetPath(), err)
		}
	}
	st, _ := p.Store.Status()
	if st.GetPartials() != 0 {
		t.Fatalf("no partial is left behind: %+v", st)
	}
}

// A repair pull after verify takes the blobs the store lost from a member, keeping the model's
// local usage time
func TestRepairPullTakesMissingBlobsFromTheMesh(t *testing.T) {
	ctx := context.Background()
	a := newMember(t, "a")
	files := map[string][]byte{"m.gguf": fixture(40), "config.json": []byte(`{}`)}
	a.hold(t, "disk", "org/model", "Q4", files, llamaDescriptor("Q4"))
	p := testPuller(t, ctx, []*v1.Source{{Id: "disk", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": t.TempDir()}}})
	p.Mesh = &fakeMesh{members: []*member{a}}
	task, err := p.Pull(ctx, &v1.PullRequest{Repo: "org/model", Group: "Q4"})
	if done, logs := finish(t, ctx, p, task, err); done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("first pull: %s\n%s", done.GetError(), strings.Join(logs, "\n"))
	}
	used := timestamppb.New(time.Now().Add(-time.Hour).Truncate(time.Second))
	m, _ := p.Store.ReadManifest("disk", "org/model", "Q4")
	m.UsedAt = used
	if err := p.Store.WriteManifest(m); err != nil {
		t.Fatal(err)
	}
	lost := digestOf(files["m.gguf"])
	if err := p.Store.RemoveBlob(lost); err != nil {
		t.Fatal(err)
	}
	task, err = p.Pull(ctx, &v1.PullRequest{Repo: "org/model", Group: "Q4"})
	done, logs := finish(t, ctx, p, task, err)
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("repair: %s\n%s", done.GetError(), strings.Join(logs, "\n"))
	}
	if !p.Store.HasBlob(lost) || !hasLine(logs, "m.gguf verified "+lost+" from a") || !hasLine(logs, "config.json already stored") {
		t.Fatalf("the lost blob comes from a and the other stays:\n%s", strings.Join(logs, "\n"))
	}
	m, _ = p.Store.ReadManifest("disk", "org/model", "Q4")
	if !m.GetUsedAt().AsTime().Equal(used.AsTime()) {
		t.Fatalf("usage time kept: %v", m.GetUsedAt())
	}
}

// Blobs come from members in link order, the first that serves each, and from the internet when
// none does; the log names the source of every blob
func TestBlobsFallToTheNextMemberThenTheInternet(t *testing.T) {
	ctx := context.Background()
	first, second, third := "tiny-Q4_K_M-00001-of-00003.gguf", "tiny-Q4_K_M-00002-of-00003.gguf", "tiny-Q4_K_M-00003-of-00003.gguf"
	files := map[string][]byte{first: fixture(40), second: fixture(41), third: fixture(42)}
	site := newMirror(t, "org/model", files)
	a := newMember(t, "a")
	a.hold(t, "mirror", "org/model", "Q4_K_M", files, llamaDescriptor("Q4_K_M"), second, third)
	b := newMember(t, "b")
	b.hold(t, "mirror", "org/model", "Q4_K_M", files, llamaDescriptor("Q4_K_M"), third)
	p := testPuller(t, ctx, []*v1.Source{{Id: "mirror", Kind: v1.SourceKind_SOURCE_KIND_MIRROR, Config: map[string]string{"endpoint": site.srv.URL}}})
	p.Mesh = &fakeMesh{members: []*member{a, b}}
	task, err := p.Pull(ctx, &v1.PullRequest{SourceId: "mirror", Repo: "org/model", Group: "Q4_K_M", Alone: true})
	done, logs := finish(t, ctx, p, task, err)
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("%s\n%s", done.GetError(), strings.Join(logs, "\n"))
	}
	want := []string{
		"pulling org/model Q4_K_M from a, b",
		first + " verified " + digestOf(files[first]) + " from a",
		second + ": a does not serve it",
		second + " verified " + digestOf(files[second]) + " from b",
		third + ": no member served it (a, b), fetching from mirror",
		third + " verified " + digestOf(files[third]) + " from mirror",
	}
	for _, w := range want {
		if !hasLine(logs, w) {
			t.Fatalf("missing %q in\n%s", w, strings.Join(logs, "\n"))
		}
	}
	for p2, data := range files {
		if !p.Store.HasBlob(digestOf(data)) {
			t.Fatalf("%s landed", p2)
		}
	}
	asked := site.asked()
	if len(asked) != 3 {
		t.Fatalf("the internet is asked for the chunks of the one blob no member served: %v", asked)
	}
	for _, rng := range asked {
		if !strings.HasPrefix(rng, third+" ") {
			t.Fatalf("the internet is asked for the one blob no member served: %v", asked)
		}
	}
	for _, rng := range a.asked() {
		if !strings.HasPrefix(rng, digestOf(files[first])+" ") {
			t.Fatalf("a serves the one blob it holds and is not asked for the rest: %v", a.asked())
		}
	}
}

// With no member serving a blob and the source silent, the pull fails naming both
func TestBlobNobodyServesFailsThePull(t *testing.T) {
	ctx := context.Background()
	a := newMember(t, "a")
	a.hold(t, "disk", "org/model", "Q4", map[string][]byte{"m.gguf": fixture(40)}, llamaDescriptor("Q4"), "m.gguf")
	p := testPuller(t, ctx, []*v1.Source{{Id: "disk", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": t.TempDir()}}})
	p.Mesh = &fakeMesh{members: []*member{a}}
	task, err := p.Pull(ctx, &v1.PullRequest{Repo: "org/model", Alone: true})
	done, logs := finish(t, ctx, p, task, err)
	if done.GetState() != v1.TaskState_TASK_STATE_FAILED || !strings.Contains(done.GetError(), "no member served it (a) and disk did not answer") {
		t.Fatalf("%s %q\n%s", done.GetState(), done.GetError(), strings.Join(logs, "\n"))
	}
}

// A transfer interrupted between members resumes on the internet from the chunks it landed, both
// paths keeping the partial under the blob's digest
func TestInterruptedMeshTransferResumesOnTheInternet(t *testing.T) {
	ctx := context.Background()
	files := map[string][]byte{"tiny-Q4_K_M.gguf": fixture(3*testChunk + 5)}
	site := newMirror(t, "org/model", files)
	a := newMember(t, "a")
	a.hold(t, "mirror", "org/model", "Q4_K_M", files, llamaDescriptor("Q4_K_M"))
	a.cutAt = testChunk
	p := testPuller(t, ctx, []*v1.Source{{Id: "mirror", Kind: v1.SourceKind_SOURCE_KIND_MIRROR, Config: map[string]string{"endpoint": site.srv.URL}}})
	p.Mesh = &fakeMesh{members: []*member{a}}
	digest := digestOf(files["tiny-Q4_K_M.gguf"])
	if partialKey(&v1.Model{SourceId: "mirror", Repo: "org/model"}, &v1.Artifact{Path: "tiny-Q4_K_M.gguf", Sha256: store.Hex(digest)}) != blobKey(digest) {
		t.Fatal("internet and mesh partials share one key per digest")
	}
	task, err := p.Pull(ctx, &v1.PullRequest{SourceId: "mirror", Repo: "org/model", Group: "Q4_K_M", Alone: true})
	done, logs := finish(t, ctx, p, task, err)
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("%s\n%s", done.GetError(), strings.Join(logs, "\n"))
	}
	if !p.Store.HasBlob(digest) || !hasLine(logs, "tiny-Q4_K_M.gguf verified "+digest+" from mirror") || !hasLine(logs, "tiny-Q4_K_M.gguf from a:") {
		t.Fatalf("the blob finishes from the mirror after a cut it off:\n%s", strings.Join(logs, "\n"))
	}
	asked := site.asked()
	if len(asked) != 3 {
		t.Fatalf("the mirror is asked for the chunks a did not deliver: %v", asked)
	}
	for _, rng := range asked {
		if strings.HasSuffix(rng, " bytes=0-15") {
			t.Fatalf("the first chunk landed from a is not fetched again: %v", asked)
		}
	}
	if done.GetProgress().GetDone() != done.GetProgress().GetTotal() {
		t.Fatalf("progress ends at the total: %v", done.GetProgress())
	}
}

// A part wanted as selected files lands from a member holding them, as the component the plan
// names, and a member missing any of them is passed over
func TestSelectedFilesLandFromTheMember(t *testing.T) {
	ctx := context.Background()
	files := map[string][]byte{"tokenizer.json": []byte(`{"tok":1}`), "tokenizer_config.json": []byte(`{"cfg":1}`), "model.safetensors": fixture(40)}
	short := newMember(t, "short")
	short.hold(t, "hf", "org/enc", "root", map[string][]byte{"tokenizer.json": files["tokenizer.json"], "model.safetensors": files["model.safetensors"]}, nil)
	full := newMember(t, "full")
	full.hold(t, "hf", "org/enc", "root", files, nil)
	p := testPuller(t, ctx, nil)
	p.Mesh = &fakeMesh{members: []*member{short, full}}
	wanted := []*v1.Artifact{{Path: "tokenizer.json", SizeBytes: uint64(len(files["tokenizer.json"]))}, {Path: "tokenizer_config.json", SizeBytes: uint64(len(files["tokenizer_config.json"]))}}
	var notes []string
	l, err := p.meshLanding(ctx, p.Mesh.Holders(ctx, "hf", "org/enc", "root"), "hf", "org/enc", "root", wanted, "text_encoder.tokenizer", &notes)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "short holds org/enc root without every file wanted") {
		t.Fatalf("the member missing a file is passed over: %v", notes)
	}
	if len(l.artifacts()) != 2 {
		t.Fatalf("only the selected files land: %v", l.artifacts())
	}
	task := p.Tasks.Start("pull", "test", nil, func(ctx context.Context, h *tasks.Handle) error {
		h.Progress(0, 1, "fetching")
		return p.landMesh(ctx, h, l)
	})
	done, logs := finish(t, ctx, p, task, nil)
	if done.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatalf("%s\n%s", done.GetError(), strings.Join(logs, "\n"))
	}
	m, err := p.Store.ReadManifest("hf", "org/enc", "root")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.GetArtifacts()) != 2 || m.GetDescriptor_().GetKind() != v1.ModelKind_MODEL_KIND_COMPONENT || m.GetDescriptor_().GetArchitecture() != "tokenizer" || m.GetRuntimes() != 0 {
		t.Fatalf("component manifest %v", m)
	}
	if p.Store.HasBlob(digestOf(files["model.safetensors"])) {
		t.Fatal("the unselected weights do not land")
	}
	if !hasLine(logs, "tokenizer_config.json verified "+digestOf(files["tokenizer_config.json"])+" from full") {
		t.Fatalf("selected files come from the member holding them:\n%s", strings.Join(logs, "\n"))
	}
	if _, err := p.meshLanding(ctx, p.Mesh.Holders(ctx, "hf", "org/enc", "root")[:1], "hf", "org/enc", "root", wanted, "text_encoder.tokenizer", &notes); err == nil {
		t.Fatal("no member holding every selected file is an error")
	}
}

// A member whose record advertises another descriptor than its manifest carries is passed over
// until it syncs, and a matching one is taken whatever build made the digest
func TestDigestMatchesCatchesARecordBehindItsStore(t *testing.T) {
	ctx := context.Background()
	a := newMember(t, "a")
	a.hold(t, "hf", "org/model", "Q4", map[string][]byte{"m.gguf": fixture(40)}, llamaDescriptor("Q4"))
	mesh := &fakeMesh{members: []*member{a}}
	h := mesh.holder(a)
	m, _ := mesh.Manifest(ctx, h, "hf", "org/model", "Q4")
	if err := digestMatches(h, m); err != nil {
		t.Fatal(err)
	}
	stale := llamaDescriptor("Q4")
	stale.ParameterCount = 41
	mesh.advertise = map[string][]*v1.StoredSummary{"a": {{SourceId: "hf", Repo: "org/model", Group: "Q4", DescriptorDigest: store.DescriptorDigest(stale)}}}
	h = mesh.holder(a)
	err := digestMatches(h, m)
	if err == nil || !strings.Contains(err.Error(), "record is behind its store") {
		t.Fatalf("stale record: %v", err)
	}
	p := testPuller(t, ctx, nil)
	p.Mesh = mesh
	var notes []string
	if _, err := p.meshLanding(ctx, mesh.Holders(ctx, "hf", "org/model", "Q4"), "hf", "org/model", "Q4", nil, "", &notes); err == nil || !strings.Contains(err.Error(), "behind its store") {
		t.Fatalf("the only holder behind its store fails the landing: %v", err)
	}
	mesh.advertise = map[string][]*v1.StoredSummary{"a": {{SourceId: "hf", Repo: "org/model", Group: "Q4"}}}
	m.Descriptor_ = nil
	if err := digestMatches(mesh.holder(a), m); err != nil {
		t.Fatalf("no descriptor on either side matches: %v", err)
	}
}
