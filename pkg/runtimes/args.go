package runtimes

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The prefix of a param that sets an environment variable of the server process, env.NAME setting NAME. Any runtime
// takes one, beside the params it declares
const EnvPrefix = "env."

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// The environment variable a param sets, when its name is env.NAME
func envParam(name string) (string, bool) {
	variable, named := strings.CutPrefix(name, EnvPrefix)
	return variable, named
}

// Checks a param naming an environment variable: the name is one a process can hold, and the value parses as a template
func checkEnv(variable, value string) error {
	if !envName.MatchString(variable) {
		return fmt.Errorf("%w: %q is no name for an environment variable", ErrParam, variable)
	}
	_, err := argTemplate(value)
	return err
}

// What a slot's argument templates read about a launch
type ArgScope struct {
	// The instance's name
	Name string
	// The address and port the server binds
	Host string
	Port int
	// The weights file, or the weights directory of a format served from one
	Model string
	// Every stored path by role, weights, weights_dir, mmproj and the rest
	Artifacts map[string]string
	// Every param the command carries, as text
	Params map[string]string
	// The ids of the devices the run is pinned to
	Devices []string
	Install Install
	// The seat in a formation, nil for a solo run
	Seat *v1.SeatSpec
}

// Functions argument templates may call
var argFuncs = template.FuncMap{"env": os.Getenv, "join": strings.Join}

// The scope a launch and its rendered command give argument templates
func ScopeOf(in Launch, cmd *Command) ArgScope {
	params := make(map[string]string, len(cmd.Params))
	for name, value := range cmd.Params {
		params[name] = value
	}
	devices := make([]string, 0, len(cmd.Devices))
	for _, d := range cmd.Devices {
		devices = append(devices, d.GetId())
	}
	model := in.Artifacts["weights"]
	if model == "" {
		model = in.Artifacts["weights_dir"]
	}
	port := in.Port
	if cmd.Port != 0 {
		port = cmd.Port
	}
	return ArgScope{Name: in.Name, Host: in.Host, Port: port, Model: model, Artifacts: in.Artifacts, Params: params, Devices: devices, Install: in.Install, Seat: in.Seat}
}

// Adds to a rendered command what a launch sets by hand: its extra words, one argument each, and the environment
// variables its params name, which win over the ones the runtime set. Each is a template over the launch
func Extend(in Launch, cmd *Command, args string) error {
	scope := ScopeOf(in, cmd)
	extra, err := RenderArgs(args, scope)
	if err != nil {
		return err
	}
	cmd.Args = append(cmd.Args, extra...)
	var names []string
	for name := range in.Params {
		if _, named := envParam(name); named && in.Params.Str(name) != "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		variable, _ := envParam(name)
		t, err := argTemplate(in.Params.Str(name))
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, scope); err != nil {
			return fmt.Errorf("%w: environment variable %s: %v", ErrParam, variable, err)
		}
		if cmd.Env == nil {
			cmd.Env = map[string]string{}
		}
		if cmd.Params == nil {
			cmd.Params = map[string]string{}
		}
		cmd.Env[variable], cmd.Params[name] = buf.String(), buf.String()
	}
	return nil
}

// The extra words a run launches with, its own when it names any and its slot's otherwise
func RunArgs(slot, run string) string {
	if run = strings.TrimSpace(run); run != "" {
		return run
	}
	return strings.TrimSpace(slot)
}

// Checks that extra words split by shell quoting and parse as templates
func CheckArgs(args string) error {
	words, err := splitWords(args)
	if err != nil {
		return err
	}
	for _, w := range words {
		if _, err := argTemplate(w); err != nil {
			return err
		}
	}
	return nil
}

// Renders extra words over the scope, one argument per word
func RenderArgs(args string, scope ArgScope) ([]string, error) {
	words, err := splitWords(args)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(words))
	for _, w := range words {
		t, err := argTemplate(w)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, scope); err != nil {
			return nil, fmt.Errorf("%w: argument %q: %v", ErrParam, w, err)
		}
		out = append(out, buf.String())
	}
	return out, nil
}

// One word parsed as a template, unknown fields, keys, and functions refused
func argTemplate(word string) (*template.Template, error) {
	t, err := template.New("argument").Funcs(argFuncs).Option("missingkey=error").Parse(word)
	if err != nil {
		return nil, fmt.Errorf("%w: argument %q: %v", ErrParam, word, strings.TrimPrefix(err.Error(), "template: argument:1: "))
	}
	return t, nil
}

// Splits a command line into words by shell quoting, each template action kept whole
func splitWords(s string) ([]string, error) {
	var words []string
	var word strings.Builder
	open := false
	flush := func() {
		if open {
			words = append(words, word.String())
			word.Reset()
			open = false
		}
	}
	var quote rune
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case strings.HasPrefix(s[i:], "{{"):
			end := strings.Index(s[i+2:], "}}")
			if end < 0 {
				return nil, fmt.Errorf("%w: arguments %q: a template action is not closed", ErrParam, s)
			}
			word.WriteString(s[i : i+end+4])
			open = true
			i += end + 4
			continue
		case quote != 0:
			switch {
			case r == quote:
				quote = 0
			case r == '\\' && quote == '"' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\'):
				word.WriteByte(s[i+1])
				i += 2
				continue
			default:
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, open = r, true
		case r == '\\':
			if i+size >= len(s) {
				return nil, fmt.Errorf("%w: arguments %q end in a backslash", ErrParam, s)
			}
			next, n := utf8.DecodeRuneInString(s[i+size:])
			word.WriteRune(next)
			open = true
			i += size + n
			continue
		case unicode.IsSpace(r):
			flush()
		default:
			word.WriteRune(r)
			open = true
		}
		i += size
	}
	if quote != 0 {
		return nil, fmt.Errorf("%w: arguments %q: a quote is not closed", ErrParam, s)
	}
	flush()
	return words, nil
}
