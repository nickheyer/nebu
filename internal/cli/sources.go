package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Reads a kind as its short name, such as huggingface or oci, or the full enum name
func parseSourceKind(s string) (v1.SourceKind, error) {
	name := strings.ToUpper(strings.TrimSpace(s))
	if !strings.HasPrefix(name, "SOURCE_KIND_") {
		name = "SOURCE_KIND_" + name
	}
	n, ok := v1.SourceKind_value[name]
	if !ok || n == 0 {
		return 0, fmt.Errorf("unknown source kind %q, one of %s", s, strings.Join(sourceKinds(), ", "))
	}
	return v1.SourceKind(n), nil
}

// Every kind by short name, in enum order
func sourceKinds() []string {
	var nums []int
	for n := range v1.SourceKind_name {
		if n != 0 {
			nums = append(nums, int(n))
		}
	}
	sort.Ints(nums)
	out := make([]string, 0, len(nums))
	for _, n := range nums {
		out = append(out, eval.EnumShort(v1.SourceKind(n)))
	}
	return out
}

// Finds one source by id among the daemon's list
func (e *env) findSource(ctx context.Context, cl *clients, id string) (*v1.SourceStatus, error) {
	resp, err := cl.sources.ListSources(ctx, connect.NewRequest(&v1.ListSourcesRequest{}))
	if err != nil {
		return nil, err
	}
	for _, st := range resp.Msg.GetSources() {
		if st.GetSource().GetId() == id {
			return st, nil
		}
	}
	return nil, fmt.Errorf("unknown source %q, see nebu sources", id)
}

func sourceRow(w io.Writer, st *v1.SourceStatus) {
	s, c := st.GetSource(), st.GetCapabilities()
	origin := "added"
	if s.GetSeeded() {
		origin = "seeded"
	}
	rows := [][]string{
		{"id", s.GetId()},
		{"kind", eval.EnumShort(s.GetKind())},
		{"origin", origin},
		{"endpoint", c.GetEndpoint()},
		{"path", s.GetPath()},
		{"token env", c.GetTokenEnv()},
		{"options", compact(s.GetOptions())},
		{"created", stamp(s.GetCreatedAt())},
		{"updated", stamp(s.GetUpdatedAt())},
	}
	if st.GetError() != "" {
		rows = append(rows, []string{"error", st.GetError()})
	}
	table(w, nil, rows)
}

func runSourcesAdd(ctx context.Context, e *env, args []string) error {
	fs := e.flags("sources add")
	kind := fs.String("kind", "", "provider, one of "+strings.Join(sourceKinds(), ", "))
	endpoint := fs.String("endpoint", "", "API host, the provider default when empty")
	tokenEnv := fs.String("token-env", "", "environment variable holding the credential, the provider default when empty")
	path := fs.String("path", "", "directory, for local and mirror sources")
	var options multi
	fs.Var(&options, "option", "provider specific setting as name=value, repeatable")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 || *kind == "" {
		return fmt.Errorf("usage: nebu sources add <id> --kind KIND [--endpoint URL] [--token-env VAR] [--path DIR] [--option k=v]")
	}
	k, err := parseSourceKind(*kind)
	if err != nil {
		return err
	}
	opts, err := parseParams(options)
	if err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	src := &v1.Source{Id: positional[0], Kind: k, Endpoint: *endpoint, TokenEnv: *tokenEnv, Path: *path, Options: opts}
	resp, err := cl.sources.CreateSource(ctx, connect.NewRequest(&v1.CreateSourceRequest{Source: src}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { sourceRow(w, resp.Msg.GetSource()) })
}

func runSourcesUpdate(ctx context.Context, e *env, args []string) error {
	fs := e.flags("sources update")
	endpoint := fs.String("endpoint", "", "API host, the provider default when empty")
	tokenEnv := fs.String("token-env", "", "environment variable holding the credential, the provider default when empty")
	path := fs.String("path", "", "directory, for local and mirror sources")
	var options multi
	fs.Var(&options, "option", "provider specific setting as name=value, repeatable, replaces every option")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu sources update <id> [--endpoint URL] [--token-env VAR] [--path DIR] [--option k=v]")
	}
	opts, err := parseParams(options)
	if err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	current, err := e.findSource(ctx, cl, positional[0])
	if err != nil {
		return err
	}
	// The update replaces every setting, so start from what the source has and change only what was passed
	src := current.GetSource()
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "endpoint":
			src.Endpoint = *endpoint
		case "token-env":
			src.TokenEnv = *tokenEnv
		case "path":
			src.Path = *path
		case "option":
			src.Options = opts
		}
	})
	resp, err := cl.sources.UpdateSource(ctx, connect.NewRequest(&v1.UpdateSourceRequest{Source: src}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { sourceRow(w, resp.Msg.GetSource()) })
}

func runSourcesRemove(ctx context.Context, e *env, args []string) error {
	fs := e.flags("sources remove")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu sources remove <id>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.sources.DeleteSource(ctx, connect.NewRequest(&v1.DeleteSourceRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "removed %s\n", resp.Msg.GetSource().GetId()) })
}
