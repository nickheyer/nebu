package gateway

import (
	"bytes"
	"encoding/base64"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A trace's request as turns and tools, nil when its path carries none or its protocol refuses the body
func Conversation(t *v1.Trace) *v1.TraceConversation {
	client := chat.ClientOf(t.GetClientApi())
	switch client.Endpoint(t.GetPath()) {
	case chat.Chat, chat.Generate, chat.Embed, chat.Count:
	default:
		return nil
	}
	var req *chat.Request
	parse := func(body []byte) bool {
		r, err := client.Parse(t.GetPath(), body)
		if err == nil {
			req = r
		}
		return err == nil
	}
	if body := []byte(t.GetRequest()); t.GetRequestBytes() > uint64(len(body)) {
		mend(body, parse)
	} else {
		parse(body)
	}
	if req == nil {
		return nil
	}
	out := &v1.TraceConversation{}
	for _, m := range req.Messages {
		turn := &v1.TraceTurn{Role: m.Role, ToolCallId: m.ToolID, Parts: traceParts(m.Parts)}
		for _, c := range m.ToolCalls {
			turn.Calls = append(turn.Calls, &v1.TraceCall{Id: c.ID, Name: c.Name, Arguments: c.Args})
		}
		out.Turns = append(out.Turns, turn)
	}
	for _, in := range req.Inputs {
		out.Turns = append(out.Turns, &v1.TraceTurn{Role: "user", Parts: traceParts([]chat.Part{in})})
	}
	for _, tool := range req.Tools {
		out.Tools = append(out.Tools, &v1.TraceTool{Name: tool.Name, Description: tool.Description, Schema: string(tool.Schema), Type: tool.Type})
	}
	return out
}

// Parts as a trace shows them, an inline image by its size and a token prompt by its count
func traceParts(parts []chat.Part) []*v1.TracePart {
	out := make([]*v1.TracePart, 0, len(parts))
	for _, p := range parts {
		part := &v1.TracePart{Type: p.Type, Text: p.Text, MediaType: p.MediaType, Url: p.URL}
		switch p.Type {
		case "image":
			part.Size = uint64(base64.StdEncoding.DecodedLen(len(p.Data)))
		case "tokens":
			part.Size = uint64(len(p.Tokens))
		}
		out = append(out, part)
	}
	return out
}

// Offers a body the trace cap cut short closed into JSON, least lost first, until take accepts one
func mend(b []byte, take func([]byte) bool) {
	var stack []byte
	// Where cutting the body removes each open container whole, its key with it when it is a member's value
	var drops []int
	// Where the body last held only whole values, and how deep it stood there
	safe, depth := 0, 0
	key, keyAt := false, 0
	cut := func(partial []byte) {
		if partial != nil && take(closed(partial, stack)) {
			return
		}
		if take(closed(b[:safe], stack[:depth])) {
			return
		}
		for k := len(stack) - 1; k > 0; k-- {
			if take(closed(bytes.TrimRight(bytes.TrimSuffix(bytes.TrimRight(b[:drops[k]], " \t\r\n"), []byte(",")), " \t\r\n"), stack[:k])) {
				return
			}
		}
	}
	for i := 0; i < len(b); {
		switch c := b[i]; c {
		case ' ', '\t', '\n', '\r':
			i++
		case '{', '[':
			at := i
			if len(stack) > 0 && stack[len(stack)-1] == '{' {
				at = keyAt
			}
			stack, drops = append(stack, c), append(drops, at)
			key = c == '{'
			i++
			safe, depth = i, len(stack)
		case '}', ']':
			if len(stack) == 0 {
				cut(nil)
				return
			}
			stack, drops = stack[:len(stack)-1], drops[:len(drops)-1]
			key = false
			i++
			safe, depth = i, len(stack)
		case ':':
			key = false
			i++
		case ',':
			key = len(stack) > 0 && stack[len(stack)-1] == '{'
			i++
		case '"':
			end := i + 1
			for end < len(b) && b[end] != '"' {
				if b[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(b) {
				if key {
					cut(nil)
				} else {
					cut(append(unescaped(b[:len(b):len(b)], i), '"'))
				}
				return
			}
			if key {
				keyAt = i
			} else {
				safe, depth = end+1, len(stack)
			}
			i = end + 1
		default:
			end := i
			for end < len(b) && !bytes.ContainsRune([]byte(",}] \t\r\n"), rune(b[end])) {
				end++
			}
			if end >= len(b) {
				cut(nil)
				return
			}
			i = end
			safe, depth = i, len(stack)
		}
	}
	cut(nil)
}

// A body cut inside a string that opened at from, without the escape the cut left half written
func unescaped(b []byte, from int) []byte {
	slashes := 0
	for i := len(b) - 1; i > from && b[i] == '\\'; i-- {
		slashes++
	}
	if slashes%2 == 1 {
		return b[:len(b)-1]
	}
	for i := len(b) - 1; i > from && i >= len(b)-4; i-- {
		if b[i] == 'u' && b[i-1] == '\\' {
			back := 0
			for j := i - 1; j > from && b[j] == '\\'; j-- {
				back++
			}
			if back%2 == 1 {
				return b[:i-1]
			}
		}
	}
	return b
}

// The body with every container still open closed, innermost first
func closed(b []byte, open []byte) []byte {
	out := append([]byte(nil), b...)
	for i := len(open) - 1; i >= 0; i-- {
		if open[i] == '{' {
			out = append(out, '}')
		} else {
			out = append(out, ']')
		}
	}
	return out
}
