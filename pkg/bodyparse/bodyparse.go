// Package bodyparse reads routing fields out of inference request bodies without unmarshalling them whole
package bodyparse

import "errors"

var (
	ErrNoModel  = errors.New("the body names no model")
	ErrNotJSON  = errors.New("the body is not a JSON object")
	ErrTooLarge = errors.New("the body is over the limit")
)

// The shape of the request body by its path
type Kind int

const (
	KindUnknown Kind = iota
	KindChatCompletions
	KindCompletions
	KindEmbeddings
	KindResponses
	KindAnthropicMessages
	KindOllamaChat
	KindOllamaGenerate
)

// Names a typed value derived from a body once and shared by every stage that reads it
type ViewKey string

// A request body with its routing fields read lazily and exactly once, every mutation answering a new Document
type Document interface {
	Bytes() []byte
	Kind() Kind
	// The model field, ErrNoModel when absent
	Model() (string, error)
	// The raw value of the last top level member named name, false when absent
	Field(name string) ([]byte, bool)
	// Every message's text in order, system first, for prefix hashing, read once and cached
	PromptText() string
	// The prompt token ids when the client sent tokens instead of text
	PromptTokenIDs() []int
	// Whether the request asks for a stream
	Stream() bool
	// The max output tokens the request bounds, zero when unbounded
	MaxTokens() int
	// A new Document with the model field replaced in place, every other byte untouched and every view dropped
	WithModel(model string) (Document, error)
	// A new Document with one top level field set, appended when absent, every view dropped
	WithField(name string, rawJSON []byte) (Document, error)
	// A new Document with every named top level field set in one pass, every view dropped
	WithFields(fields map[string][]byte) (Document, error)
	// The value a key names, built from this Document on the first read and shared by every later one
	View(key ViewKey, build func(Document) (any, error)) (any, error)
	// A new Document over the same bytes carrying value under key beside the views it already holds
	WithView(key ViewKey, value any) Document
}

// Byte limit above which a chunked body stream is split
const ChunkBytes = 62000
