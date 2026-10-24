package interceptors

import (
	"encoding/base64"
	"unicode/utf8"

	"github.com/nickheyer/nebu/internal/chat"
)

// The header carrying the system prompt that replaces the client's, base64 encoded UTF-8
const SystemHeader = "X-Nebu-System"

// Replaces every system and developer turn with the header's prompt and pins it against guards
type System struct{}

func (System) Stage() Stage { return Request }

func (System) Guard() bool { return false }

func (System) Intercept(x *Exchange) (bool, error) {
	raw := x.Header.Get(SystemHeader)
	// Embeddings carry no turns to set a system prompt on
	if raw == "" || x.Req.Kind == chat.Embed {
		return false, nil
	}
	text, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return false, bad("%s is not base64: %v", SystemHeader, err)
	}
	if !utf8.Valid(text) {
		return false, bad("%s does not decode to UTF-8 text", SystemHeader)
	}
	turns := []chat.Message{{Role: "system", Parts: []chat.Part{{Type: "text", Text: string(text)}}}}
	for _, m := range x.Req.Messages {
		if m.Role != "system" && m.Role != "developer" {
			turns = append(turns, m)
		}
	}
	x.Req.Messages, x.Pinned = turns, true
	return true, nil
}
