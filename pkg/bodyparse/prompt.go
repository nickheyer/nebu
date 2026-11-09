package bodyparse

import "strings"

// Builds the prompt text the kind defines, system text first
func (d *document) promptText() string {
	var sb strings.Builder
	sb.Grow(min(d.textBytes(), 1<<16))
	switch d.kind {
	case KindChatCompletions, KindOllamaChat:
		d.writeText(&sb, "messages")
	case KindAnthropicMessages:
		d.writeText(&sb, "system")
		d.writeText(&sb, "messages")
	case KindResponses:
		d.writeText(&sb, "instructions")
		d.writeText(&sb, "input")
	case KindCompletions:
		d.writeLines(&sb, "prompt")
	case KindOllamaGenerate:
		d.writeText(&sb, "system")
		d.writeLines(&sb, "prompt")
	case KindEmbeddings:
		d.writeLines(&sb, "input")
	case KindUnknown:
		d.writeText(&sb, "system")
		d.writeText(&sb, "instructions")
		d.writeText(&sb, "messages")
		d.writeLines(&sb, "prompt")
		d.writeText(&sb, "input")
	}
	return sb.String()
}

func (d *document) writeText(sb *strings.Builder, name string) {
	if f, ok := d.field(name); ok {
		writeText(sb, d.body, f.vs)
	}
}

// Writes a string, or the strings of an array joined by newlines, skipping token ids
func (d *document) writeLines(sb *strings.Builder, name string) {
	f, ok := d.field(name)
	if !ok {
		return
	}
	b := d.body
	switch b[f.vs] {
	case '"':
		writeUnquoted(sb, b[f.vs+1:f.ve-1])
	case '[':
		it := arrayAt(b, f.vs)
		first := true
		for {
			vs, ve, ok := it.next()
			if !ok {
				return
			}
			if b[vs] != '"' {
				continue
			}
			if !first {
				sb.WriteByte('\n')
			}
			first = false
			writeUnquoted(sb, b[vs+1:ve-1])
		}
	}
}

// Writes the text a value carries and returns its end, objects contributing their text and content members
func writeText(sb *strings.Builder, b []byte, i int) int {
	switch b[i] {
	case '"':
		end, _, _ := scanString(b, i)
		writeUnquoted(sb, b[i+1:end-1])
		return end
	case '[':
		if i = skipWS(b, i+1); b[i] == ']' {
			return i + 1
		}
		for {
			if i = skipWS(b, writeText(sb, b, i)); b[i] == ']' {
				return i + 1
			}
			i = skipWS(b, i+1)
		}
	case '{':
		if i = skipWS(b, i+1); b[i] == '}' {
			return i + 1
		}
		for {
			ke, esc, _ := scanString(b, i)
			key := b[i+1 : ke-1]
			esc = needsDecode(key, esc)
			vs := skipWS(b, skipWS(b, ke)+1)
			var ve int
			switch {
			case b[vs] == '"' && keyIs(key, esc, "text"), keyIs(key, esc, "content"):
				ve = writeText(sb, b, vs)
			default:
				ve = skip(b, vs)
			}
			if i = skipWS(b, ve); b[i] == '}' {
				return i + 1
			}
			i = skipWS(b, i+1)
		}
	default:
		return skip(b, i)
	}
}

// Bytes of the members a prompt may be read from, bounding the first allocation
func (d *document) textBytes() int {
	n := 0
	for _, name := range [...]string{"system", "instructions", "messages", "prompt", "input"} {
		if f, ok := d.field(name); ok {
			n += f.ve - f.vs
		}
	}
	return n
}
