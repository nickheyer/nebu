package sources

import v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"

// xAI's Grok models, a key as a bearer token: https://docs.x.ai/docs/api-reference, listed as
// https://docs.x.ai/developers/rest-api-reference/inference/models answers with context_length
var xai = modelCatalog(v1.SourceKind_SOURCE_KIND_XAI, "xai", "xAI", "https://api.x.ai/v1", v1.ApiFlavor_API_FLAVOR_OPENAI, bearerKey())

func init() { register(xai) }
