package tools

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const (
	// Waits between connection attempts, doubling to the longest
	reconnectMin   = 2 * time.Second
	reconnectMax   = time.Minute
	connectTimeout = 30 * time.Second
	// Bytes of a stdio server's stderr kept for its error message
	stderrKeep = 4096
)

var toolNameChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// A tool server speaking MCP, over a command's streams or an HTTP endpoint
type mcpServer struct {
	spec    *v1.ToolServer
	log     *slog.Logger
	mu      sync.Mutex
	session *mcp.ClientSession
	tools   []Tool
	// The last lines the command wrote to stderr, for the error when it dies
	stderr *tailWriter
}

func newMCP(spec *v1.ToolServer, log *slog.Logger) *mcpServer {
	return &mcpServer{spec: spec, log: log}
}

func (m *mcpServer) Name() string            { return m.spec.GetName() }
func (m *mcpServer) Kind() v1.ToolServerKind { return m.spec.GetKind() }

func (m *mcpServer) Tools() []Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Tool(nil), m.tools...)
}

// Keeps the server connected until ctx ends, reporting each attempt's outcome
func (m *mcpServer) run(ctx context.Context, settle func(error)) {
	wait := reconnectMin
	for ctx.Err() == nil {
		session, err := m.connect(ctx)
		if err != nil {
			settle(err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, reconnectMax)
			continue
		}
		wait = reconnectMin
		if err := m.refresh(ctx, session); err != nil {
			session.Close()
			settle(err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		m.mu.Lock()
		m.session = session
		m.mu.Unlock()
		settle(nil)
		err = session.Wait()
		m.mu.Lock()
		if m.session == session {
			m.session = nil
		}
		m.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("the server closed the connection")
		}
		settle(m.describe(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Opens one session, a command's or an endpoint's
func (m *mcpServer) connect(ctx context.Context) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "nebu", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(ctx context.Context, req *mcp.ToolListChangedRequest) {
			if err := m.refresh(ctx, req.Session); err != nil {
				m.log.Warn("tool server tools could not be listed again", "server", m.spec.GetName(), "err", err)
			}
		},
	})
	dial, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	var transport mcp.Transport
	switch m.spec.GetKind() {
	case v1.ToolServerKind_TOOL_SERVER_KIND_STDIO:
		cmd := exec.CommandContext(ctx, m.spec.GetCommand(), m.spec.GetArgs()...)
		cmd.Env = os.Environ()
		for k, v := range m.spec.GetEnv() {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		m.stderr = &tailWriter{}
		cmd.Stderr = m.stderr
		transport = &mcp.CommandTransport{Command: cmd}
	case v1.ToolServerKind_TOOL_SERVER_KIND_HTTP:
		transport = &mcp.StreamableClientTransport{Endpoint: m.spec.GetUrl(), HTTPClient: &http.Client{Transport: &headerTransport{headers: m.spec.GetHeaders(), next: http.DefaultTransport}}}
	default:
		return nil, fmt.Errorf("tool server %s is neither a command nor an endpoint", m.spec.GetName())
	}
	session, err := client.Connect(dial, transport, nil)
	if err != nil {
		return nil, m.describe(err)
	}
	return session, nil
}

// The error with what the command said on stderr, when it said anything
func (m *mcpServer) describe(err error) error {
	if m.stderr == nil {
		return err
	}
	if said := m.stderr.String(); said != "" {
		return fmt.Errorf("%w: %s", err, said)
	}
	return err
}

// Lists the server's tools and keeps the ones Settings allow
func (m *mcpServer) refresh(ctx context.Context, session *mcp.ClientSession) error {
	var tools []Tool
	taken := map[string]bool{}
	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("tools/list: %w", err)
		}
		if !allowed(m.spec.GetAllowedTools(), t.Name) {
			continue
		}
		schema, err := json.Marshal(t.InputSchema)
		if err != nil || len(schema) == 0 || string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		name := modelName(m.spec.GetName(), t.Name, taken)
		tools = append(tools, Tool{Name: name, Inner: t.Name, Description: t.Description, Schema: schema, ReadOnly: t.Annotations != nil && t.Annotations.ReadOnlyHint})
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	m.mu.Lock()
	m.tools = tools
	m.mu.Unlock()
	return nil
}

// Whether a tool is on the allowed list, every tool being allowed by an empty list
func allowed(list []string, name string) bool {
	if len(list) == 0 {
		return true
	}
	for _, a := range list {
		if a == name {
			return true
		}
	}
	return false
}

// The name the model calls a server tool by, server plus two underscores plus the cleaned tool name
func modelName(server, tool string, taken map[string]bool) string {
	base := server + Join + toolNameChars.ReplaceAllString(tool, "_")
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	taken[name] = true
	return name
}

// Runs one tool, the server's answer as parts and its failure as a failed result
func (m *mcpServer) Call(ctx context.Context, c Call) (*Result, error) {
	m.mu.Lock()
	session := m.session
	var inner string
	for _, t := range m.tools {
		if t.Name == c.Name {
			inner = t.Inner
		}
	}
	m.mu.Unlock()
	if session == nil {
		return textResult("The tool server "+m.spec.GetName()+" is not connected right now.", true), fmt.Errorf("tool server %s is not connected", m.spec.GetName())
	}
	if inner == "" {
		return textResult("The tool server "+m.spec.GetName()+" no longer offers "+c.Name+".", true), fmt.Errorf("tool server %s does not offer %s", m.spec.GetName(), c.Name)
	}
	var args map[string]any
	if len(c.Args) > 0 {
		if err := json.Unmarshal(c.Args, &args); err != nil {
			return textResult("The arguments are not a JSON object.", true), nil
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: inner, Arguments: args})
	if err != nil {
		out := textResult("The tool could not be called: "+err.Error(), true)
		out.Asked = true
		return out, fmt.Errorf("tool server %s, %s: %w", m.spec.GetName(), inner, err)
	}
	out := fromMCP(res)
	out.Asked = true
	return out, nil
}

// A call result as the gateway reads it, every content kind as text or an image
func fromMCP(res *mcp.CallToolResult) *Result {
	out := &Result{Failed: res.IsError}
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			out.Parts = append(out.Parts, Part{Type: "text", Text: v.Text})
		case *mcp.ImageContent:
			out.Parts = append(out.Parts, Part{Type: "image", MediaType: v.MIMEType, Data: base64.StdEncoding.EncodeToString(v.Data)})
		case *mcp.AudioContent:
			out.Parts = append(out.Parts, Part{Type: "text", Text: "[audio " + v.MIMEType + ", which the model cannot hear]"})
		case *mcp.EmbeddedResource:
			if v.Resource == nil {
				continue
			}
			if v.Resource.Text != "" {
				out.Parts = append(out.Parts, Part{Type: "text", Text: v.Resource.Text})
			} else if strings.HasPrefix(v.Resource.MIMEType, "image/") {
				out.Parts = append(out.Parts, Part{Type: "image", MediaType: v.Resource.MIMEType, Data: base64.StdEncoding.EncodeToString(v.Resource.Blob)})
			} else {
				out.Parts = append(out.Parts, Part{Type: "text", Text: "[resource " + v.Resource.URI + " of type " + v.Resource.MIMEType + "]"})
			}
		case *mcp.ResourceLink:
			out.Parts = append(out.Parts, Part{Type: "text", Text: "[resource " + v.URI + "] " + strings.TrimSpace(v.Name+" "+v.Description)})
		}
	}
	if len(out.Parts) == 0 && res.StructuredContent != nil {
		if data, err := json.Marshal(res.StructuredContent); err == nil {
			out.Parts = append(out.Parts, Part{Type: "text", Text: string(data)})
		}
	}
	if len(out.Parts) == 0 {
		out.Parts = []Part{{Type: "text", Text: "The tool answered with nothing."}}
	}
	return out
}

// Closes the session, the command with it
func (m *mcpServer) Close() error {
	m.mu.Lock()
	session := m.session
	m.session = nil
	m.mu.Unlock()
	if session != nil {
		return session.Close()
	}
	return nil
}

// Connects an MCP endpoint a request named, for the request alone
func Connect(ctx context.Context, name, endpoint string, headers map[string]string, allowed []string, log *slog.Logger) (*mcpServer, error) {
	spec := &v1.ToolServer{Name: name, Kind: v1.ToolServerKind_TOOL_SERVER_KIND_HTTP, Url: endpoint, Headers: headers, AllowedTools: allowed, Enabled: true}
	m := newMCP(spec, log)
	session, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	if err := m.refresh(ctx, session); err != nil {
		session.Close()
		return nil, err
	}
	m.mu.Lock()
	m.session = session
	m.mu.Unlock()
	return m, nil
}

// Adds fixed headers to every request
type headerTransport struct {
	headers map[string]string
	next    http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return t.next.RoundTrip(req)
}

// Keeps the last bytes written, for a command's stderr
type tailWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > stderrKeep {
		t.buf = t.buf[len(t.buf)-stderrKeep:]
	}
	return len(p), nil
}

// The last line written, trimmed
func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	last := ""
	sc := bufio.NewScanner(strings.NewReader(string(t.buf)))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			last = line
		}
	}
	return last
}
