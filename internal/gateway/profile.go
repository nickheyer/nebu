package gateway

import (
	"errors"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Refuses a profile that offers mutating tools with no tool server to take them from
func CheckProfile(p *v1.Profile) error {
	if p.GetMutatingTools() && len(p.GetToolServers()) == 0 {
		return errors.New("mutating tools need at least one tool server")
	}
	return nil
}

// Reports whether a profile sets anything, an empty one inheriting everything
func ProfileSet(p *v1.Profile) bool {
	return len(p.GetToolServers()) > 0 || p.GetMutatingTools()
}

// Merges an alias profile over its slot's, every alias field that is set winning
func EffectiveProfile(alias, slot *v1.Profile) *v1.Profile {
	if !ProfileSet(alias) {
		return slot
	}
	out := &v1.Profile{ToolServers: append([]string(nil), slot.GetToolServers()...), MutatingTools: slot.GetMutatingTools()}
	if len(alias.GetToolServers()) > 0 {
		out.ToolServers, out.MutatingTools = append([]string(nil), alias.GetToolServers()...), alias.GetMutatingTools()
	}
	return out
}
