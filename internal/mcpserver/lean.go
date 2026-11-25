package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nickheyer/nebu/internal/instances"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	// Files a stored model lists before its directory and size stand for them
	leanFiles = 8
	// Characters a descriptor's metadata value keeps
	leanValue = 200
	// Instances that have ended a list names before it counts the rest
	leanEnded = 5
	// Lines from the end of a log a tool answers with when a call names no count
	tailDefault = 200
)

// A text cut to its first n bytes with the rest counted, whole when no longer
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return fmt.Sprintf("%s… (%d more characters)", strings.ToValidUTF8(s[:n], ""), len(s)-n)
}

// What a tool that trims its answer takes to answer whole
type fullIn struct {
	Full bool `json:"full,omitempty" jsonschema:"answer every field, which is long"`
}

// Answers a service's message as a tool result, trimmed to what a reader acts on unless full, its error as the tool's
func (n *nebu) as(full bool) func(proto.Message, error) (*mcp.CallToolResult, any, error) {
	return func(msg proto.Message, err error) (*mcp.CallToolResult, any, error) {
		if err != nil {
			return nil, nil, unwrap(err)
		}
		if !full {
			msg = n.lean(msg)
		}
		return text(msg), nil, nil
	}
}

// A copy of a message without what its records repeat, derive, or hold for the UI: conditions, generations, the request a record was made from, a plan's untouched params, tensor tables, digests, and download addresses
func (n *nebu) lean(msg proto.Message) proto.Message {
	out := proto.Clone(msg)
	n.trim(out.ProtoReflect())
	return out
}

func (n *nebu) trim(m protoreflect.Message) {
	switch v := m.Interface().(type) {
	case *v1.Instance:
		n.trimInstance(v)
	case *v1.FitRow:
		n.trimPlan(v.GetPlan(), v.GetRuntimeId())
		n.trimPlan(v.GetFree(), v.GetRuntimeId())
	case *v1.Slot:
		v.Conditions, v.Meta, v.ObservedGeneration = nil, nil, 0
	case *v1.StoredModel:
		trimStored(v)
	case *v1.Descriptor:
		v.Groups = nil
		for k, val := range v.GetMetadata() {
			v.Metadata[k] = clip(val, leanValue)
		}
	case *v1.Artifact:
		v.Url, v.Sha256 = "", ""
	case *v1.Install:
		v.Status, v.Generation = nil, 0
	case *v1.Runtime:
		v.Params = nil
	case *v1.Device:
		delete(v.GetFacts(), "flags")
	case *v1.HostProfile:
		v.Probes = nil
	}
	m.Range(func(fd protoreflect.FieldDescriptor, val protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			if fd.MapValue().Message() != nil {
				val.Map().Range(func(_ protoreflect.MapKey, item protoreflect.Value) bool {
					n.trim(item.Message())
					return true
				})
			}
		case fd.Message() == nil:
		case fd.IsList():
			for i := range val.List().Len() {
				n.trim(val.List().Get(i).Message())
			}
		default:
			n.trim(val.Message())
		}
		return true
	})
}

// An instance as a reader acts on it: what runs, with what command and settings, in what state, what its plan placed, and what its log said
func (n *nebu) trimInstance(in *v1.Instance) {
	in.Conditions, in.Request, in.Template, in.LastHealthAt = nil, nil, nil, nil
	in.Generation, in.ObservedGeneration = 0, 0
	if len(in.GetRanks()) < 2 {
		in.Ranks = nil
	}
	n.trimPlan(in.GetPlan(), in.GetRuntimeId())
	for _, ms := range in.GetMeasurements() {
		if !strings.HasSuffix(ms.GetKey(), ".failed") {
			ms.Line = ""
		}
	}
}

// A plan with the params it moved off the runtime's defaults alone, every param of the runtime otherwise riding along
func (n *nebu) trimPlan(p *v1.MemoryPlan, runtimeID string) {
	if p == nil {
		return
	}
	p.PlannedAt = nil
	defaults := n.defaultsOf(runtimeID)
	for name, value := range p.GetParams() {
		standard := defaults[name]
		if value == "" || value == standard || standard == "" && value == "false" {
			delete(p.Params, name)
		}
	}
}

// The default of every param of a runtime by name, built once since a runtime's params are fixed
func (n *nebu) defaultsOf(runtimeID string) map[string]string {
	if kept, ok := n.defaults.Load(runtimeID); ok {
		return kept.(map[string]string)
	}
	defaults := map[string]string{}
	if rt, err := n.d.Runtimes.Get(runtimeID); err == nil {
		for _, param := range rt.Params() {
			defaults[param.GetName()] = param.GetDefault()
		}
	}
	n.defaults.Store(runtimeID, defaults)
	return defaults
}

// A stored model with its files by name, size, and place, or its directory and size alone past a handful of files
func trimStored(m *v1.StoredModel) {
	if len(m.GetArtifacts()) > leanFiles {
		m.Artifacts = nil
		return
	}
	for _, a := range m.GetArtifacts() {
		a.Digest = ""
		if art := a.GetArtifact(); art != nil {
			a.Artifact = &v1.Artifact{SizeBytes: art.GetSizeBytes(), Role: art.GetRole()}
		}
	}
}

// The instances a list answers with: every one still wanted in full, then the newest ended ones by what they ran and how they ended, the rest counted
func listed(all []*v1.Instance, every bool) ([]*v1.Instance, int) {
	var out, ended []*v1.Instance
	for _, in := range all {
		if every || !instances.Terminal(in.GetState()) {
			out = append(out, in)
			continue
		}
		ended = append(ended, in)
	}
	// The list is newest first, so its head holds the latest to end
	left := max(len(ended)-leanEnded, 0)
	for _, in := range ended[:len(ended)-left] {
		out = append(out, &v1.Instance{
			Id: in.GetId(), Name: in.GetName(), SourceId: in.GetSourceId(), Repo: in.GetRepo(), Group: in.GetGroup(), RuntimeId: in.GetRuntimeId(), InstallId: in.GetInstallId(),
			State: in.GetState(), Error: in.GetError(), SlotId: in.GetSlotId(), CreatedAt: in.GetCreatedAt(), StoppedAt: in.GetStoppedAt(),
		})
	}
	return out, left
}

// A tool answer of named parts, each a protobuf message, a string, a number, or a list of them
func parts(pairs ...any) *mcp.CallToolResult {
	var b bytes.Buffer
	b.WriteByte('{')
	wrote := false
	for i := 0; i+1 < len(pairs); i += 2 {
		var data []byte
		switch v := pairs[i+1].(type) {
		case nil:
			continue
		case proto.Message:
			if v == nil || !v.ProtoReflect().IsValid() {
				continue
			}
			data = protoJSON(v)
		default:
			var plain bytes.Buffer
			enc := json.NewEncoder(&plain)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(v); err != nil {
				continue
			}
			data = bytes.TrimSpace(plain.Bytes())
		}
		if wrote {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(pairs[i].(string))
		b.Write(key)
		b.WriteByte(':')
		b.Write(data)
		wrote = true
	}
	b.WriteByte('}')
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: b.String()}}}
}
