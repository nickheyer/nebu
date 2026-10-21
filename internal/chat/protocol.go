package chat

import (
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
}

// Writes requests in a runtime's protocol and reads its answers
type Runtime interface {
	// The endpoint path and body of a request
	Render(req *Request) (string, []byte, error)
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
