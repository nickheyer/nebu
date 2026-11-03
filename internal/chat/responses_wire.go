package chat

import (
	"encoding/json"
	"strings"
)

// A Responses API request as the client wrote it
type respRequest struct {
	Model string
	model [2]int
	// The input as items or a bare string, and whether the request had any
	Input     []respItem
	InputText *string
	HasInput  bool
	// Lead the turns as a system turn
	Instructions    string
	MaxOutputTokens int
	Temperature     *float64
	TopP            *float64
	Stream          bool
	Tools           []respTool
	ToolChoice      *toolChoiceWire
	// State the client asks a server to keep or recall
	PreviousResponseID string
	Conversation       json.RawMessage
	Prompt             json.RawMessage
	Background         bool
	Store              *bool
	Effort             string
	// Output asked for, whether input past the context is dropped to fit, and the cap on built in tool calls
	Include      []string
	Truncation   string
	MaxToolCalls *int
	Format       *respFormat
	Verbosity    string
	Extra        map[string]json.RawMessage
}

// The text format a response is asked to take
type respFormat struct {
	Type, Name string
	Schema     json.RawMessage
	Strict     *bool
}

func (r *respRequest) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "model":
			at := s.i
			r.Model, err = s.str()
			r.model = [2]int{at, s.i}
		case "input":
			switch r.HasInput = s.peek() != 'n'; s.peek() {
			case '"':
				r.InputText, err = s.optStr()
				return err
			case '[', 'n':
			default:
				return s.fail("input must be a string or a list of items")
			}
			err = s.array(func() error {
				r.Input = append(r.Input, respItem{})
				return r.Input[len(r.Input)-1].read(s)
			})
		case "instructions":
			r.Instructions, err = s.str()
		case "max_output_tokens":
			r.MaxOutputTokens, err = s.integer()
		case "temperature":
			r.Temperature, err = s.optFloat()
		case "top_p":
			r.TopP, err = s.optFloat()
		case "stream":
			r.Stream, err = s.boolean()
		case "tools":
			err = s.array(func() error {
				r.Tools = append(r.Tools, respTool{})
				return r.Tools[len(r.Tools)-1].read(s)
			})
		case "tool_choice":
			if s.null() {
				return nil
			}
			var tc toolChoiceWire
			tc, err = readToolChoice(s)
			r.ToolChoice = &tc
		case "previous_response_id":
			r.PreviousResponseID, err = s.str()
		case "conversation":
			r.Conversation, err = s.raw()
		case "prompt":
			r.Prompt, err = s.raw()
		case "background":
			r.Background, err = s.boolean()
		case "store":
			r.Store, err = s.optBool()
		case "reasoning":
			err = s.object(func(k []byte) error {
				if string(k) != "effort" {
					return s.skip()
				}
				var err error
				r.Effort, err = s.str()
				return err
			})
		case "text":
			err = s.object(func(k []byte) error {
				var err error
				switch string(k) {
				case "format":
					r.Format, err = readFormat(s)
				case "verbosity":
					r.Verbosity, err = s.str()
				default:
					err = s.skip()
				}
				return err
			})
		case "truncation":
			r.Truncation, err = s.str()
		case "max_tool_calls":
			r.MaxToolCalls, err = s.optInt()
		case "include":
			r.Include, err = s.list()
		default:
			err = s.extra(&r.Extra, k)
		}
		return err
	})
}

func readFormat(s *scan) (*respFormat, error) {
	if s.null() {
		return nil, nil
	}
	f := &respFormat{}
	err := s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			f.Type, err = s.str()
		case "name":
			f.Name, err = s.str()
		case "schema":
			f.Schema, err = s.raw()
		case "strict":
			f.Strict, err = s.optBool()
		default:
			err = s.skip()
		}
		return err
	})
	return f, err
}

// Whether raw JSON holds a value other than null
func present(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

// A tool as the Responses API lists it, functions flat rather than under a function key
type respTool struct {
	Type        string
	Name        string
	Description string
	Parameters  json.RawMessage
	Strict      *bool
	ServerLabel string
	ServerURL   string
	Auth        string
	Headers     map[string]string
	// The tool names an mcp tool may offer, from a list or an object's tool_names
	Allowed []string
	Domains []string
	Country string
	Extra   map[string]json.RawMessage
}

func (t *respTool) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			t.Type, err = s.str()
		case "name":
			t.Name, err = s.str()
		case "description":
			t.Description, err = s.str()
		case "parameters":
			t.Parameters, err = s.raw()
		case "strict":
			t.Strict, err = s.optBool()
		case "server_label":
			t.ServerLabel, err = s.str()
		case "server_url":
			t.ServerURL, err = s.str()
		case "authorization":
			t.Auth, err = s.str()
		case "headers":
			err = s.object(func(k []byte) error {
				v, err := s.str()
				if t.Headers == nil {
					t.Headers = map[string]string{}
				}
				t.Headers[string(k)] = v
				return err
			})
		case "allowed_tools":
			switch s.peek() {
			case '[', 'n':
				t.Allowed, err = s.list()
			case '{':
				err = s.object(func(k []byte) error {
					if string(k) != "tool_names" {
						return s.skip()
					}
					var err error
					t.Allowed, err = s.list()
					return err
				})
			default:
				err = s.fail("allowed_tools must be a list of names or an object with tool_names")
			}
		case "filters":
			err = s.object(func(k []byte) error {
				if string(k) != "allowed_domains" {
					return s.skip()
				}
				var err error
				t.Domains, err = s.list()
				return err
			})
		case "user_location":
			err = s.object(func(k []byte) error {
				if string(k) != "country" {
					return s.skip()
				}
				var err error
				t.Country, err = s.str()
				return err
			})
		default:
			err = s.extra(&t.Extra, k)
		}
		return err
	})
}

// One item of the input list
type respItem struct {
	Type      string
	Role      string
	Content   *respContent
	CallID    string
	Name      string
	Arguments string
	Output    *respContent
	Summary   []string
	Extra     map[string]json.RawMessage
}

// Content or output, a bare string or a list of parts
type respContent struct {
	Text  string
	Parts []respPart
	List  bool
}

func (i *respItem) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			i.Type, err = s.str()
		case "role":
			i.Role, err = s.str()
		case "content":
			i.Content, err = readRespContent(s)
		case "call_id":
			i.CallID, err = s.str()
		case "name":
			i.Name, err = s.str()
		case "arguments":
			i.Arguments, err = readArgs(s)
		case "output":
			i.Output, err = readRespContent(s)
		case "summary":
			err = s.array(func() error {
				return s.object(func(k []byte) error {
					if string(k) != "text" {
						return s.skip()
					}
					text, err := s.str()
					i.Summary = append(i.Summary, text)
					return err
				})
			})
		case "id", "status", "encrypted_content":
			err = s.skip()
		default:
			err = s.extra(&i.Extra, k)
		}
		return err
	})
}

func readRespContent(s *scan) (*respContent, error) {
	switch s.peek() {
	case 'n':
		s.null()
		return nil, nil
	case '"':
		t, err := s.str()
		return &respContent{Text: t}, err
	case '[':
		c := &respContent{List: true}
		err := s.array(func() error {
			c.Parts = append(c.Parts, respPart{})
			return c.Parts[len(c.Parts)-1].read(s)
		})
		return c, err
	}
	return nil, s.fail("content must be a string or a list of parts")
}

// One part of a message's content
type respPart struct {
	Type     string
	Text     string
	ImageURL string
	FileID   string
	Refusal  string
	Detail   string
	Extra    map[string]json.RawMessage
}

func (p *respPart) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			p.Type, err = s.str()
		case "text":
			p.Text, err = s.str()
		case "image_url":
			p.ImageURL, err = s.str()
		case "file_id":
			p.FileID, err = s.str()
		case "refusal":
			p.Refusal, err = s.str()
		case "detail":
			p.Detail, err = s.str()
		case "annotations", "logprobs":
			err = s.skip()
		default:
			err = s.extra(&p.Extra, k)
		}
		return err
	})
}

// A response object as a runtime serving the Responses API writes it
type respAnswer struct {
	ID, Model, Status string
	Output            []respOutItem
	Usage             *oaiUsage
	// Why the response stopped short, and the message of the error that failed it
	Incomplete string
	Error      string
}

func (r *respAnswer) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "id":
			r.ID, err = s.str()
		case "model":
			r.Model, err = s.str()
		case "usage":
			r.Usage = &oaiUsage{}
			err = r.Usage.read(s)
		case "error":
			r.Error, _, err = readFailure(s)
		default:
			err = r.member(s, k)
		}
		return err
	})
}

// Reads the members a response object has and a chat completion lacks, skipping the rest
func (r *respAnswer) member(s *scan, k []byte) error {
	var err error
	switch string(k) {
	case "status":
		r.Status, err = s.str()
	case "output":
		err = s.array(func() error {
			r.Output = append(r.Output, respOutItem{})
			return r.Output[len(r.Output)-1].read(s)
		})
	case "incomplete_details":
		err = s.object(func(k []byte) error {
			if string(k) != "reason" {
				return s.skip()
			}
			var err error
			r.Incomplete, err = s.str()
			return err
		})
	default:
		err = s.skip()
	}
	return err
}

// The stop a response's status and incomplete reason make
func respStop(status, reason string) string {
	if status != "incomplete" {
		return StopEnd
	}
	switch reason {
	case "max_output_tokens":
		return StopLength
	case "content_filter":
		return StopFilter
	}
	return StopEnd
}

// The answer a whole response object gives, its messages as text, reasoning as thinking, and function calls
func (r *respAnswer) result() *Result {
	res := &Result{ID: r.ID, Model: r.Model}
	if r.Usage != nil {
		res.Usage = Usage{In: r.Usage.In, Out: r.Usage.Out, Cached: r.Usage.Cached}
	}
	var text, thinking strings.Builder
	for _, it := range r.Output {
		switch it.Type {
		case "message":
			text.WriteString(it.Text)
		case "reasoning":
			if thinking.Len() > 0 {
				thinking.WriteString("\n\n")
			}
			thinking.WriteString(it.reasoning())
		case "function_call":
			args := it.Arguments
			if args == "" {
				args = "{}"
			}
			res.ToolCalls = append(res.ToolCalls, ToolCall{ID: it.CallID, Name: it.Name, Args: args})
		}
	}
	res.Text, res.Thinking = text.String(), thinking.String()
	res.Stop = StopOf(respStop(r.Status, r.Incomplete), len(res.ToolCalls))
	return res
}

// An output item of a response
type respOutItem struct {
	Type, ID, CallID, Name, Arguments string
	// The text of a message's parts or a reasoning item's content, and a reasoning item's summary
	Text    string
	Summary []string
}

func (it *respOutItem) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			it.Type, err = s.str()
		case "id":
			it.ID, err = s.str()
		case "call_id":
			it.CallID, err = s.str()
		case "name":
			it.Name, err = s.str()
		case "arguments":
			it.Arguments, err = readArgs(s)
		case "content":
			var text strings.Builder
			err = s.array(func() error {
				return s.object(func(k []byte) error {
					if string(k) != "text" && string(k) != "refusal" {
						return s.skip()
					}
					t, err := s.str()
					text.WriteString(t)
					return err
				})
			})
			it.Text = text.String()
		case "summary":
			err = s.array(func() error {
				return s.object(func(k []byte) error {
					if string(k) != "text" {
						return s.skip()
					}
					t, err := s.str()
					it.Summary = append(it.Summary, t)
					return err
				})
			})
		default:
			err = s.skip()
		}
		return err
	})
}

// The thought an item shows, its summary or its full content without one
func (it *respOutItem) reasoning() string {
	if len(it.Summary) > 0 {
		return strings.Join(it.Summary, "\n\n")
	}
	return it.Text
}
