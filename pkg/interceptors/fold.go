package interceptors

import (
	"strings"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Separator for merged system messages
const systemJoin = "\n\n"

// Folds developer turns into system turns, then system turns after the first as the route says
type Fold struct{}

func (Fold) Stage() Stage { return Request }

func (Fold) Guard() bool { return false }

func (Fold) Intercept(x *Exchange) (bool, error) {
	c := x.Req
	changed := false
	for i := range c.Messages {
		if c.Messages[i].Role == "developer" {
			c.Messages[i].Role, changed = "system", true
		}
	}
	switch x.SystemMode {
	case v1.SystemMessages_SYSTEM_MESSAGES_MERGE:
		return merge(c) || changed, nil
	case v1.SystemMessages_SYSTEM_MESSAGES_USER:
		return join(c) || changed, nil
	}
	return changed, nil
}

// Moves the text of every system turn after the first into the first, made when there is none
func merge(c *chat.Request) bool {
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
		return false
	}
	text := strings.Join(late, systemJoin)
	if len(kept) > 0 && kept[0].Role == "system" {
		kept[0].Parts = chat.AppendText(kept[0].Parts, text)
	} else {
		kept = append([]chat.Message{{Role: "system", Parts: []chat.Part{{Type: "text", Text: text}}}}, kept...)
	}
	c.Messages = kept
	return true
}

// Joins every late system turn to the user turn beside it, a lone one becoming a user turn
func join(c *chat.Request) bool {
	var kept []chat.Message
	var pending []chat.Part
	changed := false
	for i, m := range c.Messages {
		if i > 0 && m.Role == "system" {
			changed = true
			if n := len(kept); n > 0 && kept[n-1].Role == "user" {
				kept[n-1].Parts = chat.JoinParts(kept[n-1].Parts, m.Parts)
			} else {
				pending = chat.JoinParts(pending, m.Parts)
			}
			continue
		}
		if len(pending) > 0 {
			if m.Role == "user" {
				m.Parts = chat.JoinParts(pending, m.Parts)
			} else {
				kept = append(kept, chat.Message{Role: "user", Parts: pending})
			}
			pending = nil
		}
		kept = append(kept, m)
	}
	if len(pending) > 0 {
		kept = append(kept, chat.Message{Role: "user", Parts: pending})
	}
	if changed {
		c.Messages = kept
	}
	return changed
}
