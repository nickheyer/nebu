// Package source holds a chat template's text with where it was read from, apart from the reader and the renderer
package source

import "regexp"

// A template with where it was read from
type Template struct {
	Text   string
	Source string
	// The variant a GGUF names for requests carrying tools, empty when it names none
	ToolUse string
}

// Jinja string literals and comments, which never read a variable
var literals = regexp.MustCompile(`'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|\{#[\s\S]*?#\}`)

// A read of the tools variable, not an attribute or a longer name
var toolsVariable = regexp.MustCompile(`(?:^|[^\w.])tools\b`)

// Whether the template renders tool definitions
func (t *Template) Tools() bool {
	return t.ToolUse != "" || toolsVariable.MatchString(literals.ReplaceAllString(t.Text, ""))
}

// Whether a quoted string literal of the template text is exactly the word
func Quotes(text, word string) bool {
	for _, lit := range literals.FindAllString(text, -1) {
		if len(lit) >= 2 && (lit[0] == '\'' || lit[0] == '"') && lit[1:len(lit)-1] == word {
			return true
		}
	}
	return false
}
