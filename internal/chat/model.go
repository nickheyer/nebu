// Package chat holds the neutral form of a model exchange and the protocols that read and write it
package chat

import (
	"encoding/json"
	"time"

	"github.com/nickheyer/nebu/internal/tools"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// What an endpoint asks of a model
type Kind int

const (
	None Kind = iota
	Chat
	Generate
	Embed
	Count
	Image
	Video
)

// Stop reasons as a result carries them
const (
	StopEnd    = "stop"
	StopLength = "length"
	StopTool   = "tool"
	StopFilter = "filter"
)

// One request to a model, as every protocol reads and writes it
type Request struct {
	Kind        Kind
	Model       string
	Messages    []Message
	MaxTokens   int
	Temperature *float64
	TopP        *float64
	TopK        *int
	Stop        []string
	Stream      bool
	// Whether a streamed answer ends with a chunk of usage alone, as OpenAI clients ask with stream_options
	IncludeUsage bool
	// Whether the answer goes out in the OpenAI Responses shape rather than chat completions
	Responses bool
	Tools     []Tool
	// Empty, auto, none, required, or a tool name
	ToolChoice string
	// Tool endpoints the request asks the gateway to connect for it alone
	Sources []Source
	// Texts to embed
	Inputs []string
	// Fields of the wire request no protocol maps, carried as they are
	Extra map[string]json.RawMessage
}

// A tool endpoint a request names, connected for the request alone
type Source struct {
	Name  string
	URL   string
	Token string
	// Tools the endpoint may offer, every tool when empty
	Allowed []string
}

// One turn with a system, user, assistant, or tool role
type Message struct {
	Role      string
	Parts     []Part
	ToolCalls []ToolCall
	// The call a tool turn answers
	ToolID string
	Extra  map[string]json.RawMessage
}

// Text, an image as base64 or a URL, or thinking with its signature in Data
type Part struct {
	Type      string
	Text      string
	MediaType string
	Data      string
	URL       string
	Extra     map[string]json.RawMessage
}

// A tool call with JSON arguments
type ToolCall struct {
	ID   string
	Name string
	Args string
}

// A tool offered to the model, by schema or by a type the API itself provides
type Tool struct {
	Type        string
	Name        string
	Description string
	Schema      json.RawMessage
	Strict      *bool
	// Set for a tool the gateway provides and runs itself
	Provided *Provided
	Extra    map[string]json.RawMessage
}

// A tool the gateway runs on the client's behalf, with the limits the client set
type Provided struct {
	Kind    v1.ToolServerKind
	MaxUses int
	// Domains results and pages are kept to, or kept from
	Allowed, Blocked []string
	Country          string
	// Characters a fetched page is cut to, zero for the default
	MaxChars int
}

// A finished answer or embedding vectors
type Result struct {
	ID    string
	Model string
	Text  string
	// The reasoning the model wrote before its answer
	Thinking  string
	ToolCalls []ToolCall
	Stop      string
	Usage     Usage
	Vectors   [][]float64
	// Tool calls the gateway ran itself on the way to the answer, in order
	Runs  []*Run
	Extra map[string]json.RawMessage
}

// Tokens of an answer, by round when it took more than one
type Usage struct {
	In, Out int
	Rounds  []Round
	// Speculative decoding counters when the runtime reports them
	DraftOffered, DraftAccepted int
}

// The prompt and completion tokens of one model round
type Round struct {
	In, Out int
}

// A tool call the gateway ran itself, with what the tool answered
type Run struct {
	Call   ToolCall
	Server string
	// The server's own name for the tool
	Tool string
	Kind v1.ToolServerKind
	// Whether the server came from the request rather than Settings
	AdHoc  bool
	Result *tools.Result
	Took   time.Duration
}

// What happened in an answer, one piece at a time
type EventKind int

const (
	// The id and model of the answer
	Started EventKind = iota + 1
	// A fragment of the answer's text
	Said
	// A fragment of the reasoning before the answer
	Thought
	// A tool call or an indexed fragment of one, named in the first fragment
	Called
	// A tool the gateway ran, with its answer
	Ran
	// The stop reason and usage
	Stopped
	// The failure that ended the answer
	Failed
)

// One piece of an answer
type Event struct {
	Kind    EventKind
	Text    string
	Tool    *ToolCall
	Index   int
	Run     *Run
	Res     *Result
	Failure *Failure
}

// Counts the runs of a kind that reached their provider
func (r *Result) RunsOf(kind v1.ToolServerKind) int {
	n := 0
	for _, run := range r.Runs {
		if run.Kind == kind && !run.AdHoc && run.Result.Asked {
			n++
		}
	}
	return n
}

// The answer's rounds, the answer itself when the runtime lists none
func (u Usage) RoundsOrSelf() []Round {
	if len(u.Rounds) > 0 {
		return u.Rounds
	}
	return []Round{{In: u.In, Out: u.Out}}
}

// Defaults a missing stop reason, a turn with calls having stopped for them unless cut short
func StopOf(reason string, calls int) string {
	if calls > 0 && (reason == "" || reason == StopEnd) {
		return StopTool
	}
	if reason == "" {
		return StopEnd
	}
	return reason
}
