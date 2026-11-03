package vllm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/nickheyer/nebu/pkg/transcode"
)

type toolMode int

const (
	toolsOff toolMode = iota
	toolsAuto
	toolsNamed
	toolsRequired
)

// How one chat request turns model text into tool calls
type toolPlan struct {
	mode   toolMode
	parser ToolParser
	name   string
	single bool
	rid    string
}

// What a streamed response already sent so only new content and calls go out
type toolState struct {
	acc        strings.Builder
	sentText   string
	sentCalls  int
	headerSent bool
}

// Decides the tool mode from tools, tool_choice and parallel_tool_calls and returns the forcing JSON schema
func planTools(tools []Tool, choice json.RawMessage, parallel *bool, parser ToolParser, rid string) (toolPlan, string, error) {
	plan := toolPlan{parser: parser, rid: rid, single: parallel != nil && !*parallel}
	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		if t.Type != "function" {
			return plan, "", transcode.BadRequest("tool type %q is unknown, only function tools exist", t.Type)
		}
		if t.Function.Name == "" {
			return plan, "", transcode.BadRequest("function tool lacks a name")
		}
		byName[t.Function.Name] = t
	}
	choice = bytes.TrimSpace(choice)
	var kind string
	switch {
	case len(choice) == 0 || string(choice) == "null":
		kind = "auto"
	case choice[0] == '"':
		if err := json.Unmarshal(choice, &kind); err != nil {
			return plan, "", transcode.BadRequest("tool_choice: %v", err)
		}
	default:
		var named struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := json.Unmarshal(choice, &named); err != nil {
			return plan, "", transcode.BadRequest("tool_choice: %v", err)
		}
		t, ok := byName[named.Function.Name]
		if named.Type != "function" || !ok {
			return plan, "", transcode.BadRequest("tool_choice names function %q which is not among the tools", named.Function.Name)
		}
		plan.mode, plan.name = toolsNamed, t.Function.Name
		return plan, compact(parameters(t)), nil
	}
	switch kind {
	case "none":
		return plan, "", nil
	case "auto":
		if len(tools) == 0 {
			return plan, "", nil
		}
		if parser == nil {
			return plan, "", transcode.BadRequest("tool_choice auto needs a tool parser for this model")
		}
		plan.mode = toolsAuto
		return plan, "", nil
	case "required":
		if len(tools) == 0 {
			return plan, "", transcode.BadRequest("tool_choice required needs at least one tool")
		}
		plan.mode = toolsRequired
		return plan, requiredSchema(tools, plan.single), nil
	}
	return plan, "", transcode.BadRequest("tool_choice %q is unknown", kind)
}

// Returns the tool parameter schema or an empty object schema when the tool declares none
func parameters(t Tool) json.RawMessage {
	if len(t.Function.Parameters) == 0 || string(t.Function.Parameters) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return t.Function.Parameters
}

// Builds the JSON schema forcing an array of name and parameters objects, one entry per offered tool
func requiredSchema(tools []Tool, single bool) string {
	variants := make([]any, 0, len(tools))
	for _, t := range tools {
		variants = append(variants, map[string]any{
			"properties": map[string]any{
				"name":       map[string]any{"type": "string", "enum": []string{t.Function.Name}},
				"parameters": parameters(t),
			},
			"required": []string{"name", "parameters"},
		})
	}
	schema := map[string]any{
		"type":     "array",
		"minItems": 1,
		"items":    map[string]any{"type": "object", "anyOf": variants},
	}
	if single {
		schema["maxItems"] = 1
	}
	b, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Returns the id the n-th call of this request carries when the parser leaves it empty
func (p *toolPlan) callID(n int) string {
	return fmt.Sprintf("call_%s_%d", p.rid, n)
}

// Fills missing call ids and types and keeps one call when parallel calls are off
func (p *toolPlan) normalize(calls []ToolCall, from int) []ToolCall {
	if p.single && len(calls) > 1 {
		calls = calls[:1]
	}
	out := make([]ToolCall, len(calls))
	for i, c := range calls {
		if c.ID == "" {
			c.ID = p.callID(from + i)
		}
		if c.Type == "" {
			c.Type = "function"
		}
		out[i] = c
	}
	return out
}

// Splits a finished response into message content and tool calls, stopped tells whether output ended normally
func (p *toolPlan) final(text string, stopped bool) (*string, []ToolCall, error) {
	switch p.mode {
	case toolsAuto:
		content, calls, ok := p.parser.Parse(text)
		if !ok {
			return &text, nil, nil
		}
		calls = p.normalize(calls, 0)
		if content == "" && len(calls) > 0 {
			return nil, calls, nil
		}
		return &content, calls, nil
	case toolsNamed:
		return nil, []ToolCall{{ID: p.callID(0), Type: "function", Function: FunctionCall{Name: p.name, Arguments: text}}}, nil
	case toolsRequired:
		calls, err := p.required(text, 0, stopped)
		return nil, calls, err
	}
	return &text, nil, nil
}

// Parses forced tool output from element skip onward, a stopped output must be a closed JSON array
func (p *toolPlan) required(text string, skip int, stopped bool) ([]ToolCall, error) {
	elems, closed := arrayElements(text)
	if stopped && !closed {
		return nil, transcode.BadGateway("forced tool output is not a JSON array")
	}
	var calls []ToolCall
	for i := skip; i < len(elems); i++ {
		var e struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		}
		if err := json.Unmarshal([]byte(elems[i]), &e); err != nil || e.Name == "" {
			return nil, transcode.BadGateway("forced tool output element %d is not a name and parameters object", i)
		}
		args := "{}"
		if len(e.Parameters) > 0 {
			args = compact(e.Parameters)
		}
		calls = append(calls, ToolCall{ID: p.callID(i), Type: "function", Function: FunctionCall{Name: e.Name, Arguments: args}})
	}
	return calls, nil
}

// Turns one streamed text piece into the content and tool call deltas not yet sent
func (p *toolPlan) delta(st *toolState, text string, final, stopped bool) (string, []toolCallDelta, error) {
	switch p.mode {
	case toolsAuto:
		st.acc.WriteString(text)
		acc := st.acc.String()
		content, calls, ok := p.parser.Parse(acc)
		if !ok {
			content, calls = acc, nil
		}
		calls = p.normalize(calls, 0)
		if !strings.HasPrefix(content, st.sentText) || len(calls) < st.sentCalls {
			return "", nil, transcode.NewError(http.StatusInternalServerError, "tool parser retracted output that was already streamed")
		}
		out := content[len(st.sentText):]
		st.sentText = content
		deltas := make([]toolCallDelta, 0, len(calls)-st.sentCalls)
		for i := st.sentCalls; i < len(calls); i++ {
			deltas = append(deltas, fullDelta(i, calls[i]))
		}
		st.sentCalls = len(calls)
		return out, deltas, nil
	case toolsNamed:
		if text == "" && (!final || st.headerSent) {
			return "", nil, nil
		}
		if st.headerSent {
			return "", []toolCallDelta{{Index: 0, Function: functionDelta{Arguments: text}}}, nil
		}
		st.headerSent = true
		call := ToolCall{ID: p.callID(0), Type: "function", Function: FunctionCall{Name: p.name, Arguments: text}}
		return "", []toolCallDelta{fullDelta(0, call)}, nil
	case toolsRequired:
		st.acc.WriteString(text)
		calls, err := p.required(st.acc.String(), st.sentCalls, final && stopped)
		if err != nil {
			return "", nil, err
		}
		deltas := make([]toolCallDelta, 0, len(calls))
		for i, c := range calls {
			deltas = append(deltas, fullDelta(st.sentCalls+i, c))
		}
		st.sentCalls += len(calls)
		return "", deltas, nil
	}
	return text, nil, nil
}

// Returns whether a finished stopped response reports tool_calls instead of stop
func (p *toolPlan) called(n int) bool {
	return (p.mode == toolsAuto || p.mode == toolsRequired) && n > 0
}

// Builds the delta announcing a complete call with its id, name and arguments
func fullDelta(index int, c ToolCall) toolCallDelta {
	return toolCallDelta{Index: index, ID: c.ID, Type: c.Type, Function: functionDelta{Name: c.Function.Name, Arguments: c.Function.Arguments}}
}

// Returns the complete top level elements of a JSON array prefix and whether the array has closed
func arrayElements(s string) ([]string, bool) {
	var elems []string
	depth, start := 0, -1
	inString, escaped := false, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case ' ', '\t', '\n', '\r':
		case '[', '{':
			if depth == 0 && ch != '[' {
				return elems, false
			}
			if depth == 1 && start < 0 {
				start = i
			}
			depth++
		case ']', '}':
			depth--
			switch {
			case depth == 1 && start >= 0:
				elems = append(elems, s[start:i+1])
				start = -1
			case depth == 0:
				if start >= 0 {
					elems = append(elems, strings.TrimSpace(s[start:i]))
				}
				return elems, true
			}
		case ',':
			if depth == 1 && start >= 0 {
				elems = append(elems, strings.TrimSpace(s[start:i]))
				start = -1
			}
		default:
			if depth == 0 {
				return elems, false
			}
			if ch == '"' {
				inString = true
			}
			if depth == 1 && start < 0 {
				start = i
			}
		}
	}
	return elems, false
}
