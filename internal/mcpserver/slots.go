package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

type aliasIn struct {
	Name          string `json:"name" jsonschema:"extra model name for the slot"`
	InheritSlotID string `json:"inherit_slot_id,omitempty" jsonschema:"slot id to take limits and tools from"`
}

type slotIn struct {
	Description   *string           `json:"description,omitempty"`
	RuntimeID     *string           `json:"runtime_id,omitempty" jsonschema:"runtime for every launch in the slot"`
	InstallID     *string           `json:"install_id,omitempty" jsonschema:"install for every launch in the slot"`
	Params        map[string]string `json:"params,omitempty" jsonschema:"runtime parameters for every launch in the slot, such as n_ctx, and environment variables of the server process as env.NAME"`
	Args          *string           `json:"args,omitempty" jsonschema:"extra arguments for the runtime's server command"`
	Placement     string            `json:"placement,omitempty" jsonschema:"auto, device, or host"`
	DeviceIDs     []string          `json:"device_ids,omitempty" jsonschema:"devices the slot may use"`
	PoolBytes     map[string]uint64 `json:"pool_bytes,omitempty" jsonschema:"byte limit per memory pool, by pool id"`
	Policy        *policyIn         `json:"policy,omitempty"`
	ToolServers   []string          `json:"tool_servers,omitempty" jsonschema:"tool servers for requests on the slot's route"`
	MutatingTools *bool             `json:"mutating_tools,omitempty"`
	Aliases       []aliasIn         `json:"aliases,omitempty"`
}

type createSlotIn struct {
	Name string `json:"name" jsonschema:"model name clients send"`
	slotIn
}

type updateSlotIn struct {
	ID   string `json:"id" jsonschema:"slot id or name"`
	Name string `json:"name,omitempty" jsonschema:"new name"`
	slotIn
}

type deleteSlotIn struct {
	ID    string `json:"id" jsonschema:"slot id or name"`
	Force bool   `json:"force,omitempty" jsonschema:"stop the running model first"`
}

// The placement a name stands for, the one held when empty
func placementOf(name string, held v1.Placement) (v1.Placement, error) {
	switch strings.ToLower(name) {
	case "":
		return held, nil
	case "auto":
		return v1.Placement_PLACEMENT_UNSPECIFIED, nil
	case "device":
		return v1.Placement_PLACEMENT_DEVICE, nil
	case "host":
		return v1.Placement_PLACEMENT_HOST, nil
	}
	return 0, fmt.Errorf("placement must be auto, device, or host, not %q", name)
}

// Lays the settings a call names over a slot's, each one it leaves out staying as it is
func (in slotIn) over(s *v1.Slot) error {
	placement, err := placementOf(in.Placement, s.GetPlacement())
	if err != nil {
		return err
	}
	s.Placement = placement
	if in.Description != nil {
		s.Description = *in.Description
	}
	if in.RuntimeID != nil {
		s.RuntimeId = *in.RuntimeID
	}
	if in.InstallID != nil {
		s.InstallId = *in.InstallID
	}
	if in.Params != nil {
		s.Params = in.Params
	}
	if in.Args != nil {
		s.Args = *in.Args
	}
	if in.DeviceIDs != nil {
		s.DeviceIds = in.DeviceIDs
	}
	if in.PoolBytes != nil {
		s.PoolBytes = in.PoolBytes
	}
	if p := in.Policy; p != nil {
		s.Policy = &v1.Policy{MaxInFlight: p.MaxInFlight, RequestsPerSecond: p.RequestsPerSecond, Burst: p.Burst, RequestTimeoutMs: p.RequestTimeoutMs, UpstreamTimeoutMs: p.UpstreamTimeoutMs}
	}
	if in.ToolServers != nil || in.MutatingTools != nil {
		profile := &v1.Profile{ToolServers: s.GetProfile().GetToolServers(), MutatingTools: s.GetProfile().GetMutatingTools()}
		if in.ToolServers != nil {
			profile.ToolServers = in.ToolServers
		}
		if in.MutatingTools != nil {
			profile.MutatingTools = *in.MutatingTools
		}
		s.Profile = profile
	}
	if in.Aliases != nil {
		s.Aliases = nil
		for _, a := range in.Aliases {
			s.Aliases = append(s.Aliases, &v1.SlotAlias{Name: a.Name, InheritSlotId: a.InheritSlotID})
		}
	}
	return nil
}

// Adds the tools creating, changing and deleting slots
func (n *nebu) addSlotTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "create_slot", Description: "Creates an empty slot, a named reservation of devices and memory"}, func(ctx context.Context, _ *mcp.CallToolRequest, in createSlotIn) (*mcp.CallToolResult, any, error) {
		slot := &v1.Slot{}
		if err := in.over(slot); err != nil {
			return nil, nil, err
		}
		req, err := checked(&v1.CreateSlotRequest{Name: in.Name, Description: slot.GetDescription(), DeviceIds: slot.GetDeviceIds(), RuntimeId: slot.GetRuntimeId(), InstallId: slot.GetInstallId(), Params: slot.GetParams(), Args: slot.GetArgs(), Placement: slot.GetPlacement(), PoolBytes: slot.GetPoolBytes(), Policy: slot.GetPolicy(), Profile: slot.GetProfile(), Aliases: slot.GetAliases()})
		if err != nil {
			return nil, nil, err
		}
		r, err := n.slots.CreateSlot(ctx, req)
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "update_slot", Description: "Updates the slot settings you pass. Launch settings apply on the next launch"}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateSlotIn) (*mcp.CallToolResult, any, error) {
		cur, err := n.slots.GetSlot(ctx, connect.NewRequest(&v1.GetSlotRequest{Id: in.ID}))
		if err != nil {
			return nil, nil, unwrap(err)
		}
		slot := proto.Clone(cur.Msg.GetSlot()).(*v1.Slot)
		if err := in.over(slot); err != nil {
			return nil, nil, err
		}
		req, err := checked(&v1.UpdateSlotRequest{Id: slot.GetId(), Name: in.Name, Description: slot.GetDescription(), DeviceIds: slot.GetDeviceIds(), MemoryBytes: slot.GetMemoryBytes(), RuntimeId: slot.GetRuntimeId(), InstallId: slot.GetInstallId(), Params: slot.GetParams(), Args: slot.GetArgs(), Placement: slot.GetPlacement(), PoolBytes: slot.GetPoolBytes(), Policy: slot.GetPolicy(), Profile: slot.GetProfile(), Aliases: slot.GetAliases()})
		if err != nil {
			return nil, nil, err
		}
		r, err := n.slots.UpdateSlot(ctx, req)
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "relaunch_slot", Description: "Runs a slot's model again after it failed or stopped, and returns the slot, instance, and task"}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.slots.RelaunchSlot(ctx, connect.NewRequest(&v1.RelaunchSlotRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "delete_slot", Description: "Deletes a slot and its routes", Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true)}}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteSlotIn) (*mcp.CallToolResult, any, error) {
		r, err := n.slots.DeleteSlot(ctx, connect.NewRequest(&v1.DeleteSlotRequest{Id: in.ID, Force: in.Force}))
		return answer(r, err)
	})
}
