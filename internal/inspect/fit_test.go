package inspect

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/formats/all"
	"github.com/nickheyer/nebu/pkg/host"
	"github.com/nickheyer/nebu/pkg/precision"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/sources"
)

// GGUF value types as the format numbers them
const (
	ggufUint32 uint32 = 4
	ggufString uint32 = 8
)

// Llama GGUF fixture: two layers, width 64, context length 4096, with tensor data.
func ggufFixture() []byte {
	var b bytes.Buffer
	u32 := func(v uint32) { binary.Write(&b, binary.LittleEndian, v) }
	u64 := func(v uint64) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) {
		u64(uint64(len(s)))
		b.WriteString(s)
	}
	type tensor struct {
		name string
		dims []uint64
	}
	tensors := []tensor{
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
			u32(ggufString)
			str(v)
		case uint32:
			u32(ggufUint32)
			u32(v)
		}
	}
	var offset, total uint64
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
		total += n * 4
	}
	for b.Len()%32 != 0 {
		b.WriteByte(0)
	}
	b.Write(make([]byte, total))
	return b.Bytes()
}

// Test probe with ample device memory.
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

func inspector(t *testing.T) *Inspector {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "org", "model"), 0o755)
	if err := os.WriteFile(filepath.Join(root, "org", "model", "tiny-Q4_K_M.gguf"), ggufFixture(), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := sources.Build([]*v1.Source{{Id: "disk", Kind: v1.SourceKind_SOURCE_KIND_LOCAL, Config: map[string]string{"path": root}}})
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
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Inspector{
		Sources:  reg,
		Formats:  fmts,
		Builder:  &descriptor.Builder{Formats: fmts, Archs: families, Scale: precision.Bits{}},
		Runtimes: rts,
		Host:     host.New([]host.Probe{cardProbe{}}, nil, time.Minute),
		Cache:    store,
		Contexts: []uint32{8192, 32768, 131072},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// The fit table starts with the largest fitting context, then the capped context grid.
func TestInspectLeadsWithTheSolvedContext(t *testing.T) {
	i := inspector(t)
	resp, err := i.Inspect(context.Background(), &v1.InspectRequest{SourceId: "disk", Repo: "org/model"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetWarnings()) != 0 || len(resp.GetDescriptors()) != 1 {
		t.Fatalf("descriptors %v warnings %v", resp.GetDescriptors(), resp.GetWarnings())
	}
	d := resp.GetDescriptors()[0]
	if d.GetGroup() != "Q4_K_M" || d.GetArchitecture() != "llama" || d.GetParams()["n_ctx_train"] != 4096 || d.GetParams()["n_layer"] != 2 {
		t.Fatalf("descriptor %+v", d)
	}
	var contexts []uint32
	for _, r := range resp.GetRows() {
		if r.GetRuntimeId() == "llamacpp" {
			contexts = append(contexts, r.GetContext())
		}
	}
	if len(contexts) != 2 || contexts[0] != 0 || contexts[1] != 4096 {
		t.Fatalf("contexts %v", contexts)
	}
	solved := resp.GetRows()[0]
	if solved.GetPlan().GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || solved.GetPlan().GetParams()["n_ctx"] != "4096" || solved.GetFree().GetParams()["n_ctx"] != "4096" {
		t.Fatalf("solved row %+v", solved)
	}
	if p := solved.GetPlan().GetPools()[0]; p.GetTotalBytes() != 24<<30 || p.GetFreeBytes() != 20<<30 || solved.GetPlan().GetPlannedAt() == nil {
		t.Fatalf("pool totals travel with the plan: %+v", p)
	}
	if free := solved.GetFree().GetPools()[0]; free.GetCapacityBytes() != 20<<30 || !solved.GetFree().GetAgainstFree() {
		t.Fatalf("the free plan is capped at what is free: %+v", free)
	}
	// A context the params set is the solved row's context, never replaced by a solved one
	resp, err = i.Inspect(context.Background(), &v1.InspectRequest{SourceId: "disk", Repo: "org/model", RuntimeIds: []string{"llamacpp"}, Params: map[string]string{"n_ctx": "1024"}})
	if err != nil {
		t.Fatal(err)
	}
	if set := resp.GetRows()[0]; set.GetContext() != 0 || set.GetPlan().GetParams()["n_ctx"] != "1024" || set.GetFree().GetParams()["n_ctx"] != "1024" {
		t.Fatalf("the set context is planned %+v", set)
	}
	// Explicit contexts omit the automatically solved row.
	resp, err = i.Inspect(context.Background(), &v1.InspectRequest{SourceId: "disk", Repo: "org/model", Contexts: []uint32{1024}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resp.GetRows() {
		if r.GetContext() != 1024 {
			t.Fatalf("named context %v", r)
		}
	}
	est, err := i.Estimate(context.Background(), &v1.EstimateRequest{SourceId: "disk", Repo: "org/model", RuntimeId: "llamacpp", Params: map[string]string{"n_ctx": "2048"}, Free: true})
	if err != nil {
		t.Fatal(err)
	}
	if est.GetPlan().GetParams()["n_ctx"] != "2048" || len(est.GetParams()) == 0 || est.GetWarning() != "" {
		t.Fatalf("estimate %+v", est)
	}
	states := map[string]*v1.ParamState{}
	for _, s := range est.GetParams() {
		states[s.GetName()] = s
	}
	if states["n_ctx"].GetMax() != 4096 || states["n_gpu_layers"].GetMax() != 2 {
		t.Fatalf("states %v", est.GetParams())
	}
}

// The solved row keeps a set context and solves only an unset one, the grid rows set their own
func TestWithContext(t *testing.T) {
	base := map[string]string{"a": "1", "n_ctx": "512"}
	if got := withContext(base, "n_ctx", 0); got["n_ctx"] != "512" || got["a"] != "1" {
		t.Fatalf("a set context is kept %v", got)
	}
	for _, unset := range []map[string]string{{"a": "1"}, {"a": "1", "n_ctx": ""}, {"a": "1", "n_ctx": estimate.Auto}} {
		if got := withContext(unset, "n_ctx", 0); got["n_ctx"] != estimate.Auto || got["a"] != "1" {
			t.Fatalf("an unset context is solved %v", got)
		}
	}
	if got := withContext(base, "n_ctx", 2048); got["n_ctx"] != strconv.Itoa(2048) {
		t.Fatalf("named %v", got)
	}
	if got := withContext(base, "", 2048); got["n_ctx"] != "512" || len(got) != 2 {
		t.Fatalf("no context param leaves the params alone %v", got)
	}
}

// A draft path is read from its header, a stored draft from its descriptor, and the plan carries both
func TestDraftSizedFromItsFile(t *testing.T) {
	i := inspector(t)
	rt, err := i.Runtimes.Get("llamacpp")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "draft-tiny-Q4_K_M.gguf")
	if err := os.WriteFile(path, ggufFixture(), 0o644); err != nil {
		t.Fatal(err)
	}
	target := &v1.Descriptor{Architecture: "llama", Family: "default", Kind: v1.ModelKind_MODEL_KIND_LANGUAGE, Params: map[string]float64{"n_layer": 2, "n_head_kv": 8, "head_dim": 128, "head_dim_v": 128, "n_embd": 4096, "n_ctx_train": 8192}}
	for l := int32(0); l < 2; l++ {
		target.Groups = append(target.Groups, &v1.TensorGroup{Id: "layer." + strconv.Itoa(int(l)), Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Layer: l, Bytes: 1 << 20})
	}
	profile, err := i.Host.Profile(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	overrides := map[string]string{"n_ctx": "4096", "spec_type": "draft-simple", "draft_model": path}
	in, warning, err := i.input(context.Background(), rt, target, profile, overrides, false, v1.Placement_PLACEMENT_UNSPECIFIED, "org/model", nil, nil)
	if err != nil || warning != "" {
		t.Fatal(err, warning)
	}
	if in.Draft == nil || in.Draft.GetArchitecture() != "llama" || in.DraftFamily == nil || in.Draft.GetParams()["n_layer"] != 2 {
		t.Fatalf("draft read from its header: %v %v", in.Draft, in.DraftFamily)
	}
	plan, err := rt.Policy().Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's two layers of 2 heads at width 16 keep 4096 f16 tokens each, beside the target's own cache
	draftCache := uint64(4096 * 2 * 2 * 32 * 2)
	target4096 := uint64(4096 * 2 * 8 * 256 * 2)
	if plan.GetCacheBytes() != target4096+draftCache || plan.GetWeightsBytes() <= 2<<20 {
		t.Fatalf("the plan carries the draft: cache %d weights %d", plan.GetCacheBytes(), plan.GetWeightsBytes())
	}
	// Left unset, the method of a plain draft model is llama.cpp's own pick
	unset := map[string]string{"n_ctx": "4096", "draft_model": path}
	if plan, _, err = i.Plan(context.Background(), rt, target, profile, unset, false, v1.Placement_PLACEMENT_UNSPECIFIED, "org/model", nil, nil); err != nil || plan.GetParams()["spec_type"] != estimate.Auto {
		t.Fatalf("a plain draft model leaves the method to llama.cpp: %v %v", plan.GetParams()["spec_type"], err)
	}
	// A stored draft uses its descriptor, the model itself included
	stored := &v1.StoredModel{SourceId: "disk", Repo: "org/small", Group: "Q4_K_M", FormatId: "gguf", Descriptor_: target, Artifacts: []*v1.StoredArtifact{{Path: "/store/small.gguf", Artifact: &v1.Artifact{Path: "small.gguf", Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS}}}}
	overrides["draft_model"] = "/store/small.gguf"
	if in, _, err = i.input(context.Background(), rt, target, profile, overrides, false, v1.Placement_PLACEMENT_UNSPECIFIED, "org/model", nil, []*v1.StoredModel{stored}); err != nil || in.Draft != target {
		t.Fatalf("stored draft: %v %v", in.Draft, err)
	}
	if in, _, err = i.input(context.Background(), rt, target, profile, overrides, false, v1.Placement_PLACEMENT_UNSPECIFIED, "org/model", stored, nil); err != nil || in.Draft != target {
		t.Fatalf("the model as its own draft: %v %v", in.Draft, err)
	}
	// A draft file no header reads plans uncounted, and the plan says so
	overrides["draft_model"] = filepath.Join(dir, "gone.gguf")
	if in, warning, err = i.input(context.Background(), rt, target, profile, overrides, false, v1.Placement_PLACEMENT_UNSPECIFIED, "org/model", nil, nil); err != nil || in.Draft != nil || !strings.Contains(warning, "gone.gguf") || !strings.Contains(warning, "not counted") {
		t.Fatalf("a missing draft: %v %q %v", in.Draft, warning, err)
	}
}
