package chat

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Reads requests in a client's protocol and writes answers in it
type Client interface {
	// The kind of request a path carries, None for a path the protocol does not translate
	Endpoint(path string) Kind
	Parse(path string, body []byte) (*Request, error)
	// The writer for an answer, streamed when the request asked to and whole at stop otherwise
	Answer(w http.ResponseWriter, req *Request, model string) Writer
	Error(w http.ResponseWriter, f Failure)
	// The encoded event that ends a stream the runtime answered with a failure, in the stream form of its path
	StreamFailure(at StreamAt, f Failure) ([]byte, error)
}

// Where a stream the runtime answered stands when the gateway ends it with a failure
type StreamAt struct {
	Path  string
	Model string
	// The sequence number the next event takes, for streams numbering their events
	Seq int
}

// Writes requests in a runtime's protocol and reads its answers
type Runtime interface {
	// The endpoint path and body of a request translated into the runtime's protocol
	Render(req *Request) (string, []byte, error)
	// The client's own path and body with the model renamed, for a runtime that serves the client's endpoint itself
	Native(req *Request) (string, []byte, error)
	ParseResult(req *Request, body []byte) (*Result, error)
	ParseStream(r io.Reader, emit func(Event) error) error
	// The message of an error body, empty when the body names none
	ErrorMessage(body []byte) string
	Traits() Traits
}

// What a runtime protocol needs of a request and gives in an answer
type Traits struct {
	// Image URLs must be fetched into bytes before rendering
	InlineImages bool
	// Token counts cover images
	CountsImages bool
	// Fields the protocol does not define are refused, so unmapped fields stay out
	Strict bool
	// Serves /v1/responses itself, so such a request passes through Native with its stored state
	Responses bool
	// Answers several choices in one call, so a request with n above one needs no call per choice
	Choices bool
	// The path a Count request renders to, empty when the runtime has no tokenizer endpoint
	TokenizePath string
}

// Writes an answer event by event, whole at stop or as a stream
type Writer interface {
	Write(Event) error
	Close() error
}

// A protocol in both roles
type protocol interface {
	Client
	Runtime
}

var protocols = map[v1.ApiFlavor]protocol{
	v1.ApiFlavor_API_FLAVOR_OPENAI:    openai{},
	v1.ApiFlavor_API_FLAVOR_ANTHROPIC: anthropic{},
	v1.ApiFlavor_API_FLAVOR_OLLAMA:    ollama{},
	v1.ApiFlavor_API_FLAVOR_SDCPP:     sdcpp{},
}

// The protocol of an api, OpenAI for one not known
func of(api v1.ApiFlavor) protocol {
	if p, ok := protocols[api]; ok {
		return p
	}
	return protocols[v1.ApiFlavor_API_FLAVOR_OPENAI]
}

// The client side of an api's protocol
func ClientOf(api v1.ApiFlavor) Client { return of(api) }

// The runtime side of an api's protocol
func RuntimeOf(api v1.ApiFlavor) Runtime { return of(api) }

// The protocol a request speaks, by its path, the Anthropic version header deciding paths the protocols share
func Detect(r *http.Request) v1.ApiFlavor {
	switch {
	case strings.HasPrefix(r.URL.Path, sdcppPrefix):
		return v1.ApiFlavor_API_FLAVOR_SDCPP
	case strings.HasPrefix(r.URL.Path, ollamaPrefix):
		return v1.ApiFlavor_API_FLAVOR_OLLAMA
	case strings.HasPrefix(r.URL.Path, anthropicMessages):
		return v1.ApiFlavor_API_FLAVOR_ANTHROPIC
	case openai{}.Endpoint(r.URL.Path) != None:
		return v1.ApiFlavor_API_FLAVOR_OPENAI
	case r.Header.Get("anthropic-version") != "":
		return v1.ApiFlavor_API_FLAVOR_ANTHROPIC
	}
	return v1.ApiFlavor_API_FLAVOR_OPENAI
}

// Removes every unmapped field at every level, for a runtime that refuses them
func DropExtras(req *Request) {
	req.Extra = nil
	for i := range req.Messages {
		req.Messages[i].Extra = nil
		for j := range req.Messages[i].Parts {
			req.Messages[i].Parts[j].Extra = nil
		}
	}
	for i := range req.Tools {
		req.Tools[i].Extra = nil
	}
}

// The client's path and body for a protocol serving that path, the model renamed and every other byte kept
func native(p protocol, req *Request) (string, []byte, error) {
	o := req.Origin
	switch {
	case o == nil:
		return "", nil, bad("the request has no body of the client's own to pass through, so it needs translating")
	case p.Endpoint(o.Path) == None, o.Path == OpenAIResponses && !p.Traits().Responses:
		return "", nil, bad("the runtime does not serve %s itself, so the request needs translating", o.Path)
	}
	return o.Path, o.withModel(req.Model), nil
}

// Refuses several choices to a runtime answering one a call, which takes a call per choice instead
func choicesFit(rt Runtime, c *Request, protocol string) error {
	if c.N > 1 && !rt.Traits().Choices {
		return bad("the %s protocol answers one choice a call, so n=%d takes a call per choice", protocol, c.N)
	}
	return nil
}

// The body with the model's value replaced, the body itself when the model is unchanged
func (o *Origin) withModel(model string) []byte {
	if model == o.named {
		return o.Body
	}
	at := o.model
	if at != [2]int{} {
		out := make([]byte, 0, len(o.Body)+len(model)+2)
		out = append(out, o.Body[:at[0]]...)
		out = appendStr(out, model)
		return append(out, o.Body[at[1]:]...)
	}
	s := &scan{b: o.Body}
	s.peek()
	brace := s.i + 1
	s.i = brace
	out := make([]byte, 0, len(o.Body)+len(model)+12)
	out = append(out, o.Body[:brace]...)
	out = append(out, `"model":`...)
	out = appendStr(out, model)
	if s.peek() != '}' {
		out = append(out, ',')
	}
	return append(out, o.Body[brace:]...)
}

// A client request's origin, the model's offsets and name as its reader found them
func originOf(path string, body []byte, at [2]int, model string) *Origin {
	return &Origin{Path: path, Body: body, model: at, named: model}
}

// Refuses translating a request whose client asked for what only its own protocol can do
func translatable(req *Request) error {
	if req.Origin != nil && req.Origin.Untranslatable != nil {
		return req.Origin.Untranslatable
	}
	return nil
}

// Refuses a writer an event for a choice past the first, in a protocol whose answers have one
func oneChoice(ev Event, protocol string) error {
	if ev.Choice != 0 {
		return fmt.Errorf("the %s protocol answers with one choice, and the answer carried choice %d", protocol, ev.Choice)
	}
	return nil
}
