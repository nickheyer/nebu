package sources

import v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"

// Mistral's models, a key as a bearer token, listed with max_context_length: https://docs.mistral.ai/api/endpoint/models
var mistral = modelCatalog(v1.SourceKind_SOURCE_KIND_MISTRAL, "mistral", "Mistral", "https://api.mistral.ai/v1", v1.ApiFlavor_API_FLAVOR_OPENAI, bearerKey())

func init() { register(mistral) }
