package bodyparse

import "testing"

func TestKindOf(t *testing.T) {
	cases := []struct {
		path string
		want Kind
	}{
		{"/v1/chat/completions", KindChatCompletions},
		{"/openai/v1/chat/completions", KindChatCompletions},
		{"/v1/chat/completions/", KindChatCompletions},
		{"/v1/chat/completions?x=1", KindChatCompletions},
		{"/v1/completions", KindCompletions},
		{"/v1/embeddings", KindEmbeddings},
		{"/v1/responses", KindResponses},
		{"/v1/messages", KindAnthropicMessages},
		{"/anthropic/v1/messages", KindAnthropicMessages},
		{"/v1/messages/count_tokens", KindUnknown},
		{"/api/chat", KindOllamaChat},
		{"/ollama/api/chat", KindOllamaChat},
		{"/api/generate", KindOllamaGenerate},
		{"/api/embed", KindEmbeddings},
		{"/v1/models", KindUnknown},
		{"/xv1/completions", KindUnknown},
		{"", KindUnknown},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := KindOf(c.path); got != c.want {
				t.Fatalf("KindOf(%q) = %s, want %s", c.path, got, c.want)
			}
		})
	}
}
