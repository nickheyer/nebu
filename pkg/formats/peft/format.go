// Package peft reads PEFT adapter weights and adapter_config.json.
package peft

import (
	"context"
	"io"
	"path"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats"
	"github.com/nickheyer/nebu/pkg/formats/diffusion"
	"github.com/nickheyer/nebu/pkg/formats/torch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

type Format struct{}

func (Format) ID() string          { return "peft" }
func (Format) Description() string { return "PEFT adapter directory" }
func (Format) Blurb() string {
	return "PEFT adapter weights and config for a language model or diffusion model"
}

// Claim adapters before the safetensors language model format.
func (Format) Priority() int               { return 6 }
func (Format) Requires() []v1.ArtifactRole { return nil }

// Adapter config metadata prefix and file limits.
const (
	prefix    = "adapter."
	config    = "adapter_config.json"
	maxConfig = 1 << 20
)

// Groups adapter weights and shards by directory.
func (Format) Classify(p string) (formats.Claim, bool) {
	dir, base := formats.Split(p)
	lower := strings.ToLower(base)
	group := dir
	if group == "" {
		group = "default"
	}
	switch {
	case base == config:
		return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_CONFIG}, true
	case strings.HasPrefix(lower, "adapter_model") && (strings.HasSuffix(lower, ".safetensors") || strings.HasSuffix(lower, ".bin")):
		stem := base[:strings.LastIndex(base, ".")]
		_, index, count, _ := formats.Shard(stem)
		return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, Group: group, ShardIndex: index, ShardCount: count}, true
	}
	return formats.Claim{}, false
}

// Reads weight headers and adapter config metadata.
func (Format) Read(ctx context.Context, open formats.Opener, g *formats.Group) (*v1.RawModel, error) {
	raw, err := formats.EachWeight(ctx, open, g, func(ra io.ReaderAt, size int64) (map[string]string, []*v1.TensorInfo, error) {
		if isTorch(g) {
			ck, err := torch.ReadZip(ra, size)
			if err != nil {
				return nil, nil, err
			}
			return ck.Metadata, ck.Tensors, nil
		}
		return formats.SafetensorsHeader(ra, size)
	})
	if err != nil {
		return nil, err
	}
	for _, a := range g.Files[v1.ArtifactRole_ARTIFACT_ROLE_CONFIG] {
		if path.Base(a.GetPath()) != config {
			continue
		}
		data, err := formats.ReadAll(ctx, open, a, maxConfig)
		if err == nil {
			err = text.FlattenJSON(data, "adapter", raw.Metadata)
		}
		if err != nil {
			return nil, err
		}
	}
	return raw, nil
}

// Reports whether the weights use torch serialization.
func isTorch(g *formats.Group) bool {
	return len(g.Weights) > 0 && strings.HasSuffix(strings.ToLower(g.Weights[0].GetPath()), ".bin")
}

// Infers LoRA, LyCORIS, or DoRA from tensor names.
func (Format) Architecture(raw *v1.RawModel) string { return diffusion.Architecture(raw) }

func (Format) Params(raw *v1.RawModel) formats.Params { return formats.Params{} }

// Adapter weights use one placement group.
func (Format) Tensor(string) (v1.TensorGroupKind, int32) {
	return v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, -1
}

func (Format) DraftFrom(formats.Params) int32                   { return -1 }
func (Format) Elements(t *v1.TensorInfo, _ *v1.RawModel) uint64 { return t.GetElements() }

func (Format) Precision(raw *v1.RawModel, group string) formats.Words {
	return diffusion.Format{}.Precision(raw, group)
}

// Retains inferred adapter type and config metadata, including base model, rank, alpha, and target
// modules.
func (Format) Metadata(raw *v1.RawModel) map[string]string {
	out := diffusion.Metadata(raw)
	for k, v := range raw.GetMetadata() {
		if strings.HasPrefix(k, prefix) && len(v) < 200 {
			out[k] = v
		}
	}
	return out
}
