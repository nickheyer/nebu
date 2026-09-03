package runtime

import (
	"fmt"
	"strings"

	"github.com/nickheyer/nebu/pkg/eval"
)

// Everything a launch template can reference
type RenderInput struct {
	Name      string
	Params    map[string]any
	Artifacts map[string]string
	Host      string
	Port      int
	Install   map[string]string
}

// Rendered command line, environment, and the param values that were emitted
type Rendered struct {
	Command string
	Args    []string
	Env     map[string]string
	Params  map[string]string
}

func (in RenderInput) context() map[string]any {
	return map[string]any{
		"name":      in.Name,
		"params":    in.Params,
		"artifacts": in.Artifacts,
		"host":      in.Host,
		"port":      in.Port,
		"install":   in.Install,
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
		out.Args = append(out.Args, arg)
	}
	for k, t := range rt.launchEnv {
		v, err := t.Render(ctx)
		if err != nil {
			return nil, err
		}
		out.Env[k] = v
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

// Formats a param value, rendering string templates, and says whether to emit it
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
