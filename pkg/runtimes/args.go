package runtimes

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

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
	// The ids of the devices the run may use
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
	devices := make([]string, 0, len(in.Devices))
	for _, d := range in.Devices {
		devices = append(devices, d.GetId())
	}
	model := in.Artifacts["weights"]
	if model == "" {
		model = in.Artifacts["weights_dir"]
	}
	return ArgScope{Name: in.Name, Host: in.Host, Port: in.Port, Model: model, Artifacts: in.Artifacts, Params: params, Devices: devices, Install: in.Install, Seat: in.Seat}
}

// Joins a slot's extra words and a run's, the run's last so a repeated flag takes its value
func JoinArgs(slot, run string) string {
	return strings.TrimSpace(strings.TrimSpace(slot) + " " + strings.TrimSpace(run))
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
