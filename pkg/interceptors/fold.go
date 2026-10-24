package interceptors

import (
	"strings"

	"github.com/nickheyer/nebu/internal/chat"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Separator for merged system messages
const systemJoin = "\n\n"

// Folds system turns after the first as the route says, merged into the first or turned into user turns
type Fold struct{}

func (Fold) Stage() Stage { return Request }

func (Fold) Guard() bool { return false }

func (Fold) Intercept(x *Exchange) (bool, error) {
	c := x.Req
	switch x.SystemMode {
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
			return false, nil
		}
		text := strings.Join(late, systemJoin)
		if len(kept) > 0 && kept[0].Role == "system" {
			kept[0].Parts = chat.AppendText(kept[0].Parts, text)
		} else {
			kept = append([]chat.Message{{Role: "system", Parts: []chat.Part{{Type: "text", Text: text}}}}, kept...)
		}
		c.Messages = kept
		return true, nil
	case v1.SystemMessages_SYSTEM_MESSAGES_USER:
		changed := false
		for i := range c.Messages {
			if i > 0 && c.Messages[i].Role == "system" {
				c.Messages[i].Role, changed = "user", true
			}
		}
		return changed, nil
	}
	return false, nil
}
