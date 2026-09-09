package formats

import "math"

// The architecture parameters a header names, the ones every planner formula reads
//
// Zero means the header did not say. Derive fills what follows from what was read: heads without
// a separate key value count share one, a head width follows from the embedding and head count,
// and a layer count the header lacks is the number of layers seen among the tensors.
type Params struct {
	Layers            float64
	Embedding         float64
	Heads             float64
	HeadsKV           float64
	HeadDim           float64
	HeadDimV          float64
	ContextTrain      float64
	AttentionInterval float64
	Vocab             float64
	Experts           float64
	ExpertsUsed       float64
	DraftLayers       float64
	KVLoraRank        float64
	RopeDim           float64
	SlidingWindow     float64
	SlidingPattern    float64
	// Elements of the word embedding, for headers that count no vocabulary
	EmbeddingElements float64
}

// Fills the parameters that follow from others, layersSeen being the layers counted among the tensors
func (p *Params) Derive(layersSeen float64) {
	if p.Layers == 0 {
		p.Layers = layersSeen
	}
	if p.HeadsKV == 0 {
		p.HeadsKV = p.Heads
	}
	if p.HeadDim == 0 && p.Heads > 0 {
		p.HeadDim = p.Embedding / p.Heads
	}
	if p.HeadDimV == 0 {
		p.HeadDimV = p.HeadDim
	}
	if p.Vocab == 0 && p.Embedding > 0 && p.EmbeddingElements > 0 {
		p.Vocab = p.EmbeddingElements / p.Embedding
	}
}

// The parameters by the names the API and its clients read them under, zeros left out
func (p Params) Map() map[string]float64 {
	out := map[string]float64{}
	put := func(name string, v float64) {
		if v != 0 && !math.IsNaN(v) && !math.IsInf(v, 0) {
			out[name] = v
		}
	}
	put("n_layer", p.Layers)
	put("n_embd", p.Embedding)
	put("n_head", p.Heads)
	put("n_head_kv", p.HeadsKV)
	put("head_dim", p.HeadDim)
	put("head_dim_v", p.HeadDimV)
	put("n_ctx_train", p.ContextTrain)
	put("attn_interval", p.AttentionInterval)
	put("n_vocab", p.Vocab)
	put("n_expert", p.Experts)
	put("n_expert_used", p.ExpertsUsed)
	put("n_layer_draft", p.DraftLayers)
	put("kv_lora_rank", p.KVLoraRank)
	put("rope_dim", p.RopeDim)
	put("n_swa", p.SlidingWindow)
	put("swa_pattern", p.SlidingPattern)
	return out
}

// Reads the parameters back from the map a descriptor carries
func ParamsOf(m map[string]float64) Params {
	return Params{
		Layers:            m["n_layer"],
		Embedding:         m["n_embd"],
		Heads:             m["n_head"],
		HeadsKV:           m["n_head_kv"],
		HeadDim:           m["head_dim"],
		HeadDimV:          m["head_dim_v"],
		ContextTrain:      m["n_ctx_train"],
		AttentionInterval: m["attn_interval"],
		Vocab:             m["n_vocab"],
		Experts:           m["n_expert"],
		ExpertsUsed:       m["n_expert_used"],
		DraftLayers:       m["n_layer_draft"],
		KVLoraRank:        m["kv_lora_rank"],
		RopeDim:           m["rope_dim"],
		SlidingWindow:     m["n_swa"],
		SlidingPattern:    m["swa_pattern"],
	}
}
