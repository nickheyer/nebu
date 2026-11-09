package bodyparse

import "strings"

var suffixes = [...]struct {
	suffix string
	kind   Kind
}{
	{"/v1/chat/completions", KindChatCompletions},
	{"/v1/completions", KindCompletions},
	{"/v1/embeddings", KindEmbeddings},
	{"/v1/responses", KindResponses},
	{"/v1/messages", KindAnthropicMessages},
	{"/api/chat", KindOllamaChat},
	{"/api/generate", KindOllamaGenerate},
	{"/api/embed", KindEmbeddings},
}

// The body shape a request path carries, any prefix before the API root allowed
func KindOf(path string) Kind {
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimRight(path, "/")
	for _, s := range suffixes {
		if strings.HasSuffix(path, s.suffix) {
			return s.kind
		}
	}
	return KindUnknown
}

var kindNames = [...]string{
	KindUnknown:           "unknown",
	KindChatCompletions:   "chat_completions",
	KindCompletions:       "completions",
	KindEmbeddings:        "embeddings",
	KindResponses:         "responses",
	KindAnthropicMessages: "anthropic_messages",
	KindOllamaChat:        "ollama_chat",
	KindOllamaGenerate:    "ollama_generate",
}

func (k Kind) String() string {
	if k < 0 || int(k) >= len(kindNames) {
		return "unknown"
	}
	return kindNames[k]
}
