package chattemplate

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Writes a GGUF header holding the given string metadata
func ggufFile(t *testing.T, dir, name string, meta map[string]string) string {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("GGUF")
	binary.Write(&b, binary.LittleEndian, uint32(3))
	binary.Write(&b, binary.LittleEndian, uint64(0))
	binary.Write(&b, binary.LittleEndian, uint64(len(meta)))
	str := func(s string) {
		binary.Write(&b, binary.LittleEndian, uint64(len(s)))
		b.WriteString(s)
	}
	for k, v := range meta {
		str(k)
		binary.Write(&b, binary.LittleEndian, uint32(8))
		str(v)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func stored(format string, files ...*v1.StoredArtifact) *v1.StoredModel {
	return &v1.StoredModel{FormatId: format, Group: "g", Artifacts: files}
}

func file(t *testing.T, dir, rel, content string, role v1.ArtifactRole) *v1.StoredArtifact {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return &v1.StoredArtifact{Path: p, Artifact: &v1.Artifact{Path: rel, Role: role}}
}

const qwen = `{%- if tools %}{{- '<|im_start|>system\n' }}{%- for tool in tools %}{{ tool | tojson }}{%- endfor %}{%- endif %}{%- for message in messages %}{{ message.content }}{%- endfor %}`

// A variable read counts, a mention inside a string or a comment or as part of another name does not
func TestToolsVariable(t *testing.T) {
	for text, want := range map[string]bool{
		qwen: true,
		`{% if tools is defined and tools is not none %}x{% endif %}`: true,
		`{{ tools|length }}`:                                         true,
		`{{ "# Tools" }}{{ 'use the tools' }}`:                       false,
		`{# tools go here #}{{ messages }}`:                          false,
		`{{ message.tools }}{{ my_tools }}{{ toolset }}`:             false,
		`{% for call in message.tool_calls %}{{ call }}{% endfor %}`: false,
	} {
		if got := (&Template{Text: text}).Tools(); got != want {
			t.Errorf("%q: tools %v, want %v", text, got, want)
		}
	}
	if !(&Template{Text: "{{ messages }}", ToolUse: "{{ tools }}"}).Tools() {
		t.Fatal("a tool use variant renders tools")
	}
}

// The jinja file wins over the JSON ones, a named list yields its default, and a bare model has none
func TestReadFiles(t *testing.T) {
	dir := t.TempDir()
	role := v1.ArtifactRole_ARTIFACT_ROLE_TEMPLATE
	jinja := file(t, dir, "chat_template.jinja", qwen, role)
	js := file(t, dir, "chat_template.json", `{"chat_template": "{{ messages }}"}`, role)
	cfg := file(t, dir, "tokenizer_config.json", `{"chat_template": [{"name": "rag", "template": "{{ documents }}"}, {"name": "default", "template": "{{ tools }}"}]}`, v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER)
	got, err := Read(stored("safetensors", cfg, js, jinja))
	if err != nil || got.Source != "chat_template.jinja" || got.Text != qwen {
		t.Fatalf("jinja: %+v %v", got, err)
	}
	got, err = Read(stored("safetensors", cfg, js))
	if err != nil || got.Source != "chat_template.json" || got.Text != "{{ messages }}" {
		t.Fatalf("json: %+v %v", got, err)
	}
	got, err = Read(stored("safetensors", cfg))
	if err != nil || got.Source != "tokenizer_config.json" || got.Text != "{{ tools }}" {
		t.Fatalf("tokenizer config: %+v %v", got, err)
	}
	bare := file(t, dir, "bare/tokenizer_config.json", `{"model_max_length": 4096}`, v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER)
	if _, err := Read(stored("safetensors", bare)); err == nil || !strings.Contains(err.Error(), "no chat template") {
		t.Fatalf("bare: %v", err)
	}
	broken := file(t, dir, "broken/chat_template.json", `{"chat_template": 7}`, role)
	if _, err := Read(stored("safetensors", broken)); err == nil || !strings.Contains(err.Error(), "chat_template.json") {
		t.Fatalf("broken: %v", err)
	}
	if tpl := Inspect(stored("safetensors", js)); tpl.GetError() != "" || tpl.GetSource() != "chat_template.json" || tpl.GetTools() != v1.ToolSupport_TOOL_SUPPORT_EMULATED {
		t.Fatalf("inspect without tools: %v", tpl)
	}
	if tpl := Inspect(stored("safetensors", jinja)); tpl.GetTools() != v1.ToolSupport_TOOL_SUPPORT_NATIVE {
		t.Fatalf("inspect with tools: %v", tpl)
	}
	if tpl := Inspect(stored("safetensors", bare)); !strings.Contains(tpl.GetError(), "no chat template") || tpl.GetTools() != v1.ToolSupport_TOOL_SUPPORT_UNSPECIFIED {
		t.Fatalf("inspect bare: %v", tpl)
	}
}

// The file nearest the root wins when a tree holds several of a name
func TestReadPrefersRootFiles(t *testing.T) {
	dir := t.TempDir()
	role := v1.ArtifactRole_ARTIFACT_ROLE_TOKENIZER
	deep := file(t, dir, "text_encoder/tokenizer_config.json", `{"chat_template": "{{ deep }}"}`, role)
	root := file(t, dir, "tokenizer_config.json", `{"chat_template": "{{ root }}"}`, role)
	got, err := Read(stored("safetensors", deep, root))
	if err != nil || got.Text != "{{ root }}" {
		t.Fatalf("%+v %v", got, err)
	}
}

// A GGUF's first shard names the template, a tool use variant marking tools, and one without a template says so
func TestReadGGUF(t *testing.T) {
	dir := t.TempDir()
	second := ggufFile(t, dir, "m-00002-of-00002.gguf", map[string]string{"general.architecture": "qwen3"})
	first := ggufFile(t, dir, "m-00001-of-00002.gguf", map[string]string{ggufKey: "{{ messages }}", ggufToolUseKey: "{{ tools }}"})
	weights := func(p string, index uint32) *v1.StoredArtifact {
		return &v1.StoredArtifact{Path: p, Artifact: &v1.Artifact{Path: filepath.Base(p), Role: v1.ArtifactRole_ARTIFACT_ROLE_WEIGHTS, ShardIndex: index, ShardCount: 2}}
	}
	got, err := Read(stored("gguf", weights(second, 2), weights(first, 1)))
	if err != nil || got.Source != ggufKey || got.Text != "{{ messages }}" || got.ToolUse != "{{ tools }}" || !got.Tools() {
		t.Fatalf("%+v %v", got, err)
	}
	plain := ggufFile(t, dir, "plain.gguf", map[string]string{"general.architecture": "llama"})
	if _, err := Read(stored("gguf", weights(plain, 0))); err == nil || !strings.Contains(err.Error(), ggufKey) {
		t.Fatalf("plain: %v", err)
	}
	if _, err := Read(stored("gguf")); err == nil {
		t.Fatal("no weights")
	}
}
