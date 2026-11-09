package tools

import (
	"fmt"
	"maps"

	"github.com/nickheyer/nebu/internal/auth"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// Copies a server with its Brave key, env values, and header values as the marker, marking whether any was set
func Redact(s *v1.ToolServer) *v1.ToolServer {
	out := proto.Clone(s).(*v1.ToolServer)
	out.SecretsSet = false
	if out.BraveKey != "" {
		out.BraveKey, out.SecretsSet = auth.Redacted, true
	}
	for _, m := range []map[string]string{out.Env, out.Headers} {
		for k := range m {
			m[k], out.SecretsSet = auth.Redacted, true
		}
	}
	return out
}

// Copies servers through Redact
func RedactAll(list []*v1.ToolServer) []*v1.ToolServer {
	out := make([]*v1.ToolServer, len(list))
	for i, s := range list {
		out[i] = Redact(s)
	}
	return out
}

// Puts back stored secrets wherever an incoming server sends the marker, refusing a marker with nothing stored behind it
func Restore(incoming, stored []*v1.ToolServer) error {
	byID := map[string]*v1.ToolServer{}
	for _, s := range stored {
		byID[s.GetId()] = s
	}
	for _, s := range incoming {
		s.SecretsSet = false
		old := byID[s.GetId()]
		missing := func(what string) error {
			return fmt.Errorf("%w: %s sends the redaction marker for %s but holds no stored value there", ErrServer, s.GetName(), what)
		}
		if s.BraveKey == auth.Redacted {
			if old.GetBraveKey() == "" {
				return missing("the Brave key")
			}
			s.BraveKey = old.GetBraveKey()
		}
		for _, f := range []struct {
			what   string
			in     map[string]string
			before map[string]string
		}{{"env", s.Env, old.GetEnv()}, {"headers", s.Headers, old.GetHeaders()}} {
			for k, v := range maps.Clone(f.in) {
				if v != auth.Redacted {
					continue
				}
				kept, ok := f.before[k]
				if !ok {
					return missing(f.what + " " + k)
				}
				f.in[k] = kept
			}
		}
	}
	return nil
}
