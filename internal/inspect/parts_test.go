package inspect

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nickheyer/nebu/pkg/archs"
	"github.com/nickheyer/nebu/pkg/blueprint"
	"github.com/nickheyer/nebu/pkg/cache"
	"github.com/nickheyer/nebu/pkg/descriptor"
	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/all"
	"github.com/nickheyer/nebu/pkg/precision"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

// One tensor of a fake safetensors file
type tensor struct {
	name  string
	dtype string
	shape []uint64
}

var widths = map[string]uint64{"F16": 2, "BF16": 2, "F32": 4, "F8_E4M3": 1}

// Maximum zero-filled payload in test files. Declared size may be larger.
const tailMax = 4096

// Builds a safetensors header with a short zero-filled payload. The fake hub
// reports the full size declared by tensor offsets.
func safetensors(ts ...tensor) []byte {
	header := map[string]any{}
	var off uint64
	for _, t := range ts {
		n := uint64(1)
		for _, d := range t.shape {
			n *= d
		}
		size := n * widths[t.dtype]
		header[t.name] = map[string]any{"dtype": t.dtype, "shape": t.shape, "data_offsets": []uint64{off, off + size}}
		off += size
	}
	js, _ := json.Marshal(header)
	out := make([]byte, 8, 8+len(js)+tailMax)
	binary.LittleEndian.PutUint64(out, uint64(len(js)))
	out = append(out, js...)
	return append(out, make([]byte, min(off, tailMax))...)
}

// Returns file size from the header and tensor offsets.
func declared(data []byte) int {
	n := int(binary.LittleEndian.Uint64(data[:8]))
	var header map[string]struct {
		DataOffsets [2]uint64 `json:"data_offsets"`
	}
	if err := json.Unmarshal(data[8:8+n], &header); err != nil {
		return len(data)
	}
	var end uint64
	for _, t := range header {
		end = max(end, t.DataOffsets[1])
	}
	return 8 + n + int(end)
}

// Uses declared safetensors size or actual byte length for other files.
func listedSize(name string, data []byte) int {
	if strings.HasSuffix(name, ".safetensors") {
		return declared(data)
	}
	return len(data)
}

func blocks(n int) []tensor {
	var out []tensor
	for i := 0; i < n; i++ {
		out = append(out, tensor{fmt.Sprintf("blocks.%d.self_attn.q.weight", i), "F16", []uint64{64, 64}})
	}
	return out
}

// Wan denoiser fixture at the specified precision.
func wan(dtype string) []byte {
	ts := append(blocks(4), tensor{"blocks.0.cross_attn.norm_k.weight", dtype, []uint64{64}}, tensor{"patch_embedding.weight", dtype, []uint64{64, 16, 1, 2, 2}}, tensor{"head.head.weight", dtype, []uint64{64, 64}})
	for i := range ts {
		ts[i].dtype = dtype
	}
	return safetensors(ts...)
}

// Fake Hugging Face API for repository listings and file downloads.
func hub(t *testing.T, repos map[string]map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		for repo, files := range repos {
			switch {
			case p == "/api/models/"+repo || p == "/api/models/"+repo+"/revision/main":
				fmt.Fprintf(w, `{"id":%q,"sha":"abc"}`, repo)
				return
			case p == "/api/models/"+repo+"/refs":
				fmt.Fprint(w, `{"branches":[{"name":"main","targetCommit":"abc"}],"tags":[]}`)
				return
			case p == "/api/models/"+repo+"/tree/main":
				var entries []string
				for name, data := range files {
					entries = append(entries, fmt.Sprintf(`{"type":"file","path":%q,"size":%d}`, name, listedSize(name, data)))
				}
				fmt.Fprint(w, "["+strings.Join(entries, ",")+"]")
				return
			case strings.HasPrefix(p, "/"+repo+"/resolve/main/"):
				name := strings.TrimPrefix(p, "/"+repo+"/resolve/main/")
				if data, ok := files[name]; ok {
					http.ServeContent(w, r, name, timeZero, bytesReader(data))
					return
				}
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func hubInspector(t *testing.T, srv *httptest.Server, stored func() ([]*v1.StoredModel, error)) *Inspector {
	t.Helper()
	srcs, err := sources.Build([]*v1.Source{{Id: "hf", Kind: v1.SourceKind_SOURCE_KIND_HUGGINGFACE, Config: map[string]string{"endpoint": srv.URL}}})
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
	c, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Inspector{Sources: srcs, Formats: fmts, Builder: &descriptor.Builder{Formats: fmts, Archs: families, Scale: precision.Bits{}}, Cache: c, Log: slog.Default(), Stored: stored}
}

func comfyWan() map[string][]byte {
	return map[string][]byte{
		"split_files/vae/wan_2.1_vae.safetensors":                          safetensors(tensor{"encoder.conv1.weight", "F16", []uint64{96, 3, 3, 3, 3}}, tensor{"decoder.conv1.weight", "F16", []uint64{96, 16, 3, 3, 3}}),
		"split_files/text_encoders/umt5_xxl_fp16.safetensors":              safetensors(tensor{"shared.weight", "F16", []uint64{256384, 4096}}, tensor{"encoder.block.0.layer.0.SelfAttention.q.weight", "F16", []uint64{4096, 4096}}),
		"split_files/text_encoders/umt5_xxl_fp8_e4m3fn_scaled.safetensors": safetensors(tensor{"shared.weight", "F8_E4M3", []uint64{256384, 4096}}, tensor{"encoder.block.0.layer.0.SelfAttention.q.weight", "F8_E4M3", []uint64{4096, 4096}}),
		"split_files/text_encoders/t5xxl_fp16.safetensors":                 safetensors(tensor{"shared.weight", "F16", []uint64{32128, 4096}}, tensor{"encoder.block.0.layer.0.SelfAttention.q.weight", "F16", []uint64{4096, 4096}}),
		"split_files/clip_vision/clip_vision_h.safetensors":                safetensors(tensor{"vision_model.encoder.layers.0.self_attn.k_proj.weight", "F16", []uint64{64, 64}}, tensor{"visual_projection.weight", "F16", []uint64{1024, 1280}}),
		"split_files/diffusion_models/wan2.1_t2v_1.3B_fp16.safetensors":    wan("F16"),
	}
}

func byslot(plan *Plan) map[string]Pick {
	out := map[string]Pick{}
	for _, p := range plan.Picks {
		out[p.Fill.Slot] = p
	}
	return out
}

// Parts prefer the model's repository, then blueprint repositories, with matching precision.
func TestPartsResolveAcrossRepositories(t *testing.T) {
	srv := hub(t, map[string]map[string][]byte{
		"someone/wan-lite":                     {"wan_t2v_fp8.safetensors": wan("F8_E4M3"), "wan_t2v_fp16.safetensors": wan("F16")},
		"Comfy-Org/Wan_2.1_ComfyUI_repackaged": comfyWan(),
	})
	i := hubInspector(t, srv, nil)
	plan, err := i.PartsOf(context.Background(), "hf", "someone/wan-lite", "", "wan_t2v_fp8")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Family == nil || plan.Family.ID != "wan" {
		t.Fatalf("family %+v", plan.Family)
	}
	picks := byslot(plan)
	if vae := picks[blueprint.SlotVAE]; !vae.Pulls() || vae.Group.Name != "wan_2.1_vae" || vae.Model.GetRepo() != "Comfy-Org/Wan_2.1_ComfyUI_repackaged" {
		t.Fatalf("vae %+v %v", vae.Group, vae.Err)
	}
	if t5 := picks[blueprint.SlotTextEncoderT5]; !t5.Pulls() || t5.Group.Name != "umt5_xxl_fp8_e4m3fn_scaled" {
		t.Fatalf("expected fp8 umt5 for fp8 denoiser: %+v %v", t5.Group, t5.Err)
	}
	if _, ok := picks[blueprint.SlotImageClipVision]; ok {
		t.Fatal("unexpected image encoder for text-to-video model")
	}
	if err := plan.Unfilled(); err != nil {
		t.Fatal(err)
	}
	if plan.Bytes() < 256384*4096 || len(plan.Pulls()) != 2 {
		t.Fatalf("pulls %d bytes %d", len(plan.Pulls()), plan.Bytes())
	}
	resp := plan.Proto()
	if resp.GetFamily() != "wan" || len(resp.GetParts()) != 2 || resp.GetParts()[1].GetRepo() != "Comfy-Org/Wan_2.1_ComfyUI_repackaged" || resp.GetParts()[1].GetSourceId() != "hf" || resp.GetPullBytes() != plan.Bytes() {
		t.Fatalf("proto %+v", resp)
	}
	// The fp16 denoiser takes the fp16 encoder
	plan, err = i.PartsOf(context.Background(), "hf", "someone/wan-lite", "", "wan_t2v_fp16")
	if err != nil {
		t.Fatal(err)
	}
	if t5 := byslot(plan)[blueprint.SlotTextEncoderT5]; !t5.Pulls() || t5.Group.Name != "umt5_xxl_fp16" {
		t.Fatalf("fp16 %+v %v", t5.Group, t5.Err)
	}
	// Prefer parts from the model's repository.
	plan, err = i.PartsOf(context.Background(), "hf", "Comfy-Org/Wan_2.1_ComfyUI_repackaged", "", "wan2.1_t2v_1.3B_fp16")
	if err != nil {
		t.Fatal(err)
	}
	picks = byslot(plan)
	if vae := picks[blueprint.SlotVAE]; !vae.Pulls() || vae.Model.GetRepo() != "Comfy-Org/Wan_2.1_ComfyUI_repackaged" || vae.Group.Name != "wan_2.1_vae" {
		t.Fatalf("own vae %+v", vae)
	}
}

// Reuse stored parts. Missing required parts report searched repositories.
func TestPartsStoredAndUnfilled(t *testing.T) {
	files := comfyWan()
	delete(files, "split_files/vae/wan_2.1_vae.safetensors")
	srv := hub(t, map[string]map[string][]byte{
		"someone/wan-lite":                     {"wan_t2v_fp16.safetensors": wan("F16")},
		"Comfy-Org/Wan_2.1_ComfyUI_repackaged": files,
	})
	umt5 := &v1.StoredModel{SourceId: "hf", Repo: "city96/umt5-xxl-encoder-gguf", Group: "Q8_0", Bytes: 5, Descriptor_: &v1.Descriptor{Architecture: "t5", Kind: v1.ModelKind_MODEL_KIND_COMPONENT, Params: map[string]float64{"n_vocab": 256384}}, Artifacts: []*v1.StoredArtifact{{Artifact: &v1.Artifact{Path: "umt5-xxl-encoder-Q8_0.gguf", Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS}, Path: "/store/umt5.gguf"}}}
	i := hubInspector(t, srv, func() ([]*v1.StoredModel, error) { return []*v1.StoredModel{umt5}, nil })
	plan, err := i.PartsOf(context.Background(), "hf", "someone/wan-lite", "", "wan_t2v_fp16")
	if err != nil {
		t.Fatal(err)
	}
	picks := byslot(plan)
	if t5 := picks[blueprint.SlotTextEncoderT5]; t5.Stored != umt5 || t5.Pulls() {
		t.Fatalf("expected stored encoder: %+v", t5)
	}
	vae := picks[blueprint.SlotVAE]
	if vae.Err == nil || vae.Pulls() {
		t.Fatalf("expected unresolved VAE: %+v", vae)
	}
	err = plan.Unfilled()
	if err == nil || !strings.Contains(err.Error(), "vae") || !strings.Contains(err.Error(), "Comfy-Org/Wan_2.1_ComfyUI_repackaged") {
		t.Fatalf("unfilled %v", err)
	}
	if p := vae.Proto(); !p.GetRequired() || p.GetError() == "" || p.GetStored() {
		t.Fatalf("proto %+v", p)
	}
	if p := picks[blueprint.SlotTextEncoderT5].Proto(); !p.GetStored() || p.GetRepo() != "city96/umt5-xxl-encoder-gguf" || p.GetSizeBytes() != 5 {
		t.Fatalf("stored proto %+v", p)
	}
}

// Language model parts are bundled in the group.
func TestPartsOfALanguageModel(t *testing.T) {
	srv := hub(t, map[string]map[string][]byte{
		"someone/tiny": {
			"model.safetensors": safetensors(tensor{"model.embed_tokens.weight", "F16", []uint64{100, 8}}, tensor{"model.layers.0.self_attn.q_proj.weight", "F16", []uint64{8, 8}}, tensor{"lm_head.weight", "F16", []uint64{100, 8}}),
			"config.json":       []byte(`{"architectures":["LlamaForCausalLM"],"num_hidden_layers":1,"hidden_size":8,"num_attention_heads":1,"max_position_embeddings":16,"vocab_size":100}`),
			"tokenizer.json":    []byte(`{}`),
		},
	})
	i := hubInspector(t, srv, nil)
	plan, err := i.PartsOf(context.Background(), "hf", "someone/tiny", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Family == nil || plan.Family.ID != blueprint.Language || len(plan.Pulls()) != 0 || plan.Unfilled() != nil {
		t.Fatalf("plan %+v %v", plan.Family, plan.Unfilled())
	}
	picks := byslot(plan)
	if !picks[blueprint.SlotWeights].Bundled || !picks[blueprint.SlotConfig].Bundled || !picks[blueprint.SlotTokenizer].Bundled {
		t.Fatalf("bundled %+v", picks)
	}
	if _, ok := picks[blueprint.SlotProjector]; ok {
		t.Fatal("unexpected projector for text-only model")
	}
}

// Standalone components have no blueprint.
func TestPartsOfAComponent(t *testing.T) {
	srv := hub(t, map[string]map[string][]byte{"Comfy-Org/Wan_2.1_ComfyUI_repackaged": comfyWan()})
	i := hubInspector(t, srv, nil)
	plan, err := i.PartsOf(context.Background(), "hf", "Comfy-Org/Wan_2.1_ComfyUI_repackaged", "", "wan_2.1_vae")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Family != nil || len(plan.Picks) != 0 || plan.Proto().GetFamily() != "" {
		t.Fatalf("plan %+v", plan)
	}
	var _ formats.Group
}

// Declared pipelines bundle all parts in the group.
func TestPartsOfADeclaredPipeline(t *testing.T) {
	srv := hub(t, map[string]map[string][]byte{"MiniMaxAI/MiniMax-H3": {
		"model_index.json":                                    []byte(`{"_class_name":"MiniMaxH3ModularPipeline","text_encoder":["transformers","Qwen3VLForConditionalGeneration",{"subfolder":"text_encoder"}],"tokenizer":["transformers","Qwen2TokenizerFast",{"subfolder":"tokenizer"}],"vae":["diffusers","AutoencoderKLMiniMaxH3",{"subfolder":"vae"}],"audio_vae":["diffusers","AutoencoderKLMiniMaxH3Audio",{"subfolder":"audio_vae"}],"transformer":["diffusers","MiniMaxH3Transformer3DModel",{"subfolder":"transformer"}],"transformer_ref":["diffusers","MiniMaxH3Transformer3DModel",{"subfolder":"transformer_ref"}]}`),
		"transformer/config.json":                             []byte(`{"_class_name":"MiniMaxH3Transformer3DModel","hidden_size":5376,"num_layers":50}`),
		"transformer/diffusion_pytorch_model.safetensors":     safetensors(tensor{"blocks.0.attn.to_q.weight", "BF16", []uint64{8, 8}}),
		"transformer_ref/config.json":                         []byte(`{"_class_name":"MiniMaxH3Transformer3DModel","hidden_size":5376,"num_layers":50}`),
		"transformer_ref/diffusion_pytorch_model.safetensors": safetensors(tensor{"blocks.0.attn.to_q.weight", "BF16", []uint64{8, 8}}),
		"vae/config.json":                                     []byte(`{"_class_name":"AutoencoderKLMiniMaxH3"}`),
		"vae/diffusion_pytorch_model.safetensors":             safetensors(tensor{"decoder.conv_in.weight", "F32", []uint64{8, 24, 3, 3, 3}}),
		"audio_vae/config.json":                               []byte(`{"_class_name":"AutoencoderKLMiniMaxH3Audio"}`),
		"audio_vae/diffusion_pytorch_model.safetensors":       safetensors(tensor{"encoder.block.0.weight", "F32", []uint64{4, 4}}, tensor{"decoder.block.0.weight", "F32", []uint64{4, 4}}),
		"text_encoder/config.json":                            []byte(`{"architectures":["Qwen3VLForConditionalGeneration"],"text_config":{"hidden_size":5120,"num_hidden_layers":1,"num_attention_heads":1},"vision_config":{"depth":27}}`),
		"text_encoder/model.safetensors":                      safetensors(tensor{"model.layers.0.self_attn.q_proj.weight", "BF16", []uint64{8, 8}}, tensor{"model.visual.blocks.0.attn.qkv.weight", "BF16", []uint64{8, 8}}),
		"tokenizer/tokenizer.json":                            []byte(`{}`),
		"FL2VA/model_index.json":                              []byte(`{"_class_name":"MiniMaxH3Pipeline","text_encoder":["transformers","MiniMaxH3Qwen3VLHFEncoder"],"video_vae":["diffusers","MiniMaxH3VideoVAE"],"audio_vae":["diffusers","MiniMaxH3AudioVAE"],"transformer":["diffusers","MiniMaxH3DiTModel"]}`),
		"FL2VA/transformer/config.json":                       []byte(`{"_class_name":"MiniMaxH3DiTModel","hidden_size":5376,"num_layers":50}`),
		"FL2VA/transformer/model.safetensors":                 safetensors(tensor{"blocks.0.attn.to_q.weight", "BF16", []uint64{8, 8}}),
		"FL2VA/video_vae/source/config.json":                  []byte(`{"_class_name":"AutoencoderKLLegacy"}`),
		"FL2VA/video_vae/source/model.safetensors":            safetensors(tensor{"decoder.conv_in.weight", "F32", []uint64{8, 24, 3, 3, 3}}),
		"FL2VA/audio_vae/config.json":                         []byte(`{"_class_name":"MiniMaxH3AudioVAE"}`),
		"FL2VA/audio_vae/model.safetensors":                   safetensors(tensor{"encoder.block.0.weight", "F32", []uint64{4, 4}}),
		"FL2VA/text_encoder/config.json":                      []byte(`{"architectures":["Qwen3VLForConditionalGeneration"],"vision_config":{"depth":27}}`),
		"FL2VA/text_encoder/model.safetensors":                safetensors(tensor{"model.layers.0.self_attn.q_proj.weight", "BF16", []uint64{8, 8}}),
	}})
	i := hubInspector(t, srv, nil)
	_, model, err := i.Resolve(context.Background(), "hf", "MiniMaxAI/MiniMax-H3", "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range i.Formats.Groups(model) {
		names = append(names, g.FormatID+":"+g.Name)
	}
	// Two denoisers create separate variants with shared supporting parts.
	if strings.Join(names, ",") != "diffusers:FL2VA,diffusers:transformer,diffusers:transformer_ref" {
		t.Fatalf("groups %v", names)
	}
	vae := map[string]string{"transformer": "vae", "transformer_ref": "vae", "FL2VA": "video_vae"}
	for _, group := range []string{"transformer", "transformer_ref", "FL2VA"} {
		plan, err := i.PartsOf(context.Background(), "hf", "MiniMaxAI/MiniMax-H3", "", group)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Family == nil || plan.Family.ID != "minimax_h3" || plan.Descriptor.GetKind() != v1.ModelKind_MODEL_KIND_DIFFUSION || strings.Join(plan.Descriptor.GetGenerates(), ",") != "video" {
			t.Fatalf("%s: family %+v kind %v generates %v", group, plan.Family, plan.Descriptor.GetKind(), plan.Descriptor.GetGenerates())
		}
		if len(plan.Pulls()) != 0 || plan.Unfilled() != nil || plan.Bytes() != 0 {
			t.Fatalf("%s: expected bundled pipeline parts: %+v %v", group, plan.Pulls(), plan.Unfilled())
		}
		// Each bundled slot reports its subfolder and size. The vision tower shares
		// the text encoder's files.
		picks := byslot(plan)
		for slot, sub := range map[string]string{blueprint.SlotVAE: vae[group], blueprint.SlotVAEAudio: "audio_vae", blueprint.SlotTextEncoderLLM: "text_encoder", blueprint.SlotTextEncoderVision: "text_encoder"} {
			pick, ok := picks[slot]
			if !ok || !pick.Bundled || pick.Subfolder != sub || len(pick.Files) == 0 {
				t.Fatalf("%s: %s %+v", group, slot, pick)
			}
		}
		if plan.Descriptor.GetPrecision().GetBits() != 16 {
			t.Fatalf("%s: precision %+v", group, plan.Descriptor.GetPrecision())
		}
		for _, p := range plan.Proto().GetParts() {
			if !p.GetBundled() || p.GetSubfolder() == "" || p.GetSizeBytes() == 0 {
				t.Fatalf("%s: %+v", group, p)
			}
		}
		// Each variant contains only its own denoiser.
		other := "transformer_ref/"
		if group == "transformer_ref" {
			other = "transformer/"
		}
		g, err := formats.FindGroup(i.Formats.Groups(model), group)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range GroupArtifacts(g) {
			if strings.HasPrefix(a.GetPath(), other) {
				t.Fatalf("%s holds %s", group, a.GetPath())
			}
		}
	}
}
