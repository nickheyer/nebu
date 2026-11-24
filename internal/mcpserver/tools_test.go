package mcpserver

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/inspect"
	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/internal/instances"
	"github.com/nickheyer/nebu/internal/instances/fake"
	"github.com/nickheyer/nebu/internal/slots"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/formats/all"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/precision"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A llama GGUF of two layers trained for 4096 tokens, headers and tensor data
func ggufFixture() []byte {
	var b bytes.Buffer
	u32 := func(v uint32) { binary.Write(&b, binary.LittleEndian, v) }
	u64 := func(v uint64) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) {
		u64(uint64(len(s)))
		b.WriteString(s)
	}
	tensors := []struct {
		name string
		dims []uint64
	}{
		{"token_embd.weight", []uint64{64, 100}},
		{"blk.0.attn_q.weight", []uint64{64, 64}},
		{"blk.1.attn_q.weight", []uint64{64, 64}},
		{"output.weight", []uint64{64, 100}},
	}
	b.WriteString("GGUF")
	u32(3)
	u64(uint64(len(tensors)))
	kv := [][2]any{
		{"general.architecture", "llama"},
		{"general.type", "model"},
		{"llama.block_count", uint32(2)},
		{"llama.embedding_length", uint32(64)},
		{"llama.attention.head_count", uint32(4)},
		{"llama.attention.head_count_kv", uint32(2)},
		{"llama.context_length", uint32(4096)},
	}
	u64(uint64(len(kv)))
	for _, pair := range kv {
		str(pair[0].(string))
		switch v := pair[1].(type) {
		case string:
			u32(8)
			str(v)
		case uint32:
			u32(4)
			u32(v)
		}
	}
	var offset uint64
	for _, t := range tensors {
		str(t.name)
		u32(uint32(len(t.dims)))
		n := uint64(1)
		for _, d := range t.dims {
			u64(d)
			n *= d
		}
		u32(0)
		u64(offset)
		offset += n * 4
	}
	for b.Len()%32 != 0 {
		b.WriteByte(0)
	}
	b.Write(make([]byte, offset))
	return b.Bytes()
}

// A host with one roomy GPU, so every plan fits
type cardProbe struct{}

func (cardProbe) ID() string               { return "card" }
func (cardProbe) Description() string      { return "a card for tests" }
func (cardProbe) Runs(string, string) bool { return true }
func (cardProbe) Run(context.Context) host.Result {
	const gib = 1 << 30
	return host.Result{
		Status:  v1.ProbeStatus_PROBE_STATUS_OK,
		Devices: []*v1.Device{{Id: "g0", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: "nvidia", Name: "Card", MemoryTotalBytes: 24 * gib, MemoryFreeBytes: 20 * gib}},
		Pools: []*v1.MemoryPool{
			{Id: "g0", Kind: v1.PoolKind_POOL_KIND_DEVICE, DeviceId: "g0", TotalBytes: 24 * gib, FreeBytes: 20 * gib},
			{Id: "host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: 64 * gib, FreeBytes: 60 * gib},
		},
	}
}

// A source that answers a model card too, which a directory has none of
type carded struct{ sources.Source }

func (carded) Card(_ context.Context, repo, _ string) (*v1.ModelCard, error) {
	return &v1.ModelCard{Markdown: "# " + repo}, nil
}

// Fills the deps a node's model, runtime, instance and slot tools need: a directory source holding one GGUF, the fake runtime installed with one stored model, and slots over both
func node(t *testing.T) func(context.Context, *Deps) {
	return func(ctx context.Context, d *Deps) {
		t.Helper()
		dir := t.TempDir()
		repo := filepath.Join(dir, "repos", "org", "model")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "tiny-Q4_K_M.gguf"), ggufFixture(), 0o644); err != nil {
			t.Fatal(err)
		}
		built, err := sources.Build([]*v1.Source{{Id: "disk", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": filepath.Join(dir, "repos")}}})
		if err != nil {
			t.Fatal(err)
		}
		disk, err := built.Get("disk")
		if err != nil {
			t.Fatal(err)
		}
		reg := sources.NewRegistry(carded{disk})
		fmts, err := all.Registry()
		if err != nil {
			t.Fatal(err)
		}
		families, err := archs.New(archs.All())
		if err != nil {
			t.Fatal(err)
		}
		rts, err := runtimes.New(append(runtimes.All(), fake.Runtime{}))
		if err != nil {
			t.Fatal(err)
		}
		headers, err := cache.Open(filepath.Join(dir, "cache"))
		if err != nil {
			t.Fatal(err)
		}
		database, err := db.Open(filepath.Join(dir, "node.db"))
		if err != nil {
			t.Fatal(err)
		}
		blobs, err := store.Open(filepath.Join(dir, "store"))
		if err != nil {
			t.Fatal(err)
		}
		if err := database.PutInstall(ctx, &v1.Install{Id: db.NewID(), RuntimeId: fake.RuntimeID, Kind: v1.InstallKind_INSTALL_KIND_ADOPTED, Path: filepath.Join(dir, "fake"), Dir: dir, Version: "1", CreatedAt: timestamppb.Now()}); err != nil {
			t.Fatal(err)
		}
		weights := filepath.Join(dir, "weights.fake")
		if err := os.WriteFile(weights, []byte("weights"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := blobs.WriteManifest(&v1.StoredModel{SourceId: "src", Repo: "org/stored", Group: "q4", FormatId: fake.Format, Path: dir, Descriptor_: &v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_LANGUAGE, Architecture: "fake", Metadata: map[string]string{descriptor.RevisionKey: descriptor.Revision}}, Artifacts: []*v1.StoredArtifact{{Digest: "sha256:m", Path: weights, Artifact: &v1.Artifact{Path: "weights.fake", Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS}}}}); err != nil {
			t.Fatal(err)
		}
		prober := host.New([]host.Probe{cardProbe{}}, nil, time.Minute)
		work := tasks.New(ctx, d.Log, database, d.Events)
		insp := &inspect.Inspector{Sources: reg, Formats: fmts, Builder: &descriptor.Builder{Formats: fmts, Archs: families, Scale: precision.Bits{}}, Stored: blobs.ListManifests, Runtimes: rts, Host: prober, Cache: headers, Contexts: []uint32{8192}, Log: d.Log}
		inst := &installs.Manager{DB: database, Runtimes: rts, Host: prober, Tasks: work, Events: d.Events, Log: d.Log}
		launcher, registrar, traffic := fake.New(), fake.NewRegistrar(), fake.NewTraffic()
		running := &instances.Manager{
			DB: database, Dir: filepath.Join(dir, "instances"), Store: blobs, Runtimes: rts, Installs: inst, Inspector: insp, Launcher: launcher, Tasks: work, Host: prober, Registrar: registrar,
			InFlight: traffic.InFlight, Dispatched: traffic.Dispatched, Load: traffic.Load, Listening: launcher.Listening, DrainTimeout: 5 * time.Second, Events: d.Events, Log: d.Log,
		}
		held := &slots.Manager{DB: database, Instances: running, Registrar: registrar, Routes: registrar, Tasks: work, Host: prober, Events: d.Events, Log: d.Log}
		running.Slots = held
		if err := held.Load(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			running.Close()
			database.Close()
		})
		d.Host, d.Sources, d.Inspector, d.Formats, d.Runtimes, d.Store, d.Tasks, d.Installs, d.Instances, d.Slots = prober, &sources.Manager{Registry: reg}, insp, fmts, rts, blobs, work, inst, running, held
	}
}

// The card, the weight groups and the fit table of a repository answer as their services do
func TestModelTools(t *testing.T) {
	cs := poolSession(t, node(t))
	ref := map[string]any{"source_id": "disk", "repo": "org/model"}
	if card := call(t, cs, "get_model_card", ref, "")["card"].(map[string]any); card["markdown"] != "# org/model" {
		t.Fatalf("card %v", card)
	}
	call(t, cs, "get_model_card", map[string]any{"source_id": "nowhere", "repo": "org/model"}, "nowhere")
	files := call(t, cs, "resolve_model", ref, "")["model"].(map[string]any)["artifacts"].([]any)
	if file := files[0].(map[string]any); len(files) != 1 || file["group"] != "Q4_K_M" || file["format_id"] != "gguf" {
		t.Fatalf("artifacts %v", files)
	}
	fit := call(t, cs, "inspect_model", map[string]any{"source_id": "disk", "repo": "org/model", "groups": []string{"Q4_K_M"}, "runtime_ids": []string{"llamacpp"}, "contexts": []int{2048}}, "")
	rows := fit["rows"].([]any)
	if row := rows[0].(map[string]any); len(rows) != 1 || row["group"] != "Q4_K_M" || row["runtime_id"] != "llamacpp" || row["context"] != float64(2048) || row["plan"].(map[string]any)["verdict"] != "FIT_VERDICT_FITS" || row["free"].(map[string]any)["verdict"] != "FIT_VERDICT_FITS" {
		t.Fatalf("rows %v", rows)
	}
	if none := call(t, cs, "inspect_model", map[string]any{"source_id": "disk", "repo": "org/model", "groups": []string{"Q8_0"}}, ""); none["rows"] != nil {
		t.Fatalf("a group the repository lacks was planned: %v", none["rows"])
	}
}

// A runtime answers with its params and install methods, and an install takes the settings its method lists and no other
func TestRuntimeTools(t *testing.T) {
	cs := poolSession(t, node(t))
	status := call(t, cs, "get_runtime", map[string]any{"id": fake.RuntimeID}, "")["runtime"].(map[string]any)
	if method := status["installs"].([]any)[0].(map[string]any); method["method"].(map[string]any)["id"] != "adopt" || method["fields"].([]any)[0].(map[string]any)["name"] != "path" {
		t.Fatalf("installs %v", status["installs"])
	}
	if params := status["runtime"].(map[string]any)["params"].([]any); len(params) != len(fake.Runtime{}.Params()) {
		t.Fatalf("params %v", params)
	}
	call(t, cs, "get_runtime", map[string]any{"id": "nope"}, "unknown runtime")
	call(t, cs, "install_runtime", map[string]any{"runtime_id": fake.RuntimeID, "method": "adopt", "settings": map[string]string{"speed": "fast"}}, `takes no setting "speed"`)
}

// A slot is created with its settings, changed one setting at a time, served in, read by its log, and deleted only by force while it serves
func TestSlotTools(t *testing.T) {
	cs := poolSession(t, node(t))
	slot := call(t, cs, "create_slot", map[string]any{"name": "main", "description": "the main slot", "runtime_id": fake.RuntimeID, "params": map[string]string{fake.ParamMaxSeqs: "4"}, "placement": "device", "device_ids": []string{"g0"}, "pool_bytes": map[string]int{"g0": 1 << 30}, "policy": map[string]any{"max_in_flight": 2}, "aliases": []map[string]any{{"name": "chat"}}}, "")["slot"].(map[string]any)
	if slot["name"] != "main" || slot["runtime_id"] != fake.RuntimeID || slot["placement"] != "PLACEMENT_DEVICE" || slot["params"].(map[string]any)[fake.ParamMaxSeqs] != "4" || slot["pool_bytes"].(map[string]any)["g0"] != "1073741824" || slot["policy"].(map[string]any)["max_in_flight"] != float64(2) || slot["aliases"].([]any)[0].(map[string]any)["name"] != "chat" || slot["state"] != "SLOT_STATE_EMPTY" {
		t.Fatalf("created %v", slot)
	}
	call(t, cs, "create_slot", map[string]any{"name": "side", "placement": "sideways"}, "must be auto, device, or host")
	call(t, cs, "create_slot", map[string]any{"name": "side", "device_ids": []string{"g9"}}, "g9")
	// A change names one setting and the rest stay
	slot = call(t, cs, "update_slot", map[string]any{"id": "main", "description": "the first slot"}, "")["slot"].(map[string]any)
	if slot["description"] != "the first slot" || slot["runtime_id"] != fake.RuntimeID || slot["placement"] != "PLACEMENT_DEVICE" || slot["params"].(map[string]any)[fake.ParamMaxSeqs] != "4" || slot["device_ids"].([]any)[0] != "g0" || slot["policy"].(map[string]any)["max_in_flight"] != float64(2) || len(slot["aliases"].([]any)) != 1 {
		t.Fatalf("updated %v", slot)
	}
	// A setting named empty is cleared, and a name renames
	slot = call(t, cs, "update_slot", map[string]any{"id": "main", "name": "first", "description": "", "placement": "auto", "aliases": []any{}, "params": map[string]string{}}, "")["slot"].(map[string]any)
	if slot["name"] != "first" || slot["description"] != nil || slot["placement"] != nil || slot["aliases"] != nil || slot["params"] != nil || slot["runtime_id"] != fake.RuntimeID {
		t.Fatalf("cleared %v", slot)
	}
	call(t, cs, "update_slot", map[string]any{"id": "nope", "description": "x"}, "nope")
	call(t, cs, "instance_logs", map[string]any{"id": "first"}, `unknown instance "first"`)
	call(t, cs, "run_model", map[string]any{"source_id": "src", "repo": "org/stored", "group": "q4", "slot_id": "first"}, "")
	deadline := time.Now().Add(30 * time.Second)
	for call(t, cs, "list_slots", map[string]any{}, "")["slots"].([]any)[0].(map[string]any)["state"] != "SLOT_STATE_READY" {
		if time.Now().After(deadline) {
			t.Fatal("the slot never served")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lines := call(t, cs, "instance_logs", map[string]any{"id": "first", "tail": 1}, "")["lines"].([]any); len(lines) != 1 {
		t.Fatalf("log tail %v", lines)
	}
	call(t, cs, "delete_slot", map[string]any{"id": "first"}, "evict it")
	if gone := call(t, cs, "delete_slot", map[string]any{"id": "first", "force": true}, "")["slot"].(map[string]any); gone["name"] != "first" {
		t.Fatalf("deleted %v", gone)
	}
	if left := call(t, cs, "list_slots", map[string]any{}, ""); len(left) != 0 {
		t.Fatalf("slots left %v", left)
	}
}

// The new tools say which of them read, which change, and which destroy
func TestToolAnnotations(t *testing.T) {
	cs := poolSession(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	hints := map[string]*mcp.ToolAnnotations{}
	for _, tool := range res.Tools {
		hints[tool.Name] = tool.Annotations
	}
	for _, name := range []string{"get_model_card", "resolve_model", "inspect_model", "get_runtime", "instance_logs"} {
		if a, ok := hints[name]; !ok || a == nil || !a.ReadOnlyHint {
			t.Fatalf("%s is not listed as read only: %v", name, a)
		}
	}
	for _, name := range []string{"create_slot", "update_slot", "install_runtime"} {
		if a, ok := hints[name]; !ok || a != nil {
			t.Fatalf("%s is not listed as a plain change: %v", name, a)
		}
	}
	if a := hints["delete_slot"]; a == nil || a.DestructiveHint == nil || !*a.DestructiveHint || a.ReadOnlyHint {
		t.Fatalf("delete_slot is not listed as destructive: %v", a)
	}
}
