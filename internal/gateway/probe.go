package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Maximum probe error body size
const refusalCap = 512

// The tool the probe offers
var probeTool = chat.Tool{Name: "probe", Description: "Reports whether the runtime takes tools", Schema: json.RawMessage(`{"type":"object","properties":{}}`)}

// Probes later system messages and tools with three one token requests, a baseline failure being inconclusive
func ProbeTemplate(ctx context.Context, client *http.Client, endpoint string, api v1.ApiFlavor, model string) *v1.TemplateProbe {
	target, err := url.Parse(endpoint)
	if err != nil {
		return &v1.TemplateProbe{Error: "endpoint: " + err.Error()}
	}
	runtime := chat.RuntimeOf(api)
	turns := []chat.Message{
		{Role: "system", Parts: []chat.Part{{Type: "text", Text: "You answer briefly."}}},
		{Role: "user", Parts: []chat.Part{{Type: "text", Text: "Say ok."}}},
		{Role: "assistant", Parts: []chat.Part{{Type: "text", Text: "ok"}}},
	}
	control := append(append([]chat.Message{}, turns...), chat.Message{Role: "user", Parts: []chat.Part{{Type: "text", Text: "Say ok again."}}})
	probe := append(append([]chat.Message{}, turns...),
		chat.Message{Role: "system", Parts: []chat.Part{{Type: "text", Text: "Answer in lower case."}}},
		chat.Message{Role: "user", Parts: []chat.Part{{Type: "text", Text: "Say ok again."}}})
	if status, message, err := sendOnce(ctx, client, target, runtime, &chat.Request{Kind: chat.Chat, Model: model, MaxTokens: 1, Messages: control}); err != nil {
		return &v1.TemplateProbe{Error: "a plain chat request failed: " + err.Error()}
	} else if status >= http.StatusMultipleChoices {
		return &v1.TemplateProbe{Error: fmt.Sprintf("the runtime answered %d to a plain chat request: %s", status, message)}
	}
	status, message, err := sendOnce(ctx, client, target, runtime, &chat.Request{Kind: chat.Chat, Model: model, MaxTokens: 1, Messages: probe})
	if err != nil {
		return &v1.TemplateProbe{Error: "the chat request with a late system message failed: " + err.Error()}
	}
	out := &v1.TemplateProbe{LateSystem: status < http.StatusMultipleChoices}
	if !out.LateSystem {
		out.Refusal = message
	}
	status, message, err = sendOnce(ctx, client, target, runtime, &chat.Request{Kind: chat.Chat, Model: model, MaxTokens: 1, Messages: control, Tools: []chat.Tool{probeTool}, ToolChoice: "auto"})
	switch {
	case err != nil:
		out.Error = "the chat request with a tool failed: " + err.Error()
	case status >= http.StatusMultipleChoices:
		out.Tools, out.ToolsRefusal = v1.ToolSupport_TOOL_SUPPORT_EMULATED, message
	default:
		out.Tools = v1.ToolSupport_TOOL_SUPPORT_NATIVE
	}
	return out
}

// Sends one request and answers its status and any runtime error
func sendOnce(ctx context.Context, client *http.Client, target *url.URL, runtime chat.Runtime, req *chat.Request) (int, string, error) {
	path, out, err := runtime.Render(req)
	if err != nil {
		return 0, "", err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, target.ResolveReference(&url.URL{Path: path}).String(), strings.NewReader(string(out)))
	if err != nil {
		return 0, "", err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(hr)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode < http.StatusMultipleChoices {
		return resp.StatusCode, "", nil
	}
	message := runtime.ErrorMessage(raw)
	if message == "" {
		message = strings.TrimSpace(string(raw))
	}
	if len(message) > refusalCap {
		message = message[:refusalCap]
	}
	return resp.StatusCode, message, nil
}
