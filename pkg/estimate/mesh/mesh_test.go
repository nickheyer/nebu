package mesh_test

import (
	"context"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/estimate/mesh"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	"github.com/nickheyer/nebu/pkg/perf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

const (
	gib  = 1 << 30
	mib  = 1 << 20
	gbps = 1e9
	// Parameters per byte at four bit quantization with its scales, and at sixteen bits
	q4  = 1.75
	f16 = 0.5
)

// A model of dense layers with an embedding and an output of 256 MiB each
type shape struct {
	layers     int
	layerBytes uint64
	embedding  float64
	kvHeads    float64
	// Parameters per byte of the quantization
	density float64
	// Expert bytes per layer and the expert counts, zero for a dense model
	expertBytes   uint64
	experts, used float64
	contextTrain  float64
}

func model(s shape) *v1.Descriptor {
	ctx := s.contextTrain
	if ctx == 0 {
		ctx = 8192
	}
	kv := s.kvHeads
	if kv == 0 {
		kv = 8
	}
	d := &v1.Descriptor{Kind: v1.ModelKind_MODEL_KIND_LANGUAGE, Params: map[string]float64{"n_layer": float64(s.layers), "n_head": 64, "n_head_kv": kv, "head_dim": 128, "head_dim_v": 128, "n_embd": s.embedding, "n_ctx_train": ctx}}
	if s.experts > 0 {
		d.Params["n_expert"], d.Params["n_expert_used"] = s.experts, s.used
	}
	elements := func(b uint64) uint64 { return uint64(float64(b) * s.density) }
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "embedding", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EMBEDDING, Layer: -1, Bytes: 256 * mib, Elements: elements(256 * mib)})
	for i := 0; i < s.layers; i++ {
		d.Groups = append(d.Groups, &v1.TensorGroup{Id: "layer." + strconv.Itoa(i), Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_LAYER, Layer: int32(i), Bytes: s.layerBytes, Elements: elements(s.layerBytes)})
		if s.expertBytes > 0 {
			d.Groups = append(d.Groups, &v1.TensorGroup{Id: "experts." + strconv.Itoa(i), Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_EXPERTS, Layer: int32(i), Bytes: s.expertBytes, Elements: elements(s.expertBytes)})
		}
	}
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "output", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_OUTPUT, Layer: -1, Bytes: 256 * mib, Elements: elements(256 * mib)})
	for _, g := range d.Groups {
		d.TotalBytes += g.GetBytes()
		d.ParameterCount += g.GetElements()
	}
	return d
}

// A 70B model at four bits: 40 GiB of layers
func dense70B() *v1.Descriptor {
	return model(shape{layers: 80, layerBytes: 512 * mib, embedding: 8192, density: q4})
}

// One member with one accelerator, or none, and a CPU. Host memory streams at 100 GB/s and computes
// at one TFLOPS unless the box says otherwise.
type box struct {
	id, name string
	device   uint64
	host     uint64
	// Whether the device pool is unified with the host's
	unified bool
	stream  float64
	compute float64
	vendor  string
	version string
	// CPU numbers, the defaults when zero
	cpuStream, cpuCompute float64
	// Acceptance the device learned, the default when zero
	acceptance float64
	// Whether the numbers come from the profile table rather than samples
	profiled bool
}

func numbersOf(b box) func(*v1.Device) perf.Numbers {
	source := func(learned bool) string {
		if b.profiled {
			return "profile test"
		}
		if learned {
			return "learned from 4 samples"
		}
		return "shipped default"
	}
	return func(d *v1.Device) perf.Numbers {
		n := perf.Numbers{Fixed: 0.002, Acceptance: 0.7, FixedSource: "shipped default", AcceptanceSource: "shipped default"}
		switch {
		case d.GetKind() == v1.DeviceKind_DEVICE_KIND_CPU:
			n.Stream, n.Compute = 100*gbps, 1e12
			if b.cpuStream > 0 {
				n.Stream = b.cpuStream
			}
			if b.cpuCompute > 0 {
				n.Compute = b.cpuCompute
			}
		case d.GetId() == perf.DiskDevice:
			n.Stream = 1 * gbps
		default:
			n.Stream, n.Compute = b.stream, b.compute
			if b.acceptance > 0 {
				n.Acceptance, n.AcceptanceSource = b.acceptance, "learned from 3 samples"
			}
		}
		n.StreamSource, n.ComputeSource = source(true), source(true)
		n.Source = "bandwidth " + n.StreamSource + ", compute " + n.ComputeSource
		return n
	}
}

func nodeFor(b box, runtime string, shapes []v1.Shape) *mesh.Node {
	profile := &v1.HostProfile{Hostname: b.name}
	profile.Devices = append(profile.Devices, &v1.Device{Id: b.id + "-cpu", Kind: v1.DeviceKind_DEVICE_KIND_CPU, Name: "cpu"})
	if b.host > 0 {
		profile.Pools = append(profile.Pools, &v1.MemoryPool{Id: b.id + "-host", Kind: v1.PoolKind_POOL_KIND_HOST, TotalBytes: b.host, FreeBytes: b.host})
	}
	if b.device > 0 {
		kind := v1.PoolKind_POOL_KIND_DEVICE
		if b.unified {
			kind = v1.PoolKind_POOL_KIND_UNIFIED
		}
		profile.Devices = append(profile.Devices, &v1.Device{Id: b.id + "-gpu", Kind: v1.DeviceKind_DEVICE_KIND_GPU, Vendor: b.vendor, Name: "gpu", Facts: map[string]string{"index": "0"}})
		profile.Pools = append(profile.Pools, &v1.MemoryPool{Id: b.id + "-gpu", Kind: kind, DeviceId: b.id + "-gpu", TotalBytes: b.device, FreeBytes: b.device})
	}
	rec := &v1.Node{Id: b.id, Name: b.name, Profile: profile, Installs: []*v1.Install{{Id: b.id + "-" + runtime, RuntimeId: runtime, Version: b.version}}}
	return mesh.NodeOf(rec, runtime, "", map[string][]v1.Shape{b.id + "-" + runtime: shapes}, numbersOf(b))
}

func node(b box, shapes []v1.Shape) *mesh.Node { return nodeFor(b, "llamacpp", shapes) }

func link(from, to string, rttUs uint32, stream, aggregate float64, class v1.LinkClass, rdma string) *v1.Link {
	return &v1.Link{From: from, To: to, RttUs: rttUs, RttP95Us: rttUs * 2, StreamBytesPerSecond: uint64(stream), AggregateBytesPerSecond: uint64(aggregate), Class: class, RdmaDevice: rdma}
}

func both(a, b string, rttUs uint32, stream, aggregate float64, class v1.LinkClass, rdma string) []*v1.Link {
	return []*v1.Link{link(a, b, rttUs, stream, aggregate, class, rdma), link(b, a, rttUs, stream, aggregate, class, rdma)}
}

func lan(a, b string, rttUs uint32, gbit float64) []*v1.Link {
	return both(a, b, rttUs, gbit*gbps/8, gbit*gbps/8, v1.LinkClass_LINK_CLASS_LAN, "")
}

// The shapes a llama.cpp install supports, and the ones a rank engine's install supports
var (
	allShapes  = []v1.Shape{v1.Shape_SHAPE_SOLO, v1.Shape_SHAPE_CHAIN, v1.Shape_SHAPE_DRAFT, v1.Shape_SHAPE_RELAY, v1.Shape_SHAPE_REPLICAS}
	rankShapes = []v1.Shape{v1.Shape_SHAPE_SOLO, v1.Shape_SHAPE_CHAIN, v1.Shape_SHAPE_RELAY, v1.Shape_SHAPE_REPLICAS, v1.Shape_SHAPE_LOCKSTEP}
)

func request(d *v1.Descriptor) mesh.Request {
	return mesh.Request{Descriptor: d, Family: archs.Default{}, Runtime: runtimes.LlamaCpp{}, Params: map[string]string{"n_ctx": "8192"}, Profile: v1.PlanProfile_PLAN_PROFILE_CHAT}
}

func vllmRequest(d *v1.Descriptor) mesh.Request {
	req := request(d)
	req.Runtime = runtimes.VLLM{}
	return req
}

func find(plan *v1.FormationPlan, shape v1.Shape) *v1.Candidate {
	for _, c := range plan.GetCandidates() {
		if c.GetShape() == shape {
			return c
		}
	}
	return nil
}

// Every candidate of a shape, in table order
func all(plan *v1.FormationPlan, shape v1.Shape) []*v1.Candidate {
	var out []*v1.Candidate
	for _, c := range plan.GetCandidates() {
		if c.GetShape() == shape {
			out = append(out, c)
		}
	}
	return out
}

func within(t *testing.T, what string, got, want float64) {
	t.Helper()
	if got < want*0.75 || got > want*1.25 {
		t.Fatalf("%s: %.3g, the walkthrough says %.3g", what, got, want)
	}
}

func mustPlan(t *testing.T, req mesh.Request, m *mesh.Mesh) *v1.FormationPlan {
	t.Helper()
	plan, err := mesh.Plan(req, m)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// Walkthrough one: two identical unified memory boxes on a direct fabric link. A 235B mixture of
// experts chains on llama.cpp at 19 tokens per second and locksteps on vLLM at 28, the chat profile
// picking lockstep; a 405B dense model runs lockstep at under three tokens per second
func TestWalkthroughFabricPair(t *testing.T) {
	moe := model(shape{layers: 94, layerBytes: 38 * mib, expertBytes: 1412 * mib, experts: 128, used: 8, embedding: 4096, kvHeads: 4, density: q4, contextTrain: 40960})
	if read := float64(moe.GetTotalBytes()) / gib; read < 130 || read > 138 {
		t.Fatalf("the model is %.0f GiB, the walkthrough's is 134", read)
	}
	pair := func(runtime string) *mesh.Mesh {
		shapes := allShapes
		if runtime == "vllm" {
			shapes = rankShapes
		}
		a := nodeFor(box{id: "a", name: "left", device: 128 * gib, unified: true, stream: 273 * gbps, compute: 100e12, vendor: "nvidia", version: "b1"}, runtime, shapes)
		b := nodeFor(box{id: "b", name: "right", device: 128 * gib, unified: true, stream: 273 * gbps, compute: 100e12, vendor: "nvidia", version: "b1"}, runtime, shapes)
		return mesh.New([]*mesh.Node{a, b}, both("a", "b", 30, 12*gbps/8, 190*gbps/8, v1.LinkClass_LINK_CLASS_FABRIC, "mlx5_0"), "a")
	}
	// llama.cpp: a star chain, one round trip per token, the token reading 12.5 GiB of experts.
	plan := mustPlan(t, request(moe), pair("llamacpp"))
	chain := find(plan, v1.Shape_SHAPE_CHAIN)
	if chain == nil || chain.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		t.Fatalf("chain fits across the pair: %v, detail %s", chain, plan.GetDetail())
	}
	within(t, "chain tokens per second", chain.GetTokensPerSecond(), 19)
	if !strings.Contains(chain.GetReason(), "star chain") {
		t.Fatalf("llama.cpp chains as a star: %s", chain.GetReason())
	}
	if plan.GetShape() != v1.Shape_SHAPE_CHAIN || len(plan.GetSeats()) != 2 {
		t.Fatalf("shape %v seats %d: %s", plan.GetShape(), len(plan.GetSeats()), plan.GetDetail())
	}
	var read uint64
	for _, s := range plan.GetSeats() {
		read += s.GetReadBytes()
		if s.GetLayerTo() <= s.GetLayerFrom() {
			t.Fatalf("every seat holds layers: %v", s)
		}
	}
	within(t, "bytes a token reads", float64(read)/gib, 12.5)
	// vLLM: lockstep at 28 tokens per second beats the ring chain for a chat profile.
	plan = mustPlan(t, vllmRequest(moe), pair("vllm"))
	lock := find(plan, v1.Shape_SHAPE_LOCKSTEP)
	if lock == nil || lock.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		t.Fatalf("lockstep fits with matching installs: %v, detail %s", lock, plan.GetDetail())
	}
	within(t, "lockstep tokens per second", lock.GetTokensPerSecond(), 28)
	if plan.GetShape() != v1.Shape_SHAPE_LOCKSTEP {
		t.Fatalf("the chat profile picks lockstep, got %v: %s", plan.GetShape(), plan.GetDetail())
	}
	ring := find(plan, v1.Shape_SHAPE_CHAIN)
	if ring == nil || ring.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || !strings.Contains(ring.GetReason(), "ring chain, 1 boundaries") || ring.GetScore() <= lock.GetScore() {
		t.Fatalf("the ring chain sits beneath lockstep in the table: %v", ring)
	}
	// The 405B dense model at four bits fits lockstep at under three tokens per second: 205 GiB
	// streamed at 546 GB/s combined is the floor.
	big := model(shape{layers: 126, layerBytes: 1656 * mib, embedding: 16384, density: q4, contextTrain: 8192})
	plan = mustPlan(t, vllmRequest(big), pair("vllm"))
	if plan.GetShape() != v1.Shape_SHAPE_LOCKSTEP || plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		t.Fatalf("405B runs lockstep: %v %s", plan.GetShape(), plan.GetDetail())
	}
	if tps := plan.GetTokensPerSecond(); tps >= 3 || tps < 2 {
		t.Fatalf("405B lockstep at %.2f tokens per second, the walkthrough says under 3", tps)
	}
	for _, c := range all(plan, v1.Shape_SHAPE_SOLO) {
		if c.GetVerdict() == v1.FitVerdict_FIT_VERDICT_FITS {
			t.Fatalf("205 GiB fits no single box: %v", c)
		}
	}
}

// Walkthrough two: a desktop with a 24 GiB device, a 128 GiB unified memory box, and a laptop with
// host memory alone on a 2.5 Gb/s switch. The 70B model chains over the two devices at nine tokens
// per second with a prompt near nine seconds, the chains through the laptop's host memory priced
// beneath it, lockstep priced on the lan link, relay rejected with its reason
func TestWalkthroughLanChain(t *testing.T) {
	desktop := node(box{id: "desk", name: "desktop", device: 24 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1"}, allShapes)
	unified := node(box{id: "box", name: "box", device: 128 * gib, unified: true, stream: 256 * gbps, compute: 20e12, vendor: "apple", version: "b1"}, allShapes)
	laptop := node(box{id: "lap", name: "laptop", host: 32 * gib, version: "b1"}, allShapes)
	links := append(append(lan("desk", "box", 180, 2.3), lan("desk", "lap", 180, 2.3)...), lan("box", "lap", 180, 2.3)...)
	m := mesh.New([]*mesh.Node{desktop, unified, laptop}, links, "desk")
	plan := mustPlan(t, request(dense70B()), m)
	if plan.GetShape() != v1.Shape_SHAPE_CHAIN || plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		t.Fatalf("shape %v verdict %v detail %s", plan.GetShape(), plan.GetVerdict(), plan.GetDetail())
	}
	if len(plan.GetSeats()) != 2 {
		t.Fatalf("seats %v", plan.GetSeats())
	}
	layers := 0
	for _, s := range plan.GetSeats() {
		if s.GetNodeId() == "lap" {
			t.Fatal("the laptop holds nothing and must be dropped")
		}
		if s.GetLayerTo() <= s.GetLayerFrom() {
			t.Fatalf("empty range %v", s)
		}
		layers += int(s.GetLayerTo() - s.GetLayerFrom())
	}
	if layers != 80 {
		t.Fatalf("the seats hold %d of 80 layers", layers)
	}
	within(t, "tokens per second", plan.GetTokensPerSecond(), 9)
	within(t, "prefill at 2048 tokens", plan.GetPrefillSeconds(), 9)
	// The best chain by score heads the plan, the head holding the last layers.
	best := plan.GetCandidates()[0]
	if best.GetShape() != v1.Shape_SHAPE_CHAIN || best.GetHead() != plan.GetHead() {
		t.Fatalf("the plan is the best candidate: %v", best)
	}
	for _, s := range plan.GetSeats() {
		if s.GetNodeId() == plan.GetHead() && s.GetLayerTo() != 80 {
			t.Fatalf("the head holds the last layers: %v", s)
		}
	}
	// The laptop's host memory seats layers too: every chain through it is priced and scores below.
	listed := false
	for _, c := range all(plan, v1.Shape_SHAPE_CHAIN) {
		for _, id := range c.GetNodeIds() {
			if id != "lap" {
				continue
			}
			listed = true
			if c.GetVerdict() == v1.FitVerdict_FIT_VERDICT_FITS && c.GetScore() <= best.GetScore() {
				t.Fatalf("a chain through the laptop scores below the plan: %v", c)
			}
		}
	}
	if !listed {
		t.Fatal("the chains through the laptop are in the table")
	}
	// Lockstep is a rank engine's shape: on the same switch vLLM prices it and names the link. Each
	// rank holds half the model with half the cache, 23.5 GiB, so the desktop here has 32 GiB.
	vdesk := nodeFor(box{id: "desk", name: "desktop", device: 32 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "0.11"}, "vllm", rankShapes)
	vbox := nodeFor(box{id: "box", name: "box", device: 128 * gib, unified: true, stream: 256 * gbps, compute: 20e12, vendor: "nvidia", version: "0.11"}, "vllm", rankShapes)
	vplan := mustPlan(t, vllmRequest(dense70B()), mesh.New([]*mesh.Node{vdesk, vbox}, lan("desk", "box", 180, 2.3), "desk"))
	lock := find(vplan, v1.Shape_SHAPE_LOCKSTEP)
	if lock == nil || lock.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || !strings.Contains(lock.GetReason(), "link class lan, 180 µs") || !strings.Contains(lock.GetReason(), "every reduction pays it") {
		t.Fatalf("lockstep is priced on the lan link and says so: %v", lock)
	}
	relay := find(plan, v1.Shape_SHAPE_RELAY)
	if relay == nil || relay.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO || !strings.Contains(relay.GetReason(), "does not fit the model alone") {
		t.Fatalf("relay is rejected when no node fits alone: %v", relay)
	}
	if c := find(plan, v1.Shape_SHAPE_SOLO); c == nil || c.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || c.GetScore() <= plan.GetCandidates()[0].GetScore() {
		t.Fatalf("solo on the box fits but scores below the chain: %v", c)
	}
	if len(plan.GetSources()) == 0 {
		t.Fatal("the plan names the source of its numbers")
	}
}

// Walkthrough three: a workstation that fits the 70B model with a laptop beside it on a 1 Gb/s
// link. The draft on the laptop's device beats solo by about 1.6 times, replicas and relay are
// rejected with reasons, and the table shows solo beneath the draft
func TestWalkthroughDraft(t *testing.T) {
	draft := model(shape{layers: 28, layerBytes: 48 * mib, embedding: 1536, kvHeads: 2, density: 1})
	work := node(box{id: "work", name: "workstation", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1"}, allShapes)
	lap := node(box{id: "lap", name: "laptop", device: 16 * gib, host: 32 * gib, stream: 250 * gbps, compute: 20e12, vendor: "nvidia", version: "b1"}, allShapes)
	m := mesh.New([]*mesh.Node{work, lap}, lan("work", "lap", 400, 0.94), "work")
	req := request(dense70B())
	req.Draft, req.DraftFamily = draft, archs.Default{}
	plan := mustPlan(t, req, m)
	if plan.GetShape() != v1.Shape_SHAPE_DRAFT {
		t.Fatalf("shape %v detail %s", plan.GetShape(), plan.GetDetail())
	}
	solo := find(plan, v1.Shape_SHAPE_SOLO)
	if solo == nil || solo.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		t.Fatalf("solo fits on the workstation: %v", solo)
	}
	within(t, "solo tokens per second", solo.GetTokensPerSecond(), 19)
	within(t, "draft tokens per second", plan.GetTokensPerSecond(), 30)
	within(t, "speedup over solo", plan.GetSpeedup(), 1.6)
	if c := find(plan, v1.Shape_SHAPE_REPLICAS); c == nil || c.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO || !strings.Contains(c.GetReason(), "laptop does not fit the model") {
		t.Fatalf("replicas must be rejected, the laptop does not fit the model: %v", c)
	}
	if c := find(plan, v1.Shape_SHAPE_RELAY); c == nil || c.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO || c.GetReason() == "" {
		t.Fatalf("relay must be rejected with a reason: %v", c)
	}
	if plan.GetDraftTokens() != 5 || math.Abs(plan.GetAcceptance()-0.7) > 1e-9 {
		t.Fatalf("draft tokens %d acceptance %.2f", plan.GetDraftTokens(), plan.GetAcceptance())
	}
	if plan.GetCandidates()[0].GetShape() != v1.Shape_SHAPE_DRAFT || solo.GetScore() <= plan.GetCandidates()[0].GetScore() {
		t.Fatalf("the table shows solo beneath the draft: %v", plan.GetCandidates())
	}
	// Every draft token accepted yields gamma plus one tokens per round, a finite number.
	sure := node(box{id: "work", name: "workstation", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1", acceptance: 1}, allShapes)
	plan = mustPlan(t, req, mesh.New([]*mesh.Node{sure, lap}, lan("work", "lap", 400, 0.94), "work"))
	if plan.GetShape() != v1.Shape_SHAPE_DRAFT || math.IsNaN(plan.GetDecodeSecondsPerToken()) || plan.GetDecodeSecondsPerToken() <= 0 {
		t.Fatalf("acceptance one prices the limit: %v %s", plan.GetShape(), plan.GetDetail())
	}
	if !strings.Contains(plan.GetDetail(), "6.0 tokens per round") {
		t.Fatalf("every token accepted yields six per round: %s", plan.GetDetail())
	}
}

// Walkthrough four: a compute rich box and a bandwidth rich box on a fast link with an 8B model at
// sixteen bits. For a route with 8k prompts the relay wins: the prompt on the compute box in 1.5 s,
// the cache moved under the prefill, the answer on the bandwidth box at 38 tokens per second, and
// the plan names the prompt length above which the relay pays. Twins gain nothing from a relay
func TestWalkthroughRelay(t *testing.T) {
	eightB := model(shape{layers: 32, layerBytes: 480 * mib, embedding: 4096, density: f16})
	compute := nodeFor(box{id: "c", name: "compute", device: 64 * gib, host: 64 * gib, stream: 273 * gbps, compute: 100e12, vendor: "nvidia", version: "0.11"}, "vllm", rankShapes)
	bandwidth := nodeFor(box{id: "b", name: "bandwidth", device: 64 * gib, host: 64 * gib, stream: 819 * gbps, compute: 26e12, vendor: "nvidia", version: "0.11"}, "vllm", rankShapes)
	fast := both("c", "b", 210, 9.4*gbps/8, 9.4*gbps/8, v1.LinkClass_LINK_CLASS_FAST, "")
	m := mesh.New([]*mesh.Node{compute, bandwidth}, fast, "b")
	req := vllmRequest(eightB)
	req.Profile = v1.PlanProfile_PLAN_PROFILE_AGENT
	req.Stats = mesh.Stats{MedianPrompt: 8192, MedianCompletion: 256, Traces: 40}
	plan := mustPlan(t, req, m)
	if plan.GetShape() != v1.Shape_SHAPE_RELAY {
		t.Fatalf("shape %v detail %s", plan.GetShape(), plan.GetDetail())
	}
	if plan.GetReferencePrompt() != 8192 {
		t.Fatalf("the agent profile prices the route's median prompt, got %d", plan.GetReferencePrompt())
	}
	within(t, "relay time to first token at 8k", plan.GetPrefillSeconds(), 1.5)
	within(t, "decode on the bandwidth box", plan.GetTokensPerSecond(), 38)
	if be := plan.GetRelayBreakEvenPrompt(); be == 0 || be > 8192 || !strings.Contains(plan.GetDetail(), "pays above") {
		t.Fatalf("the plan states the prompt length above which the relay pays, break even %d: %s", be, plan.GetDetail())
	}
	if plan.GetHead() != "b" {
		t.Fatalf("the decode seat on the bandwidth box heads the relay, got %s", plan.GetHead())
	}
	var prefill, decode string
	for _, s := range plan.GetSeats() {
		switch s.GetRole() {
		case runtimes.RolePrefill:
			prefill = s.GetNodeId()
		case runtimes.RoleDecode:
			decode = s.GetNodeId()
		}
	}
	if prefill != "c" || decode != "b" {
		t.Fatalf("prefill %s decode %s", prefill, decode)
	}
	for _, c := range all(plan, v1.Shape_SHAPE_SOLO) {
		if c.GetScore() <= plan.GetCandidates()[0].GetScore() {
			t.Fatalf("both solos score below the relay: %v", c)
		}
	}
	// Two identical boxes gain nothing from a relay.
	twin := nodeFor(box{id: "d", name: "twin", device: 64 * gib, host: 64 * gib, stream: 273 * gbps, compute: 100e12, vendor: "nvidia", version: "0.11"}, "vllm", rankShapes)
	plan2 := mustPlan(t, req, mesh.New([]*mesh.Node{compute, twin}, both("c", "d", 210, 9.4*gbps/8, 9.4*gbps/8, v1.LinkClass_LINK_CLASS_FAST, ""), "c"))
	if plan2.GetShape() == v1.Shape_SHAPE_RELAY {
		t.Fatalf("twins must not relay: %s", plan2.GetDetail())
	}
	relay := find(plan2, v1.Shape_SHAPE_RELAY)
	if relay == nil || relay.GetScore() < find(plan2, v1.Shape_SHAPE_SOLO).GetScore() || !strings.Contains(relay.GetReason(), "alike") {
		t.Fatalf("relay on twins scores below solo with the reason: %v", relay)
	}
}

// Walkthrough five: a box with a 24 GiB device and little host memory, and a box with a 4 GiB
// device and much host memory, on a 1 Gb/s link, running a video pipeline whose denoiser is 14 GiB,
// text encoder 11 GiB, and decoder 1 GiB. Neither box runs it alone; stages puts the denoiser on
// the first box's device and the encoders and decoder in the second box's host memory, priced per
// request with the two crossings
func TestWalkthroughStages(t *testing.T) {
	d := &v1.Descriptor{FormatId: "diffusion", Group: "video", Architecture: "wan", Kind: v1.ModelKind_MODEL_KIND_DIFFUSION, Metadata: map[string]string{diffusion.KeyFamily: "wan", diffusion.KeyVAE: "true", diffusion.KeyTextEncoder: "true"}, Params: map[string]float64{"n_embd": 5120}}
	elements := func(b uint64) uint64 { return b / 2 }
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "diffusion", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_DIFFUSION, Layer: -1, Bytes: 14 * gib, Elements: elements(14 * gib)})
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "text_encoder", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_TEXT_ENCODER, Layer: -1, Bytes: 11 * gib, Elements: elements(11 * gib)})
	d.Groups = append(d.Groups, &v1.TensorGroup{Id: "vae", Kind: v1.TensorGroupKind_TENSOR_GROUP_KIND_VAE, Layer: -1, Bytes: 1 * gib, Elements: elements(1 * gib)})
	for _, g := range d.Groups {
		d.TotalBytes += g.GetBytes()
		d.ParameterCount += g.GetElements()
	}
	shapes := []v1.Shape{v1.Shape_SHAPE_SOLO, v1.Shape_SHAPE_STAGES, v1.Shape_SHAPE_REPLICAS}
	big := nodeFor(box{id: "big", name: "big device", device: 24 * gib, host: 16 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "s1"}, "sdcpp", shapes)
	wide := nodeFor(box{id: "wide", name: "wide host", device: 4 * gib, host: 64 * gib, stream: 250 * gbps, compute: 10e12, vendor: "nvidia", version: "s1"}, "sdcpp", shapes)
	m := mesh.New([]*mesh.Node{big, wide}, lan("big", "wide", 300, 1), "wide")
	req := mesh.Request{Descriptor: d, Family: archs.Default{}, Runtime: runtimes.SDCpp{}, Params: map[string]string{"steps": "50", "width": "1024", "height": "1024", "video_frames": "5"}, Profile: v1.PlanProfile_PLAN_PROFILE_CHAT}
	plan := mustPlan(t, req, m)
	if plan.GetShape() != v1.Shape_SHAPE_STAGES || plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		t.Fatalf("shape %v verdict %v detail %s", plan.GetShape(), plan.GetVerdict(), plan.GetDetail())
	}
	for _, c := range plan.GetCandidates() {
		if c.GetShape() != v1.Shape_SHAPE_STAGES && c.GetVerdict() == v1.FitVerdict_FIT_VERDICT_FITS {
			t.Fatalf("stages is the only candidate that fits: %v", c)
		}
	}
	var denoiser, head string
	for _, s := range plan.GetSeats() {
		switch s.GetRole() {
		case runtimes.RoleDenoiser:
			denoiser = s.GetNodeId()
			if len(s.GetDeviceIds()) != 1 || s.GetDeviceIds()[0] != "big-gpu" {
				t.Fatalf("the denoiser sits on the big device: %v", s)
			}
		case runtimes.RoleHead:
			head = s.GetNodeId()
		}
	}
	if denoiser != "big" || head != "wide" {
		t.Fatalf("denoiser on %s, head on %s", denoiser, head)
	}
	reason := plan.GetCandidates()[0].GetReason()
	for _, want := range []string{"encoding", "embedding across in", "denoising steps on big device", "latent back in", "decoding"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("the reason prices %q: %s", want, reason)
		}
	}
	// The request is priced by the cost lines: encoder parameters times prompt tokens over the head's
	// cores, denoiser parameters times steps times latent tokens over the device, decoder parameters
	// times the latent positions it decodes from over the head's cores, plus the two crossings.
	const cpu, gpu, link = 1e12, 80e12, 1 * gbps / 8
	latentTokens, latentPositions := 1024.0*1024/256*5, 1024.0*1024/64*5
	encode := 2 * float64(elements(11*gib)) * 2048 / cpu
	denoise := 2 * float64(elements(14*gib)) * 50 * latentTokens / gpu
	decode := 2 * float64(elements(1*gib)) * latentPositions / cpu
	embedding := 2048 * 5120 * 2 / link
	latent := latentTokens * 32 / link
	want := encode + denoise + decode + embedding + latent + 2*300e-6
	if got := plan.GetPrefillSeconds(); math.Abs(got-want) > want*1e-6 {
		t.Fatalf("request priced at %.1f s, the cost lines give %.1f s: %s", got, want, reason)
	}
	if !strings.Contains(reason, "20.0 MiB embedding across in 168 ms") {
		t.Fatalf("the embedding crosses once at 1 Gb/s: %s", reason)
	}
	if plan.GetRequestsPerSecond() <= 0 || plan.GetDecodeSecondsPerToken() != 0 {
		t.Fatalf("a pipeline is priced per request: rps %v decode %v", plan.GetRequestsPerSecond(), plan.GetDecodeSecondsPerToken())
	}
	// Solo on the big device is priced through the same pipeline, every part on one node.
	for _, solo := range all(plan, v1.Shape_SHAPE_SOLO) {
		if solo.GetVerdict() == v1.FitVerdict_FIT_VERDICT_FITS || !strings.Contains(solo.GetReason(), "does not fit") {
			t.Fatalf("neither box runs the pipeline alone: %v", solo)
		}
	}
	if len(all(plan, v1.Shape_SHAPE_SOLO)) != 2 {
		t.Fatalf("both solos are in the table: %v", all(plan, v1.Shape_SHAPE_SOLO))
	}
}

// A learned ratio multiplies the prediction of its shape, the plan saying from how many runs, and
// the plan keeps the cost model's own numbers beside the corrected ones
func TestRatioChangesPrediction(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "nebu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	table, err := perf.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	desktop := node(box{id: "desk", name: "desktop", device: 24 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1"}, allShapes)
	unified := node(box{id: "box", name: "box", device: 128 * gib, unified: true, stream: 256 * gbps, compute: 20e12, vendor: "apple", version: "b1"}, allShapes)
	m := mesh.New([]*mesh.Node{desktop, unified}, lan("desk", "box", 180, 2.3), "desk")
	req := request(dense70B())
	req.Shape = v1.Shape_SHAPE_CHAIN
	req.Table = table
	before := mustPlan(t, req, m)
	if before.GetTtftRatio() != 1 || before.GetTptRatio() != 1 || before.GetRatioSamples() != 0 || strings.Contains(before.GetDetail(), "corrected") {
		t.Fatalf("nothing learned yet: %s", before.GetDetail())
	}
	if len(before.GetPrefillCost()) == 0 || before.GetModelDecodeSecondsPerToken() != before.GetDecodeSecondsPerToken() {
		t.Fatalf("the plan carries its cost model: %v %v", before.GetPrefillCost(), before.GetModelDecodeSecondsPerToken())
	}
	if err := table.RecordRatio(context.Background(), v1.Shape_SHAPE_CHAIN, "llamacpp", v1.LinkClass_LINK_CLASS_LAN, 1, 1.5, 0.1, 0.2); err != nil {
		t.Fatal(err)
	}
	after := mustPlan(t, req, m)
	if math.Abs(after.GetDecodeSecondsPerToken()-2*before.GetDecodeSecondsPerToken()) > 1e-9 || math.Abs(after.GetPrefillSeconds()-1.5*before.GetPrefillSeconds()) > 1e-9 {
		t.Fatalf("the ratio multiplies the prediction: %v -> %v, %v -> %v", before.GetDecodeSecondsPerToken(), after.GetDecodeSecondsPerToken(), before.GetPrefillSeconds(), after.GetPrefillSeconds())
	}
	if after.GetModelDecodeSecondsPerToken() != before.GetModelDecodeSecondsPerToken() || after.GetPrefillCost()[0].GetFixedSeconds() != before.GetPrefillCost()[0].GetFixedSeconds() {
		t.Fatal("the cost model's own numbers stay before the ratio")
	}
	if after.GetRatioSamples() != 1 || !strings.Contains(after.GetDetail(), "corrected by learned ratios 1.50 and 2.00 from 1 runs") {
		t.Fatalf("the detail names the correction and its runs: %s", after.GetDetail())
	}
	if c := find(after, v1.Shape_SHAPE_CHAIN); c.GetTokensPerSecond() != after.GetTokensPerSecond() {
		t.Fatal("the candidate table carries the corrected numbers")
	}
}

// The batch profile weighs throughput, so two nodes that fit the model run replicas with the
// affinity policy; the auto profile prices the route's own prompt, completion, and concurrency
func TestBatchAndAutoProfiles(t *testing.T) {
	a := node(box{id: "a", name: "a", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1"}, allShapes)
	b := node(box{id: "b", name: "b", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1"}, allShapes)
	m := mesh.New([]*mesh.Node{a, b}, lan("a", "b", 200, 2.5), "a")
	req := request(dense70B())
	req.Profile = v1.PlanProfile_PLAN_PROFILE_BATCH
	plan := mustPlan(t, req, m)
	if plan.GetShape() != v1.Shape_SHAPE_REPLICAS {
		t.Fatalf("batch picks replicas: %v %s", plan.GetShape(), plan.GetDetail())
	}
	if len(plan.GetSeats()) != 2 || plan.GetRequestsPerSecond() <= find(plan, v1.Shape_SHAPE_SOLO).GetRequestsPerSecond()*1.9 {
		t.Fatalf("replicas add throughput: %v rps, solo %v", plan.GetRequestsPerSecond(), find(plan, v1.Shape_SHAPE_SOLO).GetRequestsPerSecond())
	}
	if af := plan.GetAffinity(); af.GetWindowSeconds() != 600 || !af.GetSystemMessage() || !af.GetFirstUserMessage() {
		t.Fatalf("a replicas plan carries the affinity policy: %v", af)
	}
	if !strings.Contains(find(plan, v1.Shape_SHAPE_REPLICAS).GetReason(), "each request as fast as its seat alone") {
		t.Fatal("replicas say per request latency does not change")
	}
	// A chat profile on the same pair keeps a single seat: latency, not throughput.
	req.Profile = v1.PlanProfile_PLAN_PROFILE_CHAT
	if p := mustPlan(t, req, m); p.GetShape() != v1.Shape_SHAPE_SOLO || p.GetAffinity() != nil {
		t.Fatalf("chat picks solo without affinity: %v", p.GetShape())
	}
	// Auto learns from the route's traces.
	req.Profile = v1.PlanProfile_PLAN_PROFILE_AUTO
	req.Stats = mesh.Stats{MedianPrompt: 3000, MedianCompletion: 100, PeakConcurrency: 6, Traces: 50}
	plan = mustPlan(t, req, m)
	if plan.GetReferencePrompt() != 3000 || plan.GetReferenceCompletion() != 100 {
		t.Fatalf("auto prices the route's median prompt and completion: %d %d", plan.GetReferencePrompt(), plan.GetReferenceCompletion())
	}
	if plan.GetShape() != v1.Shape_SHAPE_REPLICAS {
		t.Fatalf("six concurrent requests want replicas: %v %s", plan.GetShape(), plan.GetDetail())
	}
	req.Stats = mesh.Stats{MedianPrompt: 3000, MedianCompletion: 100, PeakConcurrency: 1, Traces: 50}
	if p := mustPlan(t, req, m); p.GetShape() != v1.Shape_SHAPE_SOLO {
		t.Fatalf("one request at a time wants solo: %v", p.GetShape())
	}
	// Without traces auto prices the chat sizes.
	req.Stats = mesh.Stats{}
	if p := mustPlan(t, req, m); p.GetReferencePrompt() != mesh.ChatPrompt || p.GetReferenceCompletion() != mesh.ChatCompletion {
		t.Fatalf("auto without traces prices chat sizes: %d %d", p.GetReferencePrompt(), p.GetReferenceCompletion())
	}
}

// Chain and a local draft compose on llama.cpp: the draft fits beside the head's layers and a
// round of draft tokens is verified in one pass through the chain, so the chain's link cost is
// paid once per round
func TestChainWithLocalDraft(t *testing.T) {
	draft := model(shape{layers: 28, layerBytes: 48 * mib, embedding: 1536, kvHeads: 2, density: 1})
	desktop := node(box{id: "desk", name: "desktop", device: 24 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1"}, allShapes)
	unified := node(box{id: "box", name: "box", device: 128 * gib, unified: true, stream: 256 * gbps, compute: 20e12, vendor: "apple", version: "b1"}, allShapes)
	m := mesh.New([]*mesh.Node{desktop, unified}, lan("desk", "box", 180, 2.3), "desk")
	req := request(dense70B())
	req.Shape = v1.Shape_SHAPE_CHAIN
	req.Draft, req.DraftFamily, req.DraftRepo = draft, archs.Default{}, "hf/draft"
	plan := mustPlan(t, req, m)
	var plain, drafted *v1.Candidate
	for _, c := range all(plan, v1.Shape_SHAPE_CHAIN) {
		if c.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || c.GetHead() != plan.GetHead() {
			continue
		}
		if c.GetDraftTokens() > 0 && drafted == nil {
			drafted = c
		} else if c.GetDraftTokens() == 0 && plain == nil {
			plain = c
		}
	}
	if plain == nil || drafted == nil {
		t.Fatalf("the table prices the chain with and without the draft: %v", plan.GetCandidates())
	}
	if !strings.Contains(drafted.GetReason(), "draft beside the head") || drafted.GetDraftTokens() != 5 {
		t.Fatalf("the drafted chain says so: %v", drafted)
	}
	if drafted.GetDecodeSecondsPerToken() >= plain.GetDecodeSecondsPerToken() {
		t.Fatalf("the draft divides the chain's step by the tokens per round: %v against %v", drafted.GetDecodeSecondsPerToken(), plain.GetDecodeSecondsPerToken())
	}
	if plan.GetDraftTokens() != 5 || plan.GetAcceptance() != 0.7 || plan.GetShape() != v1.Shape_SHAPE_CHAIN {
		t.Fatalf("the drafted chain is the plan: %v", plan.GetDetail())
	}
	// The head's memory carries the draft beside its layers.
	for _, s := range plan.GetSeats() {
		if s.GetNodeId() == plan.GetHead() && s.GetMemory().GetWeightsBytes() <= 20*gib {
			t.Fatalf("the head's plan holds the draft too: %v", s.GetMemory().GetWeightsBytes())
		}
	}
	// vLLM does not compose speculative settings with pipeline parallel, and its chain says so.
	va := nodeFor(box{id: "a", name: "a", device: 24 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "0.11"}, "vllm", rankShapes)
	vb := nodeFor(box{id: "b", name: "b", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "0.11"}, "vllm", rankShapes)
	vreq := vllmRequest(dense70B())
	vreq.Shape = v1.Shape_SHAPE_CHAIN
	vreq.Params["speculative_config"] = `{"method":"mtp","num_speculative_tokens":1}`
	vplan := mustPlan(t, vreq, mesh.New([]*mesh.Node{va, vb}, lan("a", "b", 200, 2.5), "a"))
	if vplan.GetShape() != v1.Shape_SHAPE_CHAIN || !strings.Contains(vplan.GetDetail(), "speculative settings are off in chain") {
		t.Fatalf("the vLLM chain says speculative settings are off: %v %s", vplan.GetShape(), vplan.GetDetail())
	}
	for _, c := range all(vplan, v1.Shape_SHAPE_CHAIN) {
		if c.GetDraftTokens() > 0 {
			t.Fatalf("vLLM prices no drafted chain: %v", c)
		}
	}
}

// Seats on different install versions still form: chain, relay, and draft candidates across
// mismatched installs are priced and the plan notes both builds, and ranks that differ in vendor
// or version form too with the difference noted
func TestMismatchedInstallRuns(t *testing.T) {
	draft := model(shape{layers: 28, layerBytes: 48 * mib, embedding: 1536, kvHeads: 2, density: 1})
	notes := func(plan *v1.FormationPlan) bool {
		for _, line := range plan.GetSources() {
			if strings.Contains(line, "b1") && strings.Contains(line, "b2") && strings.Contains(line, "different builds") {
				return true
			}
		}
		return false
	}
	// Chain: a pair neither of which fits the model alone.
	desktop := node(box{id: "desk", name: "desktop", device: 24 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1"}, allShapes)
	unified := node(box{id: "box", name: "box", device: 128 * gib, unified: true, stream: 256 * gbps, compute: 20e12, vendor: "apple", version: "b2"}, allShapes)
	pair := mesh.New([]*mesh.Node{desktop, unified}, lan("desk", "box", 180, 2.3), "desk")
	plan := mustPlan(t, request(dense70B()), pair)
	if c := find(plan, v1.Shape_SHAPE_CHAIN); c == nil || c.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS {
		t.Fatalf("chain across b1 and b2 fits: %v", c)
	}
	if !notes(plan) {
		t.Fatalf("the plan names both builds: %v", plan.GetSources())
	}
	// Relay and draft: a pair that fits the model alone.
	a := node(box{id: "a", name: "a", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1"}, allShapes)
	b := node(box{id: "b", name: "b", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b2"}, allShapes)
	m := mesh.New([]*mesh.Node{a, b}, lan("a", "b", 200, 2.5), "a")
	req := request(dense70B())
	req.Draft, req.DraftFamily = draft, archs.Default{}
	plan = mustPlan(t, req, m)
	for _, shape := range []v1.Shape{v1.Shape_SHAPE_RELAY, v1.Shape_SHAPE_DRAFT} {
		fits := false
		for _, c := range all(plan, shape) {
			if c.GetVerdict() == v1.FitVerdict_FIT_VERDICT_FITS {
				fits = true
			}
			if strings.Contains(c.GetReason(), "install version") {
				t.Fatalf("%v is not rejected for its install version: %v", shape, c)
			}
		}
		if !fits {
			t.Fatalf("%v fits across b1 and b2: %v", shape, all(plan, shape))
		}
	}
	if !notes(plan) {
		t.Fatalf("the plan names both builds: %v", plan.GetSources())
	}
	// Ranks of different vendors form, the plan noting the difference.
	va := nodeFor(box{id: "a", name: "a", device: 24 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "0.11"}, "vllm", rankShapes)
	vb := nodeFor(box{id: "b", name: "b", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "amd", version: "0.11"}, "vllm", rankShapes)
	vreq := vllmRequest(dense70B())
	vreq.Shape = v1.Shape_SHAPE_CHAIN
	vplan := mustPlan(t, vreq, mesh.New([]*mesh.Node{va, vb}, lan("a", "b", 200, 2.5), "a"))
	if c := find(vplan, v1.Shape_SHAPE_CHAIN); c == nil || c.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || strings.Contains(c.GetReason(), "ranks must match") {
		t.Fatalf("mixed vendors form ranks: %v", c)
	}
	if !strings.Contains(strings.Join(vplan.GetSources(), "\n"), "a has nvidia devices and b has amd, the ranks differ in vendor") {
		t.Fatalf("the plan notes the vendor difference: %v", vplan.GetSources())
	}
	// Ranks on different versions form too.
	vb.Vendor, vb.Install.Version = "nvidia", "0.12"
	vplan = mustPlan(t, vreq, mesh.New([]*mesh.Node{va, vb}, lan("a", "b", 200, 2.5), "a"))
	if c := find(vplan, v1.Shape_SHAPE_CHAIN); c == nil || c.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || strings.Contains(c.GetReason(), "ranks must match") {
		t.Fatalf("ranks on 0.11 and 0.12 form: %v", c)
	}
	if !strings.Contains(strings.Join(vplan.GetSources(), "\n"), "a runs vllm 0.11 and b runs 0.12, the seats run different builds") {
		t.Fatalf("the plan notes the build difference: %v", vplan.GetSources())
	}
}

// The plan lists every device, CPU, and disk it priced with and where each number came from, and
// says when numbers came from the profile table
func TestSourcesNameEveryNumber(t *testing.T) {
	desktop := node(box{id: "desk", name: "desktop", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1", profiled: true}, allShapes)
	unified := node(box{id: "box", name: "box", device: 128 * gib, unified: true, stream: 256 * gbps, compute: 20e12, vendor: "apple", version: "b1"}, allShapes)
	cpuOnly := node(box{id: "cpu", name: "cpubox", host: 96 * gib, version: "b1"}, allShapes)
	links := append(append(lan("desk", "box", 180, 2.3), lan("desk", "cpu", 180, 2.3)...), lan("box", "cpu", 180, 2.3)...)
	plan := mustPlan(t, request(dense70B()), mesh.New([]*mesh.Node{desktop, unified, cpuOnly}, links, "desk"))
	joined := strings.Join(plan.GetSources(), "\n")
	for _, want := range []string{"desktop gpu: bandwidth 900 GB/s profile test, compute 80 TFLOPS profile test", "box gpu: bandwidth 256 GB/s learned from 4 samples", "cpubox cpu: bandwidth 100 GB/s learned from 4 samples, compute 1 TFLOPS", "fixed cost 2.0 ms shipped default", "acceptance 0.70 shipped default", "desktop disk: bandwidth 1 GB/s", "box disk: bandwidth 1 GB/s"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("sources name %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(plan.GetDetail(), "numbers from the profile table") || !strings.Contains(plan.GetDetail(), "desktop gpu") || strings.Contains(plan.GetDetail(), "box gpu") {
		t.Fatalf("the detail says which numbers came from the profile table: %s", plan.GetDetail())
	}
	learned := mustPlan(t, request(dense70B()), mesh.New([]*mesh.Node{unified, node(box{id: "b2", name: "second", device: 128 * gib, unified: true, stream: 256 * gbps, compute: 20e12, vendor: "apple", version: "b1"}, allShapes)}, lan("box", "b2", 180, 2.3), "box"))
	if strings.Contains(learned.GetDetail(), "profile table") {
		t.Fatalf("learned numbers everywhere: %s", learned.GetDetail())
	}
}

// Every assignment a shape cannot make is in the table with its reason: nodes the caller left out
// and a node not one layer fits on. An unmeasured link is priced at a slow guess until a probe
// runs, and the plan's sources say so
func TestRejectionsAreListed(t *testing.T) {
	desktop := node(box{id: "desk", name: "desktop", device: 24 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1"}, allShapes)
	unified := node(box{id: "box", name: "box", device: 128 * gib, unified: true, stream: 256 * gbps, compute: 20e12, vendor: "apple", version: "b1"}, allShapes)
	small := node(box{id: "tiny", name: "tiny", device: 6 * gib, host: 16 * gib, stream: 200 * gbps, compute: 10e12, vendor: "nvidia", version: "b1"}, allShapes)
	nano := node(box{id: "nano", name: "nano", device: 1 * gib, host: 16 * gib, stream: 150 * gbps, compute: 5e12, vendor: "nvidia", version: "b1"}, allShapes)
	links := append(append(lan("desk", "box", 180, 2.3), lan("box", "tiny", 180, 2.3)...), lan("desk", "nano", 180, 2.3)...)
	m := mesh.New([]*mesh.Node{desktop, unified, small, nano}, links, "desk")
	req := request(dense70B())
	req.Excluded = map[string]string{"gone": "gone is unreachable since 2026-09-25 10:00:00"}
	plan := mustPlan(t, req, m)
	reasons := map[string]bool{}
	for _, c := range plan.GetCandidates() {
		reasons[c.GetReason()] = true
	}
	for _, want := range []string{"gone is unreachable since 2026-09-25 10:00:00", "nano holds no layer: not one fits on it with its cache at 8192 context"} {
		if !reasons[want] {
			t.Fatalf("the table lists %q:\n%v", want, plan.GetCandidates())
		}
	}
	const guess = "unmeasured, priced at 10 ms round trip and 0.1 Gb/s until a probe runs"
	if !strings.Contains(strings.Join(plan.GetSources(), "\n"), "link desktop to tiny "+guess) {
		t.Fatalf("the sources name the unmeasured link and its guess: %v", plan.GetSources())
	}
	// A chain the caller asks for across an unmeasured link is priced with the guess.
	req.Excluded = nil
	req.Shape = v1.Shape_SHAPE_CHAIN
	req.Span = []string{"desk", "tiny"}
	plan = mustPlan(t, req, m)
	if !strings.Contains(strings.Join(plan.GetSources(), "\n"), guess) {
		t.Fatalf("the sources name the unmeasured link and its guess: %v", plan.GetSources())
	}
	priced := false
	for _, c := range all(plan, v1.Shape_SHAPE_CHAIN) {
		if c.GetDecodeSecondsPerToken() > 0 {
			priced = true
		}
	}
	if !priced {
		t.Fatalf("the chain across the unmeasured link is priced: %v", plan.GetCandidates())
	}
	// A lockstep with no link measured is priced the same way, its reason naming the guess.
	va := nodeFor(box{id: "a", name: "a", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "0.11"}, "vllm", rankShapes)
	vb := nodeFor(box{id: "b", name: "b", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "0.11"}, "vllm", rankShapes)
	vreq := vllmRequest(dense70B())
	vreq.Shape = v1.Shape_SHAPE_LOCKSTEP
	vplan := mustPlan(t, vreq, mesh.New([]*mesh.Node{va, vb}, nil, "a"))
	if c := find(vplan, v1.Shape_SHAPE_LOCKSTEP); c == nil || c.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || !strings.Contains(c.GetReason(), "link a to b unmeasured, priced at 10 ms round trip") || c.GetDecodeSecondsPerToken() <= 0 {
		t.Fatalf("lockstep across an unmeasured link is priced with the guess: %v", c)
	}
	if !strings.Contains(strings.Join(vplan.GetSources(), "\n"), "link a to b "+guess) {
		t.Fatalf("the sources name the unmeasured link and its guess: %v", vplan.GetSources())
	}
}

// Seat roles, ranks, and phases come from the runtime's roles: llama.cpp chains stages in phase one
// and the head in phase two, the head's device list naming the stage devices first; vLLM chains
// ranks in one phase, numbered in pipeline order from the head
func TestChainSeatsFollowRuntimeRoles(t *testing.T) {
	desktop := node(box{id: "desk", name: "desktop", device: 24 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1"}, allShapes)
	unified := node(box{id: "box", name: "box", device: 128 * gib, unified: true, stream: 256 * gbps, compute: 20e12, vendor: "apple", version: "b1"}, allShapes)
	req := request(dense70B())
	req.Shape = v1.Shape_SHAPE_CHAIN
	plan := mustPlan(t, req, mesh.New([]*mesh.Node{desktop, unified}, lan("desk", "box", 180, 2.3), "desk"))
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || len(plan.GetSeats()) != 2 {
		t.Fatalf("%s", plan.GetDetail())
	}
	var head, stage *v1.Seat
	for _, s := range plan.GetSeats() {
		switch s.GetRole() {
		case runtimes.RoleHead:
			head = s
		case runtimes.RoleStage:
			stage = s
		}
	}
	if head == nil || stage == nil || head.GetPhase() != 2 || stage.GetPhase() != 1 || head.GetRank() != 0 || stage.GetRank() != 1 || head.GetExposed() || stage.GetExposed() {
		t.Fatalf("llama.cpp roles: head %v stage %v", head, stage)
	}
	if stage.GetLayerFrom() != 0 || stage.GetLayerTo() != head.GetLayerFrom() || head.GetLayerTo() != 80 {
		t.Fatalf("the stage holds the first layers and the head the last: %v %v", stage, head)
	}
	wantIDs := []string{stage.GetNodeId() + "/" + stage.GetDeviceIds()[0], head.GetNodeId() + "-gpu"}
	if strings.Join(head.GetDeviceIds(), ",") != strings.Join(wantIDs, ",") || len(head.GetDeviceBytes()) != 2 || head.GetDeviceBytes()[0] != stage.GetDeviceBytes()[0] {
		t.Fatalf("the head lists the stage devices first, then its own: %v %v", head.GetDeviceIds(), head.GetDeviceBytes())
	}
	if len(stage.GetDeviceIds()) != 1 || stage.GetDeviceIds()[0] != stage.GetNodeId()+"-gpu" {
		t.Fatalf("a stage lists its own devices: %v", stage.GetDeviceIds())
	}
	va := nodeFor(box{id: "a", name: "a", device: 24 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "0.11"}, "vllm", rankShapes)
	vb := nodeFor(box{id: "b", name: "b", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "0.11"}, "vllm", rankShapes)
	vreq := vllmRequest(dense70B())
	vreq.Shape = v1.Shape_SHAPE_CHAIN
	vplan := mustPlan(t, vreq, mesh.New([]*mesh.Node{va, vb}, lan("a", "b", 200, 2.5), "a"))
	if vplan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_FITS || len(vplan.GetSeats()) != 2 {
		t.Fatalf("%s", vplan.GetDetail())
	}
	for _, s := range vplan.GetSeats() {
		if s.GetPhase() != 1 || !s.GetExposed() {
			t.Fatalf("ranks launch together in one phase at the rendezvous: %v", s)
		}
		switch s.GetRank() {
		case 0:
			if s.GetRole() != runtimes.RoleHead || s.GetNodeId() != vplan.GetHead() || s.GetLayerFrom() != 0 {
				t.Fatalf("rank zero is the head at the start of the pipeline: %v", s)
			}
		case 1:
			if s.GetRole() != runtimes.RoleRank || s.GetLayerTo() != 80 {
				t.Fatalf("rank one holds the last layers: %v", s)
			}
		default:
			t.Fatalf("rank %d", s.GetRank())
		}
		if len(s.GetDeviceIds()) != 1 || strings.Contains(s.GetDeviceIds()[0], "/") {
			t.Fatalf("a rank lists its own devices: %v", s.GetDeviceIds())
		}
	}
}

// Equal scores go to the candidate whose head is the conductor, and nothing else reorders by score
func TestEqualScorePrefersConductor(t *testing.T) {
	a := node(box{id: "a", name: "a", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1"}, allShapes)
	b := node(box{id: "b", name: "b", device: 48 * gib, host: 64 * gib, stream: 900 * gbps, compute: 80e12, vendor: "nvidia", version: "b1"}, allShapes)
	links := lan("a", "b", 200, 2.5)
	if p := mustPlan(t, request(dense70B()), mesh.New([]*mesh.Node{a, b}, links, "b")); p.GetShape() != v1.Shape_SHAPE_SOLO || p.GetHead() != "b" {
		t.Fatalf("twins tie, the conductor heads: %v on %s", p.GetShape(), p.GetHead())
	}
	if p := mustPlan(t, request(dense70B()), mesh.New([]*mesh.Node{a, b}, links, "a")); p.GetHead() != "a" {
		t.Fatalf("the conductor heads: %s", p.GetHead())
	}
	// A faster node wins over the conductor by score alone.
	fast := node(box{id: "b", name: "b", device: 48 * gib, host: 64 * gib, stream: 950 * gbps, compute: 80e12, vendor: "nvidia", version: "b1"}, allShapes)
	if p := mustPlan(t, request(dense70B()), mesh.New([]*mesh.Node{a, fast}, links, "a")); p.GetHead() != "b" {
		t.Fatalf("the better score wins over the conductor: %s %s", p.GetHead(), p.GetDetail())
	}
}

// A shape asked for by name that no node supports is refused with the detail
func TestUnsupportedShape(t *testing.T) {
	small := model(shape{layers: 8, layerBytes: 64 * mib, embedding: 1024, density: q4})
	a := node(box{id: "a", name: "a", device: 8 * gib, host: 16 * gib, stream: 300 * gbps, compute: 10e12, vendor: "nvidia", version: "b1"}, []v1.Shape{v1.Shape_SHAPE_SOLO})
	m := mesh.New([]*mesh.Node{a}, nil, "a")
	req := request(small)
	req.Shape = v1.Shape_SHAPE_CHAIN
	plan := mustPlan(t, req, m)
	if plan.GetVerdict() != v1.FitVerdict_FIT_VERDICT_NO || plan.GetDetail() == "" {
		t.Fatalf("plan %v", plan)
	}
	if _, err := mesh.Plan(request(small), mesh.New(nil, nil, "a")); err == nil {
		t.Fatal("an empty span cannot be planned")
	}
}
