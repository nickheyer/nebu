package gateway

import (
	"errors"
	"strings"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Separator for merged system messages
const systemJoin = "\n\n"

// Whether the request has a later system turn the mode folds
func foldsSystem(c *chat.Request, mode v1.SystemMessages) bool {
	if mode != v1.SystemMessages_SYSTEM_MESSAGES_MERGE && mode != v1.SystemMessages_SYSTEM_MESSAGES_USER {
		return false
	}
	for i, m := range c.Messages {
		if i > 0 && m.Role == "system" {
			return true
		}
	}
	return false
}

// Folds later system turns as the route says, merged into the first or turned into user turns
func foldSystem(c *chat.Request, mode v1.SystemMessages) {
	switch mode {
	case v1.SystemMessages_SYSTEM_MESSAGES_MERGE:
		var kept []chat.Message
		var late []string
		for i, m := range c.Messages {
			if i > 0 && m.Role == "system" {
				late = append(late, chat.TextOf(m.Parts))
				continue
			}
			kept = append(kept, m)
		}
		if len(late) == 0 {
			return
		}
		text := strings.Join(late, systemJoin)
		if len(kept) > 0 && kept[0].Role == "system" {
			kept[0].Parts = chat.AppendText(kept[0].Parts, text)
		} else {
			kept = append([]chat.Message{{Role: "system", Parts: []chat.Part{{Type: "text", Text: text}}}}, kept...)
		}
		c.Messages = kept
	case v1.SystemMessages_SYSTEM_MESSAGES_USER:
		for i := range c.Messages {
			if i > 0 && c.Messages[i].Role == "system" {
				c.Messages[i].Role = "user"
			}
		}
	}
}

// Refuses a profile that offers mutating tools with no tool server to take them from
func CheckProfile(p *v1.Profile) error {
	if p.GetMutatingTools() && len(p.GetToolServers()) == 0 {
		return errors.New("mutating tools need at least one tool server")
	}
	return nil
}

// Reports whether a profile sets anything, an empty one inheriting everything
func ProfileSet(p *v1.Profile) bool {
	return p.GetSystemMessages() != v1.SystemMessages_SYSTEM_MESSAGES_UNSPECIFIED || len(p.GetToolServers()) > 0 || p.GetMutatingTools()
}

// Merges an alias profile over its slot's, every alias field that is set winning
func EffectiveProfile(alias, slot *v1.Profile) *v1.Profile {
	if !ProfileSet(alias) {
		return slot
	}
	out := &v1.Profile{SystemMessages: slot.GetSystemMessages(), ToolServers: append([]string(nil), slot.GetToolServers()...), MutatingTools: slot.GetMutatingTools()}
	if alias.GetSystemMessages() != v1.SystemMessages_SYSTEM_MESSAGES_UNSPECIFIED {
		out.SystemMessages = alias.GetSystemMessages()
	}
	if len(alias.GetToolServers()) > 0 {
		out.ToolServers, out.MutatingTools = append([]string(nil), alias.GetToolServers()...), alias.GetMutatingTools()
	}
	return out
}
