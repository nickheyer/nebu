// Package relay reads the reference a provider's model is stored as: where it is served, over which
// API, under which id. Registered once per API so each stored model pairs with exactly one runtime.
package relay

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/nickheyer/nebu/pkg/formats"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/text"
)

// Metadata keys a stored reference's descriptor keeps
const (
	KeyEndpoint = "relay.endpoint"
	KeyAPI      = "relay.api"
	KeyModel    = "relay.model"
	KeyContext  = "relay.context"
	// The reasoning effort levels the model takes as the JSON the reference holds them in, and its default among them
	KeyEfforts = "relay.efforts"
	KeyEffort  = "relay.effort"
)

// The reasoning effort levels a stored reference's metadata names, lowest first, and the default among them, none when it names none
func Efforts(meta map[string]string) ([]sources.Effort, string) {
	var levels []sources.Effort
	if json.Unmarshal([]byte(meta[KeyEfforts]), &levels) != nil {
		return nil, ""
	}
	return levels, meta[KeyEffort]
}

// Largest reference file read
const maxReference = 64 << 10

// A provider's model served over one API
type Format struct {
	Speaks v1.ApiFlavor
}

// The format of each API the relay serves
func Formats() []formats.Format {
	return []formats.Format{Format{Speaks: v1.ApiFlavor_API_FLAVOR_OPENAI}, Format{Speaks: v1.ApiFlavor_API_FLAVOR_ANTHROPIC}}
}

func (f Format) ID() string { return text.Enum(f.Speaks) }

func (f Format) name() string {
	if f.Speaks == v1.ApiFlavor_API_FLAVOR_ANTHROPIC {
		return "Anthropic"
	}
	return "OpenAI"
}

func (f Format) Description() string {
	return "A model a provider serves over the " + f.name() + " API, stored as a reference to it"
}

func (f Format) Blurb() string { return "Provider model, " + f.name() + " API" }

// Below every weights format, the reference file's name being its own
func (Format) Priority() int               { return 1 }
func (Format) Requires() []v1.ArtifactRole { return nil }

// Claims the API's reference file as the weights of a group named for the API
func (f Format) Classify(p string) (formats.Claim, bool) {
	if _, base := formats.Split(p); base != sources.ReferenceFile(f.Speaks) {
		return formats.Claim{}, false
	}
	return formats.Claim{Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, Group: f.ID()}, true
}

// Reads the reference into metadata, a model with no tensors
func (Format) Read(ctx context.Context, open formats.Opener, g *formats.Group) (*v1.RawModel, error) {
	raw := &v1.RawModel{FormatId: g.FormatID, Group: g.Name, Metadata: map[string]string{}}
	if len(g.Weights) == 0 {
		return raw, nil
	}
	data, err := formats.ReadAll(ctx, open, g.Weights[0], maxReference)
	if err != nil {
		return nil, err
	}
	ref, err := sources.ParseReference(data)
	if err != nil {
		return nil, err
	}
	raw.Metadata[KeyEndpoint], raw.Metadata[KeyAPI], raw.Metadata[KeyModel] = ref.Endpoint, ref.API, ref.Model
	if ref.Context > 0 {
		raw.Metadata[KeyContext] = strconv.FormatUint(ref.Context, 10)
	}
	if len(ref.Efforts) > 0 {
		levels, err := json.Marshal(ref.Efforts)
		if err != nil {
			return nil, err
		}
		raw.Metadata[KeyEfforts] = string(levels)
		if ref.Effort != "" {
			raw.Metadata[KeyEffort] = ref.Effort
		}
	}
	return raw, nil
}

func (Format) Architecture(*v1.RawModel) string { return "" }

// The context window the provider reports as the trained context
func (Format) Params(raw *v1.RawModel) formats.Params {
	return formats.Params{ContextTrain: formats.Num(raw.GetMetadata(), KeyContext)}
}

func (Format) Tensor(string) (v1.TensorGroupKind, int32) {
	return v1.TensorGroupKind_TENSOR_GROUP_KIND_OTHER, -1
}
func (Format) DraftFrom(formats.Params) int32               { return -1 }
func (Format) Elements(*v1.TensorInfo, *v1.RawModel) uint64 { return 0 }
func (Format) Precision(*v1.RawModel, string) formats.Words { return formats.Words{} }

func (Format) Metadata(raw *v1.RawModel) map[string]string {
	out := map[string]string{}
	for _, k := range []string{KeyEndpoint, KeyAPI, KeyModel, KeyContext, KeyEfforts, KeyEffort} {
		if v := raw.GetMetadata()[k]; v != "" {
			out[k] = v
		}
	}
	return out
}
