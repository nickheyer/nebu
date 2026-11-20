package sources

import v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"

// Models Groq serves, a key as a bearer token, listed with context_window: https://console.groq.com/docs/api-reference
var groq = modelCatalog(v1.SourceKind_SOURCE_KIND_GROQ, "groq", "Groq", "https://api.groq.com/openai/v1", v1.ApiFlavor_API_FLAVOR_OPENAI, bearerKey())

func init() {
	// Requests per day and tokens per minute, resets as durations such as 7.66s, https://console.groq.com/docs/rate-limits
	groq.Limits = &Limits{Header: "x-ratelimit-{field}-{window}", Windows: []string{"requests", "tokens"}}
	register(groq)
}
