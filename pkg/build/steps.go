package build

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/recipes"
)

// The shell a step's script runs under
const shell = "sh"

// One piece of a step as a template over the build, a variable the build has none of rendering empty
func render(text string, b *recipes.Build) (string, error) {
	if !strings.Contains(text, "{{") {
		return text, nil
	}
	t, err := template.New("step").Option("missingkey=zero").Parse(text)
	if err != nil {
		return "", fmt.Errorf("%q: %s", text, strings.TrimPrefix(err.Error(), "template: step:"))
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, b); err != nil {
		return "", fmt.Errorf("%q: %s", text, strings.TrimPrefix(err.Error(), "template: step:"))
	}
	return buf.String(), nil
}

// Renders every value of an environment over the build
func renderEnv(env map[string]string, b *recipes.Build) (map[string]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(env))
	for name, value := range env {
		text, err := render(value, b)
		if err != nil {
			return nil, fmt.Errorf("%w: environment variable %s: %v", ErrSelection, name, err)
		}
		out[name] = text
	}
	return out, nil
}

// What a step is called in the log and in errors, its place in the order when it has no name
func stepName(st *v1.BuildStep, i int) string {
	if name := strings.TrimSpace(st.GetName()); name != "" {
		return name
	}
	return fmt.Sprintf("step %d", i+1)
}

// Renders a request's steps over the build. A script runs under sh, and a word of a command whose template comes out
// empty is left out, as a recipe leaves out the flag of a variable nothing set
func renderSteps(steps []*v1.BuildStep, b *recipes.Build) ([]recipes.Step, error) {
	out := make([]recipes.Step, 0, len(steps))
	for i, st := range steps {
		name := stepName(st, i)
		step := recipes.Step{Name: name, Dir: st.GetDir()}
		if strings.TrimSpace(st.GetScript()) != "" {
			text, err := render(st.GetScript(), b)
			if err != nil {
				return nil, fmt.Errorf("%w: step %s: %v", ErrSelection, name, err)
			}
			step.Command = []string{shell, "-c", text}
		}
		for _, word := range st.GetCommand() {
			text, err := render(word, b)
			if err != nil {
				return nil, fmt.Errorf("%w: step %s: %v", ErrSelection, name, err)
			}
			if text == "" && strings.Contains(word, "{{") {
				continue
			}
			step.Command = append(step.Command, text)
		}
		env, err := renderEnv(st.GetEnv(), b)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", name, err)
		}
		step.Env = env
		out = append(out, step)
	}
	return out, nil
}

// Checks a request's steps and environment before any work starts: each step is a command or a script and never both,
// and every template renders
func checkSteps(steps []*v1.BuildStep, env map[string]string) error {
	probe := &recipes.Build{Vars: map[string]string{}}
	if _, err := renderEnv(env, probe); err != nil {
		return err
	}
	for i, st := range steps {
		script, words := strings.TrimSpace(st.GetScript()) != "", len(st.GetCommand()) > 0
		if script == words {
			return fmt.Errorf("%w: step %s needs a command or a script, one of the two", ErrSelection, stepName(st, i))
		}
	}
	_, err := renderSteps(steps, probe)
	return err
}
