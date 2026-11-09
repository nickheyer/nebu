package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/internal/tools"
	"github.com/nickheyer/nebu/pkg/outbound"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Time an MCP endpoint a request names has to connect and list its tools
const sourceConnect = 30 * time.Second

var sourceToolChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// An MCP endpoint a request named, reached only through the outbound guard and closed with the request
type source struct {
	name    string
	session *mcp.ClientSession
	tools   []tools.Tool
}

// Adds the request's token to every call it makes to the endpoint
type bearerTransport struct {
	token string
	next  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token == "" {
		return t.next.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(req)
}

// Connects an MCP endpoint a request names over the outbound guard and lists the tools it may offer
func connectSource(ctx context.Context, s chat.Source, log *slog.Logger) (*source, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "nebu", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: s.URL, HTTPClient: &http.Client{Transport: &bearerTransport{token: s.Token, next: outbound.Transport()}}}
	dial, cancel := context.WithTimeout(ctx, sourceConnect)
	defer cancel()
	session, err := client.Connect(dial, transport, nil)
	if err != nil {
		return nil, err
	}
	src := &source{name: s.Name, session: session}
	taken := map[string]bool{}
	for t, err := range session.Tools(dial, nil) {
		if err != nil {
			session.Close()
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		if len(s.Allowed) > 0 && !slices.Contains(s.Allowed, t.Name) {
			continue
		}
		schema, err := json.Marshal(t.InputSchema)
		if err != nil || len(schema) == 0 || string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		name := s.Name + tools.Join + sourceToolChars.ReplaceAllString(t.Name, "_")
		for i := 2; taken[name]; i++ {
			name = fmt.Sprintf("%s%s%s_%d", s.Name, tools.Join, sourceToolChars.ReplaceAllString(t.Name, "_"), i)
		}
		taken[name] = true
		src.tools = append(src.tools, tools.Tool{Name: name, Inner: t.Name, Description: t.Description, Schema: schema, ReadOnly: t.Annotations != nil && t.Annotations.ReadOnlyHint})
	}
	log.Debug("request mcp server connected", "server", s.Name, "tools", len(src.tools))
	return src, nil
}

func (s *source) Name() string            { return s.name }
func (s *source) Kind() v1.ToolServerKind { return v1.ToolServerKind_TOOL_SERVER_KIND_HTTP }
func (s *source) Tools() []tools.Tool     { return slices.Clone(s.tools) }
func (s *source) Close() error            { return s.session.Close() }

// Runs one tool, the endpoint's answer as parts and its failure as a failed result
func (s *source) Call(ctx context.Context, c tools.Call) (*tools.Result, error) {
	i := slices.IndexFunc(s.tools, func(t tools.Tool) bool { return t.Name == c.Name })
	if i < 0 {
		return &tools.Result{Parts: []tools.Part{{Type: "text", Text: "The tool server " + s.name + " does not offer " + c.Name + "."}}, Failed: true}, fmt.Errorf("mcp server %s does not offer %s", s.name, c.Name)
	}
	args := map[string]any{}
	if len(c.Args) > 0 {
		if err := json.Unmarshal(c.Args, &args); err != nil {
			return &tools.Result{Parts: []tools.Part{{Type: "text", Text: "The arguments are not a JSON object."}}, Failed: true}, nil
		}
	}
	res, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: s.tools[i].Inner, Arguments: args})
	if err != nil {
		return &tools.Result{Parts: []tools.Part{{Type: "text", Text: "The tool could not be called: " + err.Error()}}, Failed: true, Asked: true}, fmt.Errorf("mcp server %s, %s: %w", s.name, s.tools[i].Inner, err)
	}
	return resultOf(res), nil
}

// A call result as the gateway reads it, every content kind as text or an image
func resultOf(res *mcp.CallToolResult) *tools.Result {
	out := &tools.Result{Failed: res.IsError, Asked: true}
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			out.Parts = append(out.Parts, tools.Part{Type: "text", Text: v.Text})
		case *mcp.ImageContent:
			out.Parts = append(out.Parts, tools.Part{Type: "image", MediaType: v.MIMEType, Data: base64.StdEncoding.EncodeToString(v.Data)})
		case *mcp.AudioContent:
			out.Parts = append(out.Parts, tools.Part{Type: "text", Text: "[audio " + v.MIMEType + ", which the model cannot hear]"})
		case *mcp.EmbeddedResource:
			switch {
			case v.Resource == nil:
			case v.Resource.Text != "":
				out.Parts = append(out.Parts, tools.Part{Type: "text", Text: v.Resource.Text})
			case strings.HasPrefix(v.Resource.MIMEType, "image/"):
				out.Parts = append(out.Parts, tools.Part{Type: "image", MediaType: v.Resource.MIMEType, Data: base64.StdEncoding.EncodeToString(v.Resource.Blob)})
			default:
				out.Parts = append(out.Parts, tools.Part{Type: "text", Text: "[resource " + v.Resource.URI + " of type " + v.Resource.MIMEType + "]"})
			}
		case *mcp.ResourceLink:
			out.Parts = append(out.Parts, tools.Part{Type: "text", Text: "[resource " + v.URI + "] " + strings.TrimSpace(v.Name+" "+v.Description)})
		}
	}
	if len(out.Parts) == 0 && res.StructuredContent != nil {
		if data, err := json.Marshal(res.StructuredContent); err == nil {
			out.Parts = append(out.Parts, tools.Part{Type: "text", Text: string(data)})
		}
	}
	if len(out.Parts) == 0 {
		out.Parts = []tools.Part{{Type: "text", Text: "The tool answered with nothing."}}
	}
	return out
}
