package gateway

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Shared request representation for protocol translation.
type Chat struct {
	// chat, generate for a bare prompt, embed, or count for a token count
	Kind        string
	Model       string
	Messages    []Message
	MaxTokens   int
	Temperature *float64
	TopP        *float64
	TopK        *int
	Stop        []string
	Stream      bool
	Tools       []Tool
	// Empty, auto, none, required, or a tool name
	ToolChoice string
	// Texts to embed when Kind is embed
	Inputs []string
}

// Chat turn with a system, user, assistant, or tool role.
type Message struct {
	Role      string
	Parts     []Part
	ToolCalls []ToolCall
	// Tool call ID for a result turn.
	ToolID string
}

// Text or image content. Images use base64 with a media type, or a URL.
type Part struct {
	Type      string
	Text      string
	MediaType string
	Data      string
	URL       string
}

// Tool call with JSON arguments.
type ToolCall struct {
	ID   string
	Name string
	Args string
}

// A tool the client offers
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Complete answer or embedding vectors.
type Result struct {
	ID        string
	Model     string
	Text      string
	ToolCalls []ToolCall
	// stop, length, tool, or filter
	Stop    string
	In, Out int
	Vectors [][]float64
}

// Stream event. Start includes ID and model. Text carries a fragment. Tool
// carries a call or indexed fragment, named in the first fragment. Stop includes
// reason and usage. Error carries the failure message.
type Event struct {
	Kind  string
	Text  string
	Tool  *ToolCall
	Index int
	Res   *Result
}

// Detects image type from bytes, falling back to the declared type.
func mediaTypeOf(raw []byte, header string) string {
	if mt := http.DetectContentType(raw); strings.HasPrefix(mt, "image/") {
		return mt
	}
	if header != "" && strings.HasPrefix(header, "image/") {
		return strings.TrimSpace(strings.Split(header, ";")[0])
	}
	return "image/png"
}

// Detects a base64 image's type from its leading bytes.
func mediaTypeOfBase64(data string) string {
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	raw, err := base64.StdEncoding.DecodeString(head[:len(head)-len(head)%4])
	if err != nil {
		return "image/png"
	}
	return mediaTypeOf(raw, "")
}

// Writes a streamed answer in one flavor
type StreamWriter interface {
	Write(Event) error
	Close() error
}

// Client and runtime protocol adapter.
type Flavor interface {
	// Parses a request for the endpoint path.
	ParseRequest(path string, body []byte) (*Chat, error)
	// Renders a request and returns its endpoint path.
	RenderRequest(c *Chat) (string, []byte, error)
	// Reads a finished answer
	ParseResult(c *Chat, body []byte) (*Result, error)
	// Writes a finished answer
	RenderResult(c *Chat, r *Result) ([]byte, error)
	// Reads a streamed answer, calling emit per event
	ParseStream(r io.Reader, emit func(Event) error) error
	// Starts a streamed answer to the client
	Stream(w http.ResponseWriter, c *Chat) StreamWriter
	// Extracts an error message, or returns empty.
	ErrorMessage(body []byte) string
	// Writes an error
	Error(w http.ResponseWriter, status int, message, kind string)
	// Whether image URLs must be fetched before rendering.
	InlineImages() bool
	// Whether token counts already include images.
	CountsImages() bool
}

var flavors = map[v1.ApiFlavor]Flavor{
	v1.ApiFlavor_API_FLAVOR_OPENAI:    openai{},
	v1.ApiFlavor_API_FLAVOR_ANTHROPIC: anthropic{},
	v1.ApiFlavor_API_FLAVOR_OLLAMA:    ollama{},
	v1.ApiFlavor_API_FLAVOR_SDCPP:     sdcpp{},
}

// Detects the client protocol from the request path and headers.
func clientFlavor(r *http.Request) v1.ApiFlavor {
	switch {
	case strings.HasPrefix(r.URL.Path, sdcppPrefix):
		return v1.ApiFlavor_API_FLAVOR_SDCPP
	case strings.HasPrefix(r.URL.Path, ollamaPrefix):
		return v1.ApiFlavor_API_FLAVOR_OLLAMA
	case strings.HasPrefix(r.URL.Path, anthropicMessages) || r.Header.Get("anthropic-version") != "":
		return v1.ApiFlavor_API_FLAVOR_ANTHROPIC
	}
	return v1.ApiFlavor_API_FLAVOR_OPENAI
}

// Returns the instance protocol, defaulting to OpenAI.
func flavorOf(api v1.ApiFlavor) Flavor {
	if f, ok := flavors[api]; ok {
		return f
	}
	return flavors[v1.ApiFlavor_API_FLAVOR_OPENAI]
}

func bad(format string, args ...any) error {
	return fmt.Errorf("bad request: "+format, args...)
}

func ptr[T any](v T) *T { return &v }

// Parses tool arguments as JSON, returning an empty object on failure.
func jsonArgs(s string) json.RawMessage {
	if json.Valid([]byte(s)) && s != "" {
		return json.RawMessage(s)
	}
	return json.RawMessage("{}")
}

// Extracts an OpenAI or Anthropic error message, or returns empty.
func errorField(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(body, &e)
	return e.Error.Message
}

// Returns the result ID, generating one with prefix if missing.
func resultID(r *Result, prefix string) string {
	if r.ID != "" {
		return r.ID
	}
	return newID(prefix)
}

// OpenAI tool definitions, also used by Ollama.
func toolsFromOAI(tools []oaiTool) []Tool {
	var out []Tool
	for _, t := range tools {
		out = append(out, Tool{Name: t.Function.Name, Description: t.Function.Description, Schema: t.Function.Parameters})
	}
	return out
}

func toolsToOAI(tools []Tool) []oaiTool {
	var out []oaiTool
	for _, t := range tools {
		tool := oaiTool{Type: "function"}
		tool.Function.Name, tool.Function.Description, tool.Function.Parameters = t.Name, t.Description, t.Schema
		out = append(out, tool)
	}
	return out
}

// Calls in the OpenAI shape
func oaiCalls(calls []ToolCall) []oaiToolCall {
	var out []oaiToolCall
	for _, t := range calls {
		tc := oaiToolCall{ID: t.ID, Type: "function"}
		tc.Function.Name, tc.Function.Arguments = t.Name, t.Args
		out = append(out, tc)
	}
	return out
}

// Reads a string or a list of strings
func strs(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		if one == "" {
			return nil
		}
		return []string{one}
	}
	var many []string
	json.Unmarshal(raw, &many)
	return many
}

// Splits a data URL into media type and base64 payload
func dataURL(u string) (string, string, bool) {
	if !strings.HasPrefix(u, "data:") {
		return "", "", false
	}
	meta, data, ok := strings.Cut(strings.TrimPrefix(u, "data:"), ",")
	if !ok || !strings.HasSuffix(meta, ";base64") {
		return "", "", false
	}
	return strings.TrimSuffix(meta, ";base64"), data, true
}

// Flattens tools and turns into text for token counting.
func promptText(c *Chat) string {
	var b strings.Builder
	for _, t := range c.Tools {
		b.WriteString(t.Name + " " + t.Description + " " + string(t.Schema) + "\n")
	}
	for _, m := range c.Messages {
		b.WriteString(m.Role + ": " + textOf(m.Parts))
		for _, t := range m.ToolCalls {
			b.WriteString(" " + t.Name + " " + t.Args)
		}
		b.WriteString("\n")
	}
	for _, in := range c.Inputs {
		b.WriteString(in + "\n")
	}
	return b.String()
}

const imageTokensMax = 1600

// Estimates tokens at four bytes each, plus turn overhead and image area.
func estimateTokens(c *Chat) int {
	n := (len(promptText(c)) + 3) / 4
	for range c.Messages {
		n += 3
	}
	return n + imageTokensOf(c)
}

// Estimates image tokens by area.
func imageTokensOf(c *Chat) int {
	n := 0
	for _, m := range c.Messages {
		for _, p := range m.Parts {
			if p.Type == "image" {
				n += imageTokens(p)
			}
		}
	}
	return n
}

// Adds image tokens when the runtime counted only text.
func withImageTokens(upstream Flavor, c *Chat, n int) int {
	if upstream.CountsImages() {
		return n
	}
	return n + imageTokensOf(c)
}

// Estimates image tokens as area / 750, using the cap for undecodable images.
func imageTokens(p Part) int {
	raw, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil {
		return imageTokensMax
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return imageTokensMax
	}
	return min(imageTokensMax, max(1, (cfg.Width*cfg.Height+749)/750))
}

// Joins the text parts of a message
func textOf(parts []Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// Reads server sent events, calling fn with each event name and data
func readSSE(r io.Reader, fn func(event string, data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var event string
	var data bytes.Buffer
	flush := func() error {
		if data.Len() == 0 {
			event = ""
			return nil
		}
		err := fn(event, bytes.TrimSpace(data.Bytes()))
		event = ""
		data.Reset()
		return err
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}

// Writes one server sent event and flushes it
func writeSSE(w http.ResponseWriter, event string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flush(w)
	return nil
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func streamHeaders(w http.ResponseWriter, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flush(w)
}

// Gathers streamed tool fragments into whole calls, in index order
type toolGather struct {
	calls map[int]*ToolCall
	order []int
}

func (t *toolGather) add(ev Event) {
	if t.calls == nil {
		t.calls = map[int]*ToolCall{}
	}
	c, ok := t.calls[ev.Index]
	if !ok {
		c = &ToolCall{}
		t.calls[ev.Index] = c
		t.order = append(t.order, ev.Index)
	}
	if ev.Tool.ID != "" {
		c.ID = ev.Tool.ID
	}
	if ev.Tool.Name != "" {
		c.Name = ev.Tool.Name
	}
	c.Args += ev.Tool.Args
}

func (t *toolGather) list() []ToolCall {
	out := make([]ToolCall, 0, len(t.order))
	for _, i := range t.order {
		c := *t.calls[i]
		if c.Args == "" {
			c.Args = "{}"
		}
		out = append(out, c)
	}
	return out
}

func newID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// Defaults missing stop reasons, using tool when calls were made.
func stopOf(reason string, calls int) string {
	if reason == "" {
		if calls > 0 {
			return "tool"
		}
		return "stop"
	}
	return reason
}
