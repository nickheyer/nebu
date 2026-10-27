// Package chattemplate reads the chat template a stored model carries and what it renders
package chattemplate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/nickheyer/nebu/pkg/formats/gguf"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The GGUF metadata keys llama.cpp reads templates from, the tool use one when a request offers tools
const (
	ggufKey        = "tokenizer.chat_template"
	ggufToolUseKey = "tokenizer.chat_template.tool_use"
)

// The files transformers reads a template from, in the order it prefers them
var fileNames = []string{"chat_template.jinja", "chat_template.json", "tokenizer_config.json"}

// The largest template file read
const maxFile = 16 << 20

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

// What the gateway records for an instance of the model
func Inspect(stored *v1.StoredModel) *v1.ChatTemplate {
	t, err := Read(stored)
	if err != nil {
		return &v1.ChatTemplate{Error: err.Error()}
	}
	out := &v1.ChatTemplate{Source: t.Source, Tools: v1.ToolSupport_TOOL_SUPPORT_EMULATED}
	if t.Tools() {
		out.Tools = v1.ToolSupport_TOOL_SUPPORT_NATIVE
	}
	return out
}

// Reads the template from the GGUF header or the files transformers keeps beside the weights
func Read(stored *v1.StoredModel) (*Template, error) {
	if stored.GetFormatId() == (gguf.Format{}).ID() {
		return readGGUF(stored)
	}
	for _, name := range fileNames {
		sa := shallowest(stored, name)
		if sa == nil {
			continue
		}
		text, err := readFile(name, sa.GetPath())
		if err != nil {
			return nil, err
		}
		if text != "" {
			return &Template{Text: text, Source: name}, nil
		}
	}
	return nil, errors.New("no chat template among the model's files")
}

// The artifact named so nearest the root, nil when none is
func shallowest(stored *v1.StoredModel, name string) *v1.StoredArtifact {
	var best *v1.StoredArtifact
	depth := -1
	for _, sa := range stored.GetArtifacts() {
		rel := sa.GetArtifact().GetPath()
		if path.Base(rel) != name {
			continue
		}
		if d := strings.Count(rel, "/"); best == nil || d < depth {
			best, depth = sa, d
		}
	}
	return best
}

// The template a file holds, jinja as it is and JSON by its chat_template field
func readFile(name, p string) (string, error) {
	raw, err := readCapped(p)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if strings.HasSuffix(name, ".jinja") {
		return string(raw), nil
	}
	var cfg struct {
		ChatTemplate json.RawMessage `json:"chat_template"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return templateField(name, cfg.ChatTemplate)
}

// The template a chat_template field names, a string or a list of named templates whose default is taken
func templateField(name string, raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var named []struct {
		Name     string `json:"name"`
		Template string `json:"template"`
	}
	if err := json.Unmarshal(raw, &named); err != nil {
		return "", fmt.Errorf("%s: chat_template is neither a string nor a list of named templates", name)
	}
	for _, n := range named {
		if n.Name == "default" {
			return n.Template, nil
		}
	}
	if len(named) > 0 {
		return named[0].Template, nil
	}
	return "", nil
}

// The template the first weight shard's header names
func readGGUF(stored *v1.StoredModel) (*Template, error) {
	var head *v1.StoredArtifact
	for _, sa := range stored.GetArtifacts() {
		a := sa.GetArtifact()
		if a.GetRole() != v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS {
			continue
		}
		if head == nil || a.GetShardIndex() < head.GetArtifact().GetShardIndex() {
			head = sa
		}
	}
	if head == nil {
		return nil, errors.New("the model has no GGUF weights to read a template from")
	}
	name := path.Base(head.GetArtifact().GetPath())
	f, err := os.Open(head.GetPath())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	meta, _, err := gguf.ReadHeader(f, info.Size())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	text := meta[ggufKey]
	if text == "" {
		return nil, fmt.Errorf("%s names no %s", name, ggufKey)
	}
	return &Template{Text: text, Source: ggufKey, ToolUse: meta[ggufToolUseKey]}, nil
}

// Reads a file up to the template size limit
func readCapped(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxFile {
		return nil, fmt.Errorf("larger than %d bytes", maxFile)
	}
	return raw, nil
}
