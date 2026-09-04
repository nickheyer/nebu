package runtime

import (
	"fmt"
	"strings"

	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Everything a launch template can reference
type RenderInput struct {
	Name       string
	Params     map[string]any
	Artifacts  map[string]string
	Host       string
	Port       int
	Install    map[string]string
	Devices    []map[string]any
	Descriptor *v1.Descriptor
}

// Rendered command line, environment, and the emitted param values
type Rendered struct {
	Command string
	Args    []string
	Env     map[string]string
	Params  map[string]string
}

func (in RenderInput) context() map[string]any {
	return map[string]any{
		"name":       in.Name,
		"params":     in.Params,
		"artifacts":  in.Artifacts,
		"host":       in.Host,
		"port":       in.Port,
		"install":    in.Install,
		"devices":    in.Devices,
		"descriptor": DescriptorView(in.Descriptor),
	}
}

// Flattens a descriptor into the shape launch templates read
//
// Params and metadata are maps so a template can ask for a key that may be
// absent through index, a missing metadata key rendering empty and dropped.
// Always present, empty without a descriptor, so templates never trip missingkey on it.
func DescriptorView(d *v1.Descriptor) map[string]any {
	params := make(map[string]any, len(d.GetParams()))
	for k, v := range d.GetParams() {
		params[k] = v
	}
	metadata := make(map[string]string, len(d.GetMetadata()))
	for k, v := range d.GetMetadata() {
		metadata[k] = v
	}
	return map[string]any{
		"format_id":       d.GetFormatId(),
		"group":           d.GetGroup(),
		"architecture":    d.GetArchitecture(),
		"arch_spec_id":    d.GetArchSpecId(),
		"params":          params,
		"metadata":        metadata,
		"total_bytes":     float64(d.GetTotalBytes()),
		"parameter_count": float64(d.GetParameterCount()),
	}
}

// Renders the launch command, flags for every param, and env
func (rt *Runtime) Render(in RenderInput) (*Rendered, error) {
	ctx := in.context()
	command, err := rt.launchCommand.Render(ctx)
	if err != nil {
		return nil, err
	}
	out := &Rendered{Command: command, Env: map[string]string{}, Params: map[string]string{}}
	for _, t := range rt.launchArgs {
		arg, err := t.Render(ctx)
		if err != nil {
			return nil, err
		}
		// An argument that renders empty does not apply, the same rule recipe steps follow
		if arg = strings.TrimSpace(arg); arg != "" {
			out.Args = append(out.Args, arg)
		}
	}
	for k, t := range rt.launchEnv {
		v, err := t.Render(ctx)
		if err != nil {
			return nil, err
		}
		// Empty renders mean the variable does not apply
		if v = strings.TrimSpace(v); v != "" {
			out.Env[k] = v
		}
	}
	for _, p := range rt.Manifest.GetParams() {
		value, ok := in.Params[p.GetName()]
		if !ok {
			continue
		}
		text, keep, err := renderValue(value, p.GetSolved(), ctx)
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", p.GetName(), err)
		}
		if !keep {
			continue
		}
		out.Params[p.GetName()] = text
		if p.GetEnv() != "" {
			out.Env[p.GetEnv()] = text
		}
		if p.GetFlag() == "" {
			continue
		}
		if b, isBool := value.(bool); isBool {
			if b {
				out.Args = append(out.Args, p.GetFlag())
			}
			continue
		}
		if strings.HasSuffix(p.GetFlag(), "=") {
			out.Args = append(out.Args, p.GetFlag()+text)
		} else {
			out.Args = append(out.Args, p.GetFlag(), text)
		}
	}
	return out, nil
}

// Formats a param value and says whether to emit it
func renderValue(value any, solved bool, ctx map[string]any) (string, bool, error) {
	switch v := value.(type) {
	case nil:
		return "", false, nil
	case string:
		if solved && v == Auto {
			return "", false, nil
		}
		if strings.Contains(v, "{{") {
			t, err := eval.CompileTemplate(v)
			if err != nil {
				return "", false, err
			}
			rendered, err := t.Render(ctx)
			if err != nil {
				return "", false, err
			}
			v = strings.TrimSpace(rendered)
		}
		return v, v != "", nil
	case bool:
		return fmt.Sprint(v), true, nil
	}
	return fmt.Sprint(value), true, nil
}

// Renders the prepare command a manifest declares for a stored group
func (rt *Runtime) RenderPrepare(in RenderInput) (*Rendered, error) {
	if rt.prepareCommand == nil {
		return nil, nil
	}
	ctx := in.context()
	command, err := rt.prepareCommand.Render(ctx)
	if err != nil {
		return nil, err
	}
	out := &Rendered{Command: strings.TrimSpace(command), Env: map[string]string{}, Params: map[string]string{}}
	for _, t := range rt.prepareArgs {
		arg, err := t.Render(ctx)
		if err != nil {
			return nil, err
		}
		if arg = strings.TrimSpace(arg); arg != "" {
			out.Args = append(out.Args, arg)
		}
	}
	for k, t := range rt.prepareEnv {
		v, err := t.Render(ctx)
		if err != nil {
			return nil, err
		}
		if v = strings.TrimSpace(v); v != "" {
			out.Env[k] = v
		}
	}
	return out, nil
}

// Renders the command a probe runs, the install itself when the probe names none
func (rt *Runtime) ProbeCommand(p Probe, install map[string]string) (string, error) {
	if p.command == nil {
		return install["path"], nil
	}
	out, err := p.command.Render(map[string]any{"install": install})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
