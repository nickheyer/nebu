package gateway

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/nickheyer/nebu/internal/chat"
)

// The identity line a coding agent harness opens its system prompt with
const harnessIdentity = "You are Claude Code"

// The prefix of the headers a coding agent harness names its session, agent, and prompt in
const harnessHeaders = "X-Claude-Code-"

// The opening of the block a coding agent harness prepends to its system prompt for attribution
const attributionMarker = "x-anthropic-billing-header"

// Whether a coding agent harness sent the request, by its headers or its identity line
func isHarness(h http.Header, c *chat.Request) bool {
	for name := range h {
		if strings.HasPrefix(name, harnessHeaders) {
			return true
		}
	}
	if c == nil {
		return false
	}
	for _, m := range c.Messages {
		if m.Role != "system" {
			continue
		}
		for _, p := range m.Parts {
			if p.Type == "text" && strings.HasPrefix(strings.TrimSpace(p.Text), harnessIdentity) {
				return true
			}
		}
	}
	return false
}

// The environment facts a harness states that the facts prompt carries, by the name it gives each
var harnessFacts = []struct{ name, label string }{
	{"Primary working directory", "Working directory"},
	{"Additional working directories", "Additional working directories"},
	{"Is a git repository", "Git repository"},
	{"Platform", "Platform"},
	{"Shell", "Shell"},
	{"OS Version", "OS version"},
	{"Scratchpad directory", "Scratchpad directory"},
}

// The opening of the gateway's own system prompt
const factsOpening = "You are a coding agent in the user's terminal, acting through the tools offered. The user's own instructions arrive in the conversation."

// The gateway's own system prompt, the environment facts found in the system text and nothing else of it
func factsPrompt(messages []chat.Message) string {
	var b strings.Builder
	b.WriteString(factsOpening)
	for _, m := range messages {
		if m.Role != "system" {
			continue
		}
		for _, line := range strings.Split(chat.TextOf(m.Parts), "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
			for _, f := range harnessFacts {
				if value, ok := strings.CutPrefix(line, f.name+":"); ok {
					fmt.Fprintf(&b, "\n%s: %s", f.label, strings.TrimSpace(value))
				}
			}
		}
	}
	return b.String()
}
