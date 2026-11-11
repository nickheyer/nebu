package instances

import (
	"fmt"

	"github.com/nickheyer/nebu/internal/installs"
	"github.com/nickheyer/nebu/pkg/chattemplate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

// How the instance's chat template offers tools, unspecified for an instance this node does not run
func (m *Manager) ToolSupport(id string) v1.ToolSupport {
	in, err := m.find(id)
	if err != nil {
		return v1.ToolSupport_TOOL_SUPPORT_UNSPECIFIED
	}
	return in.snapshot().GetTemplate().GetTools()
}

// Whether the instance's launch leaves Claude Code's billing header out of prompts, true for an instance this node does not run
func (m *Manager) StripsBillingHeader(id string) bool {
	in, err := m.find(id)
	if err != nil {
		return true
	}
	return in.snapshot().GetParams()[runtimes.ParamStripBillingHeader] != "false"
}

// The generation modes a diffusion instance reported at ready, none for any other or an unknown one
func (m *Manager) Modes(id string) []string {
	in, err := m.find(id)
	if err != nil {
		return nil
	}
	return in.modesOf()
}

// The chat template read from the instance's stored model files
func (m *Manager) Template(id string) (*chattemplate.Template, error) {
	in, err := m.find(id)
	if err != nil {
		return nil, err
	}
	if m.Store == nil {
		return nil, fmt.Errorf("no store holds the model files of %s", id)
	}
	rec := in.snapshot()
	stored, err := m.Store.ReadManifest(rec.GetSourceId(), rec.GetRepo(), rec.GetGroup())
	if err != nil {
		return nil, fmt.Errorf("manifest of %s: %w", rec.GetName(), err)
	}
	return chattemplate.Read(stored)
}

// The path of the tokenizer.json among the instance's stored model files
func (m *Manager) TokenizerFile(id string) (string, error) {
	in, err := m.find(id)
	if err != nil {
		return "", err
	}
	if m.Store == nil {
		return "", fmt.Errorf("no store holds the model files of %s", id)
	}
	rec := in.snapshot()
	stored, err := m.Store.ReadManifest(rec.GetSourceId(), rec.GetRepo(), rec.GetGroup())
	if err != nil {
		return "", fmt.Errorf("manifest of %s: %w", rec.GetName(), err)
	}
	return installs.TokenizerFile(stored)
}
