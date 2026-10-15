package tools

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Returned when a tool server's settings are malformed
var ErrServer = errors.New("invalid tool server")

var serverName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

// Trims and checks tool servers, assigning ids and refusing clashes and missing settings
func Check(list []*v1.ToolServer) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrServer}, args...)...)
	}
	names := map[string]bool{}
	ids := map[string]bool{}
	offered := map[string]string{}
	for i, s := range list {
		s.Name = strings.TrimSpace(s.GetName())
		if s.GetName() == "" {
			return bad("tool server %d needs a name", i+1)
		}
		if !serverName.MatchString(s.GetName()) || strings.Contains(s.GetName(), Join) {
			return bad("the name %q is not letters, digits, - and _, at most 32, without two underscores in a row", s.GetName())
		}
		if names[s.GetName()] {
			return bad("two tool servers are named %s", s.GetName())
		}
		names[s.GetName()] = true
		if s.GetId() == "" {
			s.Id = db.NewID()
		}
		if ids[s.GetId()] {
			return bad("two tool servers share the id %s", s.GetId())
		}
		ids[s.GetId()] = true
		s.AllowedTools = trimAll(s.GetAllowedTools())
		s.Provider, s.SearxngUrl, s.BraveKey = strings.TrimSpace(s.GetProvider()), strings.TrimRight(strings.TrimSpace(s.GetSearxngUrl()), "/"), strings.TrimSpace(s.GetBraveKey())
		s.Command, s.Url = strings.TrimSpace(s.GetCommand()), strings.TrimSpace(s.GetUrl())
		s.Args = trimAll(s.GetArgs())
		switch s.GetKind() {
		case v1.ToolServerKind_TOOL_SERVER_KIND_SEARCH:
			switch s.GetProvider() {
			case "searxng":
				u, err := url.Parse(s.GetSearxngUrl())
				if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
					return bad("%s: the SearXNG URL must start with http:// or https:// and name a host", s.GetName())
				}
			case "brave":
				if s.GetBraveKey() == "" {
					return bad("%s: Brave Search needs a subscription token", s.GetName())
				}
			default:
				return bad("%s: web search provider %q is not searxng or brave", s.GetName(), s.GetProvider())
			}
			if err := claim(offered, SearchTool, s); err != nil {
				return bad("%v", err)
			}
		case v1.ToolServerKind_TOOL_SERVER_KIND_FETCH:
			if err := claim(offered, FetchTool, s); err != nil {
				return bad("%v", err)
			}
		case v1.ToolServerKind_TOOL_SERVER_KIND_STDIO:
			if s.GetCommand() == "" {
				return bad("%s: a stdio server needs a command", s.GetName())
			}
			for k := range s.GetEnv() {
				if strings.TrimSpace(k) == "" || strings.ContainsAny(k, "= \t\n") {
					return bad("%s: environment variable names cannot be empty or contain =", s.GetName())
				}
			}
		case v1.ToolServerKind_TOOL_SERVER_KIND_HTTP:
			u, err := url.Parse(s.GetUrl())
			if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
				return bad("%s: the URL must start with http:// or https:// and name a host", s.GetName())
			}
		default:
			return bad("%s: the kind must be search, fetch, stdio, or http", s.GetName())
		}
	}
	return nil
}

// Records which enabled server offers a fixed tool name, refusing a second
func claim(offered map[string]string, tool string, s *v1.ToolServer) error {
	if !s.GetEnabled() {
		return nil
	}
	if other, taken := offered[tool]; taken {
		return fmt.Errorf("%s and %s would both offer %s, turn one off", other, s.GetName(), tool)
	}
	offered[tool] = s.GetName()
	return nil
}

func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
