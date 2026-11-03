// Package chat holds the neutral form of a model exchange and the protocols that read and write it
package chat

import (
	"encoding/json"
	"slices"
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
	Kind  Kind
	Model string
	// The turns of a chat, or for Generate any system turns then one user turn per prompt
	Messages    []Message
	MaxTokens   int
	Temperature *float64
	TopP        *float64
	TopK        *int
	Stop        []string
	// Choices the client asked for, zero or one meaning a single answer
	N int
	// Whether the prompt reaches the model as written, with no chat template around it
	Raw    bool
	Stream bool
	// Whether a streamed answer ends with a chunk of usage alone, as OpenAI clients ask with stream_options
	IncludeUsage bool
	// Whether the answer goes out in the OpenAI Responses shape rather than chat completions
	Responses bool
	Tools     []Tool
	// Empty, auto, none, required, or a tool name
	ToolChoice string
	// Tool endpoints the request asks the gateway to connect for it alone
	Sources []Source
	// Texts or token id lists to embed, one part each
	Inputs []Part
	// Fields of the wire request no protocol maps, carried as they are
	Extra map[string]json.RawMessage
	// The request as the client sent it, for a runtime that serves the client's endpoint itself
	Origin *Origin
}

// The client's own request, which a runtime serving the same endpoint takes with only the model renamed
type Origin struct {
	Path string
	// The body Parse read, held without copying
	Body []byte
	// Why translating into another protocol would drop something the client asked for, nil when nothing would be lost
	Untranslatable error
	// Where Body names what to run, both offsets zero when it names nothing, and the name it gave
	model [2]int
	named string
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

// Text, thinking with its signature in Data, an image as base64 or a URL, or a prompt of token ids
type Part struct {
	Type      string
	Text      string
	MediaType string
	Data      string
	URL       string
	// How closely the model looks at an image, low, high, or auto, empty leaving it to the runtime
	Detail string
	// The ids of a tokens part
	Tokens []int
	Extra  map[string]json.RawMessage
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
	// The index of the choice this answer is, zero unless the request asked for several
	Choice int
	Text   string
	// The reasoning the model wrote before its answer
	Thinking  string
	ToolCalls []ToolCall
	Stop      string
	// Tokens of the whole answer, every choice together, carried on the first choice
	Usage   Usage
	Vectors [][]float64
	// Tool calls the gateway ran itself on the way to the answer, in order
	Runs []*Run
	// The choices after this one when the request asked for several, in index order
	More  []*Result
	Extra map[string]json.RawMessage
	// The ids of the answer tokens and of the prompt, when the gateway or the runtime reports them
	TokenIDs       []int
	PromptTokenIDs []int
}

// Tokens of an answer, by round when it took more than one
type Usage struct {
	In, Out int
	// Prompt tokens the runtime served from its cache
	Cached int
	Rounds []Round
	// Speculative decoding counters when the runtime reports them
	DraftOffered, DraftAccepted int
}

// The prompt and completion tokens of one model round
type Round struct {
	In, Out int
	// The choice the round answered, zero unless the request asked for several
	Choice int
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
	// The stop reason and usage, with every choice's result when there are several
	Stopped
	// The failure that ended the answer
	Failed
)

// One piece of an answer
type Event struct {
	Kind EventKind
	Text string
	// The ids of the tokens a Said delta carries, when the gateway decoded them itself
	TokenIDs []int
	Tool     *ToolCall
	// The tool call a Called fragment belongs to, counted within its choice
	Index int
	// The choice the event belongs to, zero unless the request asked for several
	Choice  int
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

// This choice and every one after it, in index order
func (r *Result) All() []*Result {
	return append([]*Result{r}, r.More...)
}

// Joins answers from separate calls into one answer of several choices, the i-th answer becoming choice i
func Combine(answers []*Result) *Result {
	out := *answers[0]
	out.Choice, out.More, out.Usage = 0, nil, Usage{}
	for i, a := range answers {
		out.Usage.In += a.Usage.In
		out.Usage.Out += a.Usage.Out
		out.Usage.Cached += a.Usage.Cached
		out.Usage.DraftOffered += a.Usage.DraftOffered
		out.Usage.DraftAccepted += a.Usage.DraftAccepted
		for _, round := range a.Usage.RoundsOrSelf() {
			round.Choice = i
			out.Usage.Rounds = append(out.Usage.Rounds, round)
		}
		if i > 0 {
			c := *a
			c.Choice, c.More, c.Usage = i, nil, Usage{}
			out.More = append(out.More, &c)
		}
	}
	return &out
}

// The answer's rounds, the answer itself as one round when the runtime lists none
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

// Orders per choice results by index, the lowest first and the rest under it
func choices(byIndex map[int]*Result) *Result {
	if len(byIndex) == 0 {
		return &Result{}
	}
	keys := make([]int, 0, len(byIndex))
	for k := range byIndex {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	first := byIndex[keys[0]]
	for _, k := range keys[1:] {
		first.More = append(first.More, byIndex[k])
	}
	return first
}
