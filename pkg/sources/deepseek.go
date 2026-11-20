package sources

import v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"

// DeepSeek's models, a key as a bearer token: https://api-docs.deepseek.com/, listed as
// https://api-docs.deepseek.com/api/list-models answers with context_window
var deepseek = modelCatalog(v1.SourceKind_SOURCE_KIND_DEEPSEEK, "deepseek", "DeepSeek", "https://api.deepseek.com", v1.ApiFlavor_API_FLAVOR_OPENAI, bearerKey())

func init() { register(deepseek) }
