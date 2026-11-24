package sources

import v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"

// Google's Gemini models through the Gemini API's OpenAI compatibility, a key as a bearer token:
// https://aistudio.google.com/docs/openai
var gemini = modelCatalog(v1.SourceKind_SOURCE_KIND_GEMINI, "gemini", "Gemini", "https://generativelanguage.googleapis.com/v1beta/openai", v1.ApiFlavor_API_FLAVOR_OPENAI, bearerKey())

func init() {
	// A stream names its usage when asked through stream_options, https://ai.google.dev/gemini-api/docs/openai
	gemini.StreamUsage = true
	register(gemini)
}
