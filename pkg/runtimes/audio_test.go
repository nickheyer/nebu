package runtimes

import (
	"slices"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func TestAudioCapabilities(t *testing.T) {
	var _ AudioServer = VLLM{}
	var _ AudioServer = LlamaCpp{}
	cases := []struct {
		name  string
		rt    AudioServer
		param map[string]string
		inv   *v1.RuntimeInventory
		caps  []string
		chats bool
	}{
		{"vllm tasks unknown", VLLM{}, nil, nil, nil, true},
		{"vllm generate", VLLM{}, map[string]string{VLLMSupportedTasks: "generate"}, nil, nil, true},
		{"vllm realtime alone", VLLM{}, map[string]string{VLLMSupportedTasks: "generate,realtime"}, nil, nil, true},
		{"vllm transcription only", VLLM{}, map[string]string{VLLMSupportedTasks: "transcription"}, nil, []string{CapabilityTranscription}, false},
		{"vllm chat and transcription", VLLM{}, map[string]string{VLLMSupportedTasks: "generate,transcription,realtime"}, nil, []string{CapabilityTranscription}, true},
		{"vllm over grpc", VLLM{}, map[string]string{VLLMSupportedTasks: "transcription", "grpc": "true"}, nil, nil, false},
		{"llama.cpp text", LlamaCpp{}, nil, &v1.RuntimeInventory{Modalities: []string{"text", "image"}}, nil, true},
		{"llama.cpp audio projector", LlamaCpp{}, nil, &v1.RuntimeInventory{Modalities: []string{"text", "audio"}}, []string{CapabilityTranscription}, true},
		{"llama.cpp no inventory", LlamaCpp{}, nil, nil, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			caps, chats := c.rt.Audio(c.param, c.inv)
			if !slices.Equal(caps, c.caps) || chats != c.chats {
				t.Fatalf("Audio = %v %v, want %v %v", caps, chats, c.caps, c.chats)
			}
		})
	}
	for _, rt := range []Runtime{SGLang{}, NeMo{}, Relay{}} {
		if _, ok := rt.(AudioServer); ok {
			t.Fatalf("%s claims audio endpoints it does not serve", rt.ID())
		}
	}
}
