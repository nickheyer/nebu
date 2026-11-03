package chat

import (
	"bytes"
	"encoding/json"
)

// Block content as a bare string, a list of blocks, or one object, or raw JSON when written already encoded
type antContent struct {
	Text   string
	Blocks []antBlock
	Object *antBlock
	Raw    json.RawMessage
	// How the content was written, a string, a list, an object, or raw, zero when absent or null
	kind byte
}

const (
	contentText   = 's'
	contentList   = 'a'
	contentObject = 'o'
	contentRaw    = 'r'
)

func blocksContent(blocks []antBlock) antContent {
	return antContent{Blocks: blocks, kind: contentList}
}

func rawContent(raw json.RawMessage) antContent {
	return antContent{Raw: raw, kind: contentRaw}
}

func (c *antContent) read(s *scan) error {
	var err error
	switch s.peek() {
	case 'n':
		s.null()
		*c = antContent{}
	case '"':
		c.kind = contentText
		c.Text, err = s.str()
	case '[':
		c.kind = contentList
		err = s.array(func() error {
			c.Blocks = append(c.Blocks, antBlock{})
			return c.Blocks[len(c.Blocks)-1].read(s)
		})
	case '{':
		c.kind, c.Object = contentObject, &antBlock{}
		err = c.Object.read(s)
	default:
		err = s.fail("content must be a string, a list of blocks, or an object")
	}
	return err
}

func (c *antContent) appendTo(b *[]byte) {
	switch c.kind {
	case contentText:
		*b = appendStr(*b, c.Text)
	case contentList:
		appendList(b, c.Blocks, (*antBlock).appendTo)
	case contentObject:
		c.Object.appendTo(b)
	case contentRaw:
		*b = append(*b, c.Raw...)
	default:
		*b = append(*b, "null"...)
	}
}

// The content as a list, a bare string being one text block, an object refused
func (c *antContent) blocks() ([]antBlock, error) {
	switch c.kind {
	case contentText:
		if c.Text == "" {
			return nil, nil
		}
		return []antBlock{{Type: "text", Text: c.Text}}, nil
	case contentObject:
		return nil, bad("content must be a string or a list of blocks")
	}
	return c.Blocks, nil
}

// A content block, any of the kinds the API reads or writes, its server tool result fields included
type antBlock struct {
	Type      string
	Text      string
	ID        string
	Name      string
	Input     json.RawMessage
	ToolUseID string
	Content   antContent
	Source    *antSource
	Thinking  string
	Signature string
	// The server of an mcp_tool_use and the outcome of an mcp_tool_result
	ServerName string
	IsError    *bool
	// Fields of documents, search results, fetched pages, and server tool errors
	Title            string
	URL              string
	EncryptedContent string
	PageAge          *string
	ErrorCode        string
	RetrievedAt      string
	Extra            map[string]json.RawMessage
}

func (b *antBlock) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			b.Type, err = s.str()
		case "text":
			b.Text, err = s.str()
		case "id":
			b.ID, err = s.str()
		case "name":
			b.Name, err = s.str()
		case "input":
			b.Input, err = s.raw()
		case "tool_use_id":
			b.ToolUseID, err = s.str()
		case "content":
			err = b.Content.read(s)
		case "source":
			if s.null() {
				return nil
			}
			b.Source = &antSource{}
			err = b.Source.read(s)
		case "thinking":
			b.Thinking, err = s.str()
		case "signature":
			b.Signature, err = s.str()
		case "server_name":
			b.ServerName, err = s.str()
		case "is_error":
			b.IsError, err = s.optBool()
		case "title":
			b.Title, err = s.str()
		case "url":
			b.URL, err = s.str()
		case "encrypted_content":
			b.EncryptedContent, err = s.str()
		case "page_age":
			b.PageAge, err = s.optStr()
		case "error_code":
			b.ErrorCode, err = s.str()
		case "retrieved_at":
			b.RetrievedAt, err = s.str()
		default:
			err = s.extra(&b.Extra, k)
		}
		return err
	})
}

func (b *antBlock) appendTo(out *[]byte) {
	o := begin(out)
	o.str("type", b.Type)
	o.strIf("text", b.Text)
	o.strIf("id", b.ID)
	o.strIf("name", b.Name)
	o.raw("input", b.Input)
	o.strIf("tool_use_id", b.ToolUseID)
	if b.Content.kind != 0 {
		o.key("content")
		b.Content.appendTo(out)
	}
	if b.Source != nil {
		o.key("source")
		b.Source.appendTo(out)
	}
	o.strIf("thinking", b.Thinking)
	o.strIf("signature", b.Signature)
	o.strIf("server_name", b.ServerName)
	if b.IsError != nil {
		o.boolean("is_error", *b.IsError)
	}
	o.strIf("title", b.Title)
	o.strIf("url", b.URL)
	o.strIf("encrypted_content", b.EncryptedContent)
	if b.PageAge != nil {
		o.str("page_age", *b.PageAge)
	}
	o.strIf("error_code", b.ErrorCode)
	o.strIf("retrieved_at", b.RetrievedAt)
	o.close(b.Extra)
}

func (b antBlock) MarshalJSON() ([]byte, error) {
	var out []byte
	b.appendTo(&out)
	return out, nil
}

type antSource struct {
	Type      string
	MediaType string
	Data      string
	URL       string
	// The blocks of a document whose source is content
	Content antContent
}

func (src *antSource) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			src.Type, err = s.str()
		case "media_type":
			src.MediaType, err = s.str()
		case "data":
			src.Data, err = s.str()
		case "url":
			src.URL, err = s.str()
		case "content":
			err = src.Content.read(s)
		default:
			err = s.skip()
		}
		return err
	})
}

func (src *antSource) appendTo(b *[]byte) {
	o := begin(b)
	o.str("type", src.Type)
	o.strIf("media_type", src.MediaType)
	o.strIf("data", src.Data)
	o.strIf("url", src.URL)
	if src.Content.kind != 0 {
		o.key("content")
		src.Content.appendTo(b)
	}
	o.close(nil)
}

type antMessage struct {
	Role    string
	Content antContent
	Extra   map[string]json.RawMessage
}

func (m *antMessage) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "role":
			m.Role, err = s.str()
		case "content":
			err = m.Content.read(s)
		default:
			err = s.extra(&m.Extra, k)
		}
		return err
	})
}

func (m *antMessage) appendTo(b *[]byte) {
	o := begin(b)
	o.str("role", m.Role)
	o.key("content")
	m.Content.appendTo(b)
	o.close(m.Extra)
}

type antRequest struct {
	Model         string
	model         [2]int
	System        antContent
	Messages      []antMessage
	MaxTokens     int
	Temperature   *float64
	TopP          *float64
	TopK          *int
	StopSequences []string
	Stream        bool
	Tools         []antTool
	ToolChoice    *antToolChoice
	MCPServers    []antMCPServer
	Extra         map[string]json.RawMessage
}

func (r *antRequest) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "model":
			at := s.i
			r.Model, err = s.str()
			r.model = [2]int{at, s.i}
		case "system":
			err = r.System.read(s)
		case "messages":
			err = s.array(func() error {
				r.Messages = append(r.Messages, antMessage{})
				return r.Messages[len(r.Messages)-1].read(s)
			})
		case "max_tokens":
			r.MaxTokens, err = s.integer()
		case "temperature":
			r.Temperature, err = s.optFloat()
		case "top_p":
			r.TopP, err = s.optFloat()
		case "top_k":
			r.TopK, err = s.optInt()
		case "stop_sequences":
			r.StopSequences, err = s.list()
		case "stream":
			r.Stream, err = s.boolean()
		case "tools":
			err = s.array(func() error {
				r.Tools = append(r.Tools, antTool{})
				return r.Tools[len(r.Tools)-1].read(s)
			})
		case "tool_choice":
			if s.null() {
				return nil
			}
			r.ToolChoice = &antToolChoice{}
			err = r.ToolChoice.read(s)
		case "mcp_servers":
			err = s.array(func() error {
				r.MCPServers = append(r.MCPServers, antMCPServer{})
				return r.MCPServers[len(r.MCPServers)-1].read(s)
			})
		default:
			err = s.extra(&r.Extra, k)
		}
		return err
	})
}

func (r *antRequest) appendTo(b *[]byte) {
	o := begin(b)
	o.str("model", r.Model)
	if r.System.kind != 0 {
		o.key("system")
		r.System.appendTo(b)
	}
	o.key("messages")
	appendList(b, r.Messages, (*antMessage).appendTo)
	o.numIf("max_tokens", r.MaxTokens)
	o.float("temperature", r.Temperature)
	o.float("top_p", r.TopP)
	if r.TopK != nil {
		o.num("top_k", *r.TopK)
	}
	o.strList("stop_sequences", r.StopSequences)
	if r.Stream {
		o.boolean("stream", true)
	}
	if len(r.Tools) > 0 {
		o.key("tools")
		appendList(b, r.Tools, (*antTool).appendTo)
	}
	if r.ToolChoice != nil {
		o.key("tool_choice")
		tc := begin(b)
		tc.str("type", r.ToolChoice.Type)
		tc.strIf("name", r.ToolChoice.Name)
		tc.close(nil)
	}
	o.close(r.Extra)
}

// An MCP endpoint as a request names it
type antMCPServer struct {
	Type          string
	URL           string
	Name          string
	Authorization string
	// Whether the configuration turns the server off, and the tools it allows
	Disabled bool
	Allowed  []string
}

func (m *antMCPServer) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			m.Type, err = s.str()
		case "url":
			m.URL, err = s.str()
		case "name":
			m.Name, err = s.str()
		case "authorization_token":
			m.Authorization, err = s.str()
		case "tool_configuration":
			err = s.object(func(k []byte) error {
				var err error
				switch string(k) {
				case "enabled":
					var on *bool
					on, err = s.optBool()
					m.Disabled = on != nil && !*on
				case "allowed_tools":
					m.Allowed, err = s.list()
				default:
					err = s.skip()
				}
				return err
			})
		default:
			err = s.skip()
		}
		return err
	})
}

// The limits a server tool definition sets, read in the same pass that keeps them raw
type antLimits struct {
	MaxUses   *int
	Allowed   []string
	Blocked   []string
	Country   string
	MaxTokens *int
}

type antTool struct {
	Type        string
	Name        string
	Description string
	InputSchema json.RawMessage
	Strict      *bool
	limits      antLimits
	Extra       map[string]json.RawMessage
}

func (t *antTool) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		start := s.i
		switch string(k) {
		case "type":
			t.Type, err = s.str()
		case "name":
			t.Name, err = s.str()
		case "description":
			t.Description, err = s.str()
		case "input_schema":
			t.InputSchema, err = s.raw()
		case "strict":
			t.Strict, err = s.optBool()
		case "defer_loading":
			err = s.skip()
		case "max_uses", "allowed_domains", "blocked_domains", "user_location", "max_content_tokens":
			if err = t.limit(s, k); err == nil {
				if t.Extra == nil {
					t.Extra = make(map[string]json.RawMessage, 4)
				}
				t.Extra[string(k)] = bytes.Clone(s.b[start:s.i])
			}
		default:
			err = s.extra(&t.Extra, k)
		}
		return err
	})
}

// Reads one of a server tool's caps on uses, domains, location, or page size
func (t *antTool) limit(s *scan, k []byte) error {
	var err error
	switch string(k) {
	case "max_uses":
		t.limits.MaxUses, err = s.optInt()
	case "allowed_domains":
		t.limits.Allowed, err = s.list()
	case "blocked_domains":
		t.limits.Blocked, err = s.list()
	case "user_location":
		err = s.object(func(k []byte) error {
			if string(k) != "country" {
				return s.skip()
			}
			var err error
			t.limits.Country, err = s.str()
			return err
		})
	case "max_content_tokens":
		t.limits.MaxTokens, err = s.optInt()
	}
	return err
}

func (t *antTool) appendTo(b *[]byte) {
	o := begin(b)
	o.strIf("type", t.Type)
	o.str("name", t.Name)
	o.strIf("description", t.Description)
	o.raw("input_schema", t.InputSchema)
	if t.Strict != nil {
		o.boolean("strict", *t.Strict)
	}
	o.close(t.Extra)
}

type antToolChoice struct {
	Type string
	Name string
}

func (c *antToolChoice) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			c.Type, err = s.str()
		case "name":
			c.Name, err = s.str()
		default:
			err = s.skip()
		}
		return err
	})
}

type antUsage struct {
	InputTokens              int               `json:"input_tokens"`
	OutputTokens             int               `json:"output_tokens"`
	CacheCreationInputTokens int               `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int               `json:"cache_read_input_tokens"`
	ServerToolUse            *antServerToolUse `json:"server_tool_use,omitempty"`
	// One entry per model round when the answer took more than one
	Iterations []antIteration `json:"iterations,omitempty"`
}

func (u *antUsage) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "input_tokens":
			u.InputTokens, err = s.integer()
		case "output_tokens":
			u.OutputTokens, err = s.integer()
		case "cache_creation_input_tokens":
			u.CacheCreationInputTokens, err = s.integer()
		case "cache_read_input_tokens":
			u.CacheReadInputTokens, err = s.integer()
		case "iterations":
			err = s.array(func() error {
				var it antIteration
				err := s.object(func(k []byte) error {
					var err error
					switch string(k) {
					case "type":
						it.Type, err = s.str()
					case "input_tokens":
						it.InputTokens, err = s.integer()
					case "output_tokens":
						it.OutputTokens, err = s.integer()
					case "cache_creation_input_tokens":
						it.CacheCreationInputTokens, err = s.integer()
					case "cache_read_input_tokens":
						it.CacheReadInputTokens, err = s.integer()
					default:
						err = s.skip()
					}
					return err
				})
				u.Iterations = append(u.Iterations, it)
				return err
			})
		default:
			err = s.skip()
		}
		return err
	})
}

// A message read from a runtime
type antResponse struct {
	ID         string
	Model      string
	Content    []antBlock
	StopReason *string
	Usage      antUsage
	Extra      map[string]json.RawMessage
}

func (r *antResponse) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "id":
			r.ID, err = s.str()
		case "model":
			r.Model, err = s.str()
		case "type", "role", "stop_sequence":
			err = s.skip()
		case "content":
			err = s.array(func() error {
				r.Content = append(r.Content, antBlock{})
				return r.Content[len(r.Content)-1].read(s)
			})
		case "stop_reason":
			r.StopReason, err = s.optStr()
		case "usage":
			err = r.Usage.read(s)
		default:
			err = s.extra(&r.Extra, k)
		}
		return err
	})
}

// The delta of a message_delta or content_block_delta event
type antDelta struct {
	Type        string
	Text        string
	Thinking    string
	PartialJSON string
	StopReason  *string
	Extra       map[string]json.RawMessage
}

func (d *antDelta) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			d.Type, err = s.str()
		case "text":
			d.Text, err = s.str()
		case "thinking":
			d.Thinking, err = s.str()
		case "partial_json":
			d.PartialJSON, err = s.str()
		case "stop_reason":
			d.StopReason, err = s.optStr()
		case "stop_sequence", "signature":
			err = s.skip()
		default:
			err = s.extra(&d.Extra, k)
		}
		return err
	})
}

// One event of a Messages stream
type antEvent struct {
	Type         string
	Index        int
	Message      *antResponse
	ContentBlock *antBlock
	Delta        *antDelta
	Usage        *antUsage
	ErrorMessage string
}

func (e *antEvent) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "type":
			e.Type, err = s.str()
		case "index":
			e.Index, err = s.integer()
		case "message":
			e.Message = &antResponse{}
			err = e.Message.read(s)
		case "content_block":
			e.ContentBlock = &antBlock{}
			err = e.ContentBlock.read(s)
		case "delta":
			e.Delta = &antDelta{}
			err = e.Delta.read(s)
		case "usage":
			e.Usage = &antUsage{}
			err = e.Usage.read(s)
		case "error":
			err = s.object(func(k []byte) error {
				if string(k) != "message" {
					return s.skip()
				}
				var err error
				e.ErrorMessage, err = s.str()
				return err
			})
		default:
			err = s.skip()
		}
		return err
	})
}

// A message written to a client
type antReply struct {
	ID         string
	Model      string
	Content    []any
	StopReason *string
	Usage      antUsage
	Extra      map[string]json.RawMessage
}

func (r antReply) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, 256)
	o := begin(&b)
	o.str("id", r.ID)
	o.str("type", "message")
	o.str("role", "assistant")
	o.str("model", r.Model)
	if r.Content == nil {
		r.Content = []any{}
	}
	if err := o.any("content", r.Content); err != nil {
		return nil, err
	}
	o.key("stop_reason")
	if r.StopReason == nil {
		b = append(b, "null"...)
	} else {
		b = appendStr(b, *r.StopReason)
	}
	o.raw("stop_sequence", []byte("null"))
	if err := o.any("usage", r.Usage); err != nil {
		return nil, err
	}
	o.close(r.Extra)
	return b, nil
}
