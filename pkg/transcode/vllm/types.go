package vllm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Turns chat messages and tools into prompt text with the model chat template
type Renderer interface {
	Render(messages []Message, tools []Tool, addGenerationPrompt bool) (string, error)
}

// Splits model output into content and complete tool calls, ok is false when the text is not tool output
type ToolParser interface {
	Parse(text string) (content string, calls []ToolCall, ok bool)
}

// Decodes token ids into text with the model tokenizer
type Detokenizer interface {
	Decode(ids []uint32) (string, error)
}

// Encodes prompt text into token ids with the model tokenizer
type Tokenizer interface {
	Encode(text string, addSpecialTokens bool) ([]uint32, error)
}

// Decodes ids as they arrive, holding text back until its characters complete, Flush giving what is held
type TokenStream interface {
	Step(id uint32) (string, error)
	Flush() string
}

// A detokenizer that also decodes incrementally, for answers whose text the host builds from token ids
type StreamDetokenizer interface {
	Detokenizer
	Stream(skipSpecialTokens bool) TokenStream
}

// One OpenAI chat message whose string content arrives as a single text part
type Message struct {
	Role       string        `json:"role"`
	Content    []ContentPart `json:"content"`
	Name       string        `json:"name,omitempty"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

// One text part of a chat message
type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// One OpenAI function tool offered to the model
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// A callable function by name, purpose and JSON schema parameters
type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// One function call made by the model
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// The called function name and its arguments as JSON text
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Decodes OpenAI message JSON with string, part list or null content and refuses parts that are not text
func (m *Message) UnmarshalJSON(b []byte) error {
	var raw struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		Name       string          `json:"name"`
		ToolCalls  []ToolCall      `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw.Role == "" {
		return errors.New("message role is required")
	}
	*m = Message{Role: raw.Role, Name: raw.Name, ToolCalls: raw.ToolCalls, ToolCallID: raw.ToolCallID}
	content := bytes.TrimSpace(raw.Content)
	switch {
	case len(content) == 0 || string(content) == "null":
		return nil
	case content[0] == '"':
		var text string
		if err := json.Unmarshal(content, &text); err != nil {
			return err
		}
		m.Content = []ContentPart{{Type: "text", Text: text}}
		return nil
	}
	var parts []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return err
	}
	for _, p := range parts {
		if p.Type != "text" {
			return fmt.Errorf("content part type %q cannot travel in a text prompt", p.Type)
		}
		if p.Text == nil {
			return errors.New("text content part lacks text")
		}
		m.Content = append(m.Content, ContentPart{Type: "text", Text: *p.Text})
	}
	return nil
}
