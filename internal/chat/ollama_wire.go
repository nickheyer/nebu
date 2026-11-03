package chat

import "encoding/json"

type olMessage struct {
	Role      string
	Content   string
	Thinking  string
	Images    []string
	ToolCalls []olToolCall
	Extra     map[string]json.RawMessage
}

func (m *olMessage) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "role":
			m.Role, err = s.str()
		case "content":
			m.Content, err = s.str()
		case "thinking":
			m.Thinking, err = s.str()
		case "images":
			m.Images, err = s.list()
		case "tool_calls":
			err = s.array(func() error {
				m.ToolCalls = append(m.ToolCalls, olToolCall{})
				return m.ToolCalls[len(m.ToolCalls)-1].read(s)
			})
		default:
			err = s.extra(&m.Extra, k)
		}
		return err
	})
}

func (m *olMessage) appendTo(b *[]byte) {
	o := begin(b)
	o.str("role", m.Role)
	o.str("content", m.Content)
	o.strIf("thinking", m.Thinking)
	o.strList("images", m.Images)
	if len(m.ToolCalls) > 0 {
		o.key("tool_calls")
		appendList(b, m.ToolCalls, (*olToolCall).appendTo)
	}
	o.close(m.Extra)
}

type olToolCall struct {
	Name      string
	Arguments json.RawMessage
}

func (t *olToolCall) read(s *scan) error {
	return s.object(func(k []byte) error {
		if string(k) != "function" {
			return s.skip()
		}
		return s.object(func(k []byte) error {
			var err error
			switch string(k) {
			case "name":
				t.Name, err = s.str()
			case "arguments":
				t.Arguments, err = s.raw()
			default:
				err = s.skip()
			}
			return err
		})
	})
}

func (t *olToolCall) appendTo(b *[]byte) {
	o := begin(b)
	o.key("function")
	fn := begin(b)
	fn.str("name", t.Name)
	fn.raw("arguments", t.Arguments)
	fn.close(nil)
	o.close(nil)
}

type olOptions struct {
	Temperature *float64
	TopP        *float64
	TopK        *int
	NumPredict  int
	Stop        []string
}

func (o *olOptions) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "temperature":
			o.Temperature, err = s.optFloat()
		case "top_p":
			o.TopP, err = s.optFloat()
		case "top_k":
			o.TopK, err = s.optInt()
		case "num_predict":
			o.NumPredict, err = s.integer()
		case "stop":
			o.Stop, err = s.strs()
		default:
			err = s.skip()
		}
		return err
	})
}

func (o *olOptions) appendTo(b *[]byte) {
	w := begin(b)
	w.float("temperature", o.Temperature)
	w.float("top_p", o.TopP)
	if o.TopK != nil {
		w.num("top_k", *o.TopK)
	}
	w.numIf("num_predict", o.NumPredict)
	w.strList("stop", o.Stop)
	w.close(nil)
}

type olRequest struct {
	Model    string
	model    [2]int
	Messages []olMessage
	Prompt   string
	System   string
	Images   []string
	Input    []Part
	Stream   *bool
	Raw      bool
	Options  *olOptions
	Tools    []oaiTool
	Extra    map[string]json.RawMessage
}

func (r *olRequest) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "model":
			at := s.i
			r.Model, err = s.str()
			r.model = [2]int{at, s.i}
		case "messages":
			err = s.array(func() error {
				r.Messages = append(r.Messages, olMessage{})
				return r.Messages[len(r.Messages)-1].read(s)
			})
		case "prompt":
			r.Prompt, err = s.str()
		case "system":
			r.System, err = s.str()
		case "images":
			r.Images, err = s.list()
		case "input":
			r.Input, err = s.prompts()
		case "stream":
			r.Stream, err = s.optBool()
		case "raw":
			r.Raw, err = s.boolean()
		case "options":
			if s.null() {
				return nil
			}
			r.Options = &olOptions{}
			err = r.Options.read(s)
		case "tools":
			err = s.array(func() error {
				r.Tools = append(r.Tools, oaiTool{})
				return r.Tools[len(r.Tools)-1].read(s)
			})
		default:
			err = s.extra(&r.Extra, k)
		}
		return err
	})
}

func (r *olRequest) appendTo(b *[]byte) {
	o := begin(b)
	o.str("model", r.Model)
	if r.Messages != nil {
		o.key("messages")
		appendList(b, r.Messages, (*olMessage).appendTo)
	}
	o.strIf("prompt", r.Prompt)
	o.strIf("system", r.System)
	o.strList("images", r.Images)
	if r.Input != nil {
		o.key("input")
		appendList(b, r.Input, func(p *Part, b *[]byte) { *b = appendStr(*b, p.Text) })
	}
	if r.Stream != nil {
		o.boolean("stream", *r.Stream)
	}
	if r.Raw {
		o.boolean("raw", true)
	}
	if r.Options != nil {
		o.key("options")
		r.Options.appendTo(b)
	}
	if len(r.Tools) > 0 {
		o.key("tools")
		appendList(b, r.Tools, (*oaiTool).appendTo)
	}
	o.close(r.Extra)
}

// A chat or generate answer, a stream line, or embeddings, as Ollama writes them
type olResponse struct {
	Model           string
	CreatedAt       string
	Message         *olMessage
	Response        *string
	Done            bool
	DoneReason      string
	PromptEvalCount int
	// The share of PromptEvalCount the prompt cache served
	PromptEvalCachedCount int
	EvalCount             int
	Embeddings            [][]float64
	Embedding             []float64
	Error                 string
	Extra                 map[string]json.RawMessage
}

func (r *olResponse) read(s *scan) error {
	return s.object(func(k []byte) error {
		var err error
		switch string(k) {
		case "model":
			r.Model, err = s.str()
		case "created_at":
			r.CreatedAt, err = s.str()
		case "message":
			r.Message = &olMessage{}
			err = r.Message.read(s)
		case "response":
			r.Response, err = s.optStr()
		case "done":
			r.Done, err = s.boolean()
		case "done_reason":
			r.DoneReason, err = s.str()
		case "prompt_eval_count":
			r.PromptEvalCount, err = s.integer()
		case "prompt_eval_cached_count":
			r.PromptEvalCachedCount, err = s.integer()
		case "eval_count":
			r.EvalCount, err = s.integer()
		case "embeddings":
			err = s.array(func() error {
				v, err := s.vector()
				r.Embeddings = append(r.Embeddings, v)
				return err
			})
		case "embedding":
			r.Embedding, err = s.vector()
		case "error":
			var message string
			message, _, err = readFailure(s)
			r.Error = message
		default:
			err = s.extra(&r.Extra, k)
		}
		return err
	})
}

func (r *olResponse) appendTo(b *[]byte) {
	o := begin(b)
	o.str("model", r.Model)
	o.str("created_at", r.CreatedAt)
	if r.Message != nil {
		o.key("message")
		r.Message.appendTo(b)
	}
	if r.Response != nil {
		o.str("response", *r.Response)
	}
	o.boolean("done", r.Done)
	o.strIf("done_reason", r.DoneReason)
	o.numIf("prompt_eval_count", r.PromptEvalCount)
	o.numIf("prompt_eval_cached_count", r.PromptEvalCachedCount)
	o.numIf("eval_count", r.EvalCount)
	o.close(r.Extra)
}

func (r *olResponse) json() []byte {
	b := make([]byte, 0, 192)
	r.appendTo(&b)
	return b
}
