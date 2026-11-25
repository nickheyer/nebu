package mcpserver

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
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
	"github.com/nickheyer/nebu/pkg/build"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/formats/all"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/precision"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/recipes"
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

// A recipe of the fake runtime that writes a script, with no source to fetch
type scriptRecipe struct{}

func (scriptRecipe) ID() string             { return "script" }
func (scriptRecipe) RuntimeID() string      { return fake.RuntimeID }
func (scriptRecipe) Description() string    { return "Writes a script" }
func (scriptRecipe) Source() recipes.Source { return recipes.Source{} }
func (scriptRecipe) Tools() []string        { return []string{"sh"} }
func (scriptRecipe) Facts() []string        { return nil }
func (scriptRecipe) Vars() []recipes.Var {
	return []recipes.Var{{Name: "greeting", Label: "Greeting", Default: "hello", Description: "The word the script prints"}}
}
func (scriptRecipe) Variants() []recipes.Variant { return nil }
func (scriptRecipe) Sandbox() recipes.Sandbox {
	return recipes.Sandbox{Kind: v1.SandboxKind_SANDBOX_KIND_HOST}
}
func (scriptRecipe) Outputs() []string        { return nil }
func (scriptRecipe) Binary() string           { return "served" }
func (scriptRecipe) Timeout() time.Duration   { return time.Minute }
func (scriptRecipe) Patches() []recipes.Patch { return nil }
func (scriptRecipe) Steps(b *recipes.Build) []recipes.Step {
	return []recipes.Step{{Name: "write", Command: []string{"sh", "-c", "echo 'echo " + b.Vars["greeting"] + "' > " + b.Out + "/served"}}}
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
		book, err := build.New([]recipes.Recipe{scriptRecipe{}})
		if err != nil {
			t.Fatal(err)
		}
		inst := &installs.Manager{DB: database, Runtimes: rts, Recipes: book, Engine: &build.Engine{Root: filepath.Join(dir, "builds"), Log: d.Log}, Defaults: &v1.Builds{}, Host: prober, Tasks: work, Events: d.Events, Log: d.Log}
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
	// The launch params are a tool of their own, so a runtime answers without them unless it is asked whole
	if params, held := status["runtime"].(map[string]any)["params"]; held {
		t.Fatalf("a runtime answered its params unasked: %v", params)
	}
	whole := call(t, cs, "get_runtime", map[string]any{"id": fake.RuntimeID, "full": true}, "")["runtime"].(map[string]any)
	if params := whole["runtime"].(map[string]any)["params"].([]any); len(params) != len(fake.Runtime{}.Params()) {
		t.Fatalf("params %v", params)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_params", Arguments: map[string]any{"runtime_id": "llamacpp", "search": "moe"}})
	if err != nil || res.IsError {
		t.Fatalf("list_params: %v %v", res, err)
	}
	if lines := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(lines, "\nmoe_cache_mib (--moe-cache-mib, default 0): ") || !strings.Contains(lines, "\nn_cpu_moe (--n-cpu-moe, default auto): ") || strings.Contains(lines, "\nn_ctx ") {
		t.Fatalf("params holding moe: %s", lines)
	}
	if named := call(t, cs, "list_params", map[string]any{"runtime_id": "llamacpp", "names": []string{"n_ubatch"}}, "")["params"].([]any); len(named) != 1 || named[0].(map[string]any)["flag"] != "--ubatch-size" {
		t.Fatalf("a named param whole: %v", named)
	}
	call(t, cs, "list_params", map[string]any{"runtime_id": "llamacpp", "search": "no such thing"}, "goes in args")
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
	// A param named is set beside the ones held, one named empty is removed, and the record answers without what the UI alone reads
	slot = call(t, cs, "update_slot", map[string]any{"id": "main", "params": map[string]string{fake.ParamTransport: "tcp"}}, "")["slot"].(map[string]any)
	if held := slot["params"].(map[string]any); held[fake.ParamMaxSeqs] != "4" || held[fake.ParamTransport] != "tcp" || slot["conditions"] != nil || slot["meta"] != nil {
		t.Fatalf("one param set %v", slot)
	}
	slot = call(t, cs, "update_slot", map[string]any{"id": "main", "params": map[string]string{fake.ParamTransport: ""}}, "")["slot"].(map[string]any)
	if held := slot["params"].(map[string]any); len(held) != 1 || held[fake.ParamMaxSeqs] != "4" {
		t.Fatalf("one param removed %v", slot)
	}
	if whole := call(t, cs, "get_slot", map[string]any{"id": "main", "full": true}, "")["slot"].(map[string]any); whole["conditions"] == nil {
		t.Fatalf("a slot asked for whole %v", whole)
	}
	// A setting named empty is cleared, and a name renames
	slot = call(t, cs, "update_slot", map[string]any{"id": "main", "name": "first", "description": "", "placement": "auto", "aliases": []any{}, "reset_params": true}, "")["slot"].(map[string]any)
	if slot["name"] != "first" || slot["description"] != nil || slot["placement"] != nil || slot["aliases"] != nil || slot["params"] != nil || slot["runtime_id"] != fake.RuntimeID {
		t.Fatalf("cleared %v", slot)
	}
	call(t, cs, "relaunch_slot", map[string]any{"id": "first"}, "holds no model")
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
	if lines := call(t, cs, "instance_logs", map[string]any{"id": "first", "match": "WEIGHTS="}, "")["lines"].([]any); len(lines) != 1 || lines[0] != "weights=1000" {
		t.Fatalf("log lines holding a text %v", lines)
	}
	// One instance answers what a reader acts on, with the end of its log when asked, and its own server answers past the gateway
	got := call(t, cs, "get_instance", map[string]any{"id": "first", "logs": 5}, "")
	rec := got["instance"].(map[string]any)
	if rec["state"] != "INSTANCE_STATE_READY" || rec["command"] == nil || rec["conditions"] != nil || rec["request"] != nil || len(got["logs"].([]any)) == 0 {
		t.Fatalf("instance %v", got)
	}
	if whole := call(t, cs, "get_instance", map[string]any{"id": "first", "full": true}, "")["instance"].(map[string]any); whole["conditions"] == nil || whole["request"] == nil {
		t.Fatalf("an instance asked for whole %v", whole)
	}
	if listed := call(t, cs, "list_instances", map[string]any{}, "")["instances"].([]any); len(listed) != 1 || listed[0].(map[string]any)["conditions"] != nil {
		t.Fatalf("instances %v", listed)
	}
	if metrics := call(t, cs, "instance_get", map[string]any{"id": "first", "path": "metrics"}, ""); metrics["status"] != float64(200) || !strings.Contains(metrics["body"].(string), "vllm:num_requests_running") {
		t.Fatalf("the instance's own metrics %v", metrics)
	}
	if missing := call(t, cs, "instance_request", map[string]any{"id": "first", "path": "/nowhere", "body": "{}"}, ""); missing["status"] != float64(404) {
		t.Fatalf("a path the instance lacks %v", missing)
	}
	// A setting changed with relaunch starts the model again with it, the one serving replaced
	before := rec["id"]
	again := call(t, cs, "update_slot", map[string]any{"id": "first", "params": map[string]string{fake.ParamTransport: "tcp"}, "relaunch": true}, "")
	if again["task"].(map[string]any)["id"] == "" {
		t.Fatalf("a relaunch answers its task %v", again)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		now := call(t, cs, "get_slot", map[string]any{"id": "first"}, "")
		if in, _ := now["instance"].(map[string]any); now["slot"].(map[string]any)["state"] == "SLOT_STATE_READY" && in["id"] != before && in["params"].(map[string]any)[fake.ParamTransport] == "tcp" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the slot never served the relaunch: %v", now)
		}
	}
	call(t, cs, "delete_slot", map[string]any{"id": "first"}, "evict it")
	if gone := call(t, cs, "delete_slot", map[string]any{"id": "first", "force": true}, "")["slot"].(map[string]any); gone["name"] != "first" {
		t.Fatalf("deleted %v", gone)
	}
	if left := call(t, cs, "list_slots", map[string]any{}, ""); len(left) != 0 {
		t.Fatalf("slots left %v", left)
	}
}

// A build is previewed with the steps its recipe would run, made from steps, an environment and a binary of the
// caller's own, read back with the request it was made from, and removed with the install it made
func TestBuildTools(t *testing.T) {
	cs := poolSession(t, node(t))
	recipe := call(t, cs, "list_recipes", map[string]any{"runtime_id": fake.RuntimeID}, "")["recipes"].([]any)[0].(map[string]any)["recipe"].(map[string]any)
	if recipe["id"] != "script" || recipe["vars"].([]any)[0].(map[string]any)["name"] != "greeting" {
		t.Fatalf("recipe %v", recipe)
	}
	preview := call(t, cs, "preview_build", map[string]any{"runtime_id": fake.RuntimeID, "vars": map[string]string{"greeting": "moon"}}, "")
	steps := preview["steps"].([]any)
	if command := steps[0].(map[string]any)["command"].([]any); len(steps) != 1 || preview["binary"] != "served" || command[2] != "echo 'echo moon' > {{.Out}}/served" {
		t.Fatalf("preview %v", preview)
	}
	if listed := call(t, cs, "list_builds", map[string]any{}, ""); len(listed) != 0 {
		t.Fatalf("a preview builds nothing: %v", listed)
	}
	call(t, cs, "preview_build", map[string]any{"runtime_id": fake.RuntimeID, "sandbox": "cloud"}, "host or oci")
	call(t, cs, "build_runtime", map[string]any{"runtime_id": fake.RuntimeID, "steps": []map[string]any{{"name": "both", "command": []string{"true"}, "script": "true"}}}, "a command or a script")
	call(t, cs, "build_runtime", map[string]any{"runtime_id": "nope"}, "unknown runtime")
	// The steps the preview answered go back with one changed, beside an environment and a binary of the caller's own
	steps[0] = map[string]any{"name": "write", "script": `echo "echo $WORD {{.Vars.greeting}} {{.Jobs}}" > {{.Out}}/tool`}
	started := call(t, cs, "build_runtime", map[string]any{"runtime_id": fake.RuntimeID, "steps": steps, "env": map[string]string{"WORD": "built"}, "binary": "tool", "timeout_minutes": 2}, "")
	id := started["build"].(map[string]any)["id"].(string)
	task := started["task"].(map[string]any)["id"].(string)
	if id == "" || id == preview["build"].(map[string]any)["id"] || task == "" {
		t.Fatalf("started %v", started)
	}
	var made map[string]any
	deadline := time.Now().Add(30 * time.Second)
	for {
		made = call(t, cs, "get_build", map[string]any{"id": id}, "")["build"].(map[string]any)
		if made["state"] != "BUILD_STATE_RUNNING" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the build never ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	request := made["request"].(map[string]any)
	if made["state"] != "BUILD_STATE_SUCCEEDED" || !strings.HasSuffix(made["binary"].(string), filepath.Join("out", "tool")) || request["binary"] != "tool" || request["env"].(map[string]any)["WORD"] != "built" || request["timeout_ms"] != float64(120_000) {
		t.Fatalf("made %v", made)
	}
	if script, err := os.ReadFile(made["binary"].(string)); err != nil || !strings.HasPrefix(string(script), "echo built hello ") {
		t.Fatalf("the caller's step wrote %q %v", script, err)
	}
	log := call(t, cs, "get_task", map[string]any{"id": task}, "")["logs"].([]any)
	if tail := call(t, cs, "get_task", map[string]any{"id": task, "tail": 1}, "")["logs"].([]any); len(log) < 2 || len(tail) != 1 || tail[0] != log[len(log)-1] {
		t.Fatalf("log %v", log)
	}
	if listed := call(t, cs, "list_builds", map[string]any{"runtime_id": fake.RuntimeID}, "")["builds"].([]any); len(listed) != 1 {
		t.Fatalf("builds %v", listed)
	}
	// The install the build made launches like any other, with words and environment variables of the caller's own
	install := made["install_id"].(string)
	run := call(t, cs, "preview_run", map[string]any{"source_id": "src", "repo": "org/stored", "group": "q4", "install_id": install, "args": "--extra {{.Name}}", "params": map[string]string{"env.WORD": "served"}}, "")
	command := run["command"].([]any)
	if n := len(command); n < 2 || command[n-2] != "--extra" || command[n-1] != "stored:q4" || run["params"].(map[string]any)["env.WORD"] != "served" {
		t.Fatalf("previewed run %v", run)
	}
	call(t, cs, "preview_run", map[string]any{"source_id": "src", "repo": "org/stored", "group": "q4", "params": map[string]string{"env.NOT A NAME": "x"}}, "no name for an environment variable")
	if gone := call(t, cs, "remove_install", map[string]any{"id": install}, "")["install"].(map[string]any); gone["id"] != install {
		t.Fatalf("removed %v", gone)
	}
	call(t, cs, "get_build", map[string]any{"id": id}, "unknown build")
	call(t, cs, "remove_build", map[string]any{"id": id}, "unknown build")
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
	for _, name := range []string{"get_model_card", "resolve_model", "inspect_model", "get_runtime", "instance_logs", "list_recipes", "preview_build", "list_builds", "get_build", "preview_run", "get_instance", "get_slot", "get_model", "list_params", "instance_get", "chat"} {
		if a, ok := hints[name]; !ok || a == nil || !a.ReadOnlyHint {
			t.Fatalf("%s is not listed as read only: %v", name, a)
		}
	}
	for _, name := range []string{"create_slot", "update_slot", "install_runtime", "build_runtime", "relaunch_slot", "instance_request"} {
		if a, ok := hints[name]; !ok || a != nil {
			t.Fatalf("%s is not listed as a plain change: %v", name, a)
		}
	}
	for _, name := range []string{"delete_slot", "remove_build", "remove_install"} {
		if a := hints[name]; a == nil || a.DestructiveHint == nil || !*a.DestructiveHint || a.ReadOnlyHint {
			t.Fatalf("%s is not listed as destructive: %v", name, a)
		}
	}
}
