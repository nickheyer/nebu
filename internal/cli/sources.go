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
		return 0, fmt.Errorf("unknown provider %q, one of %s", s, strings.Join(sourceKinds(), ", "))
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
		{"name", s.GetName()},
		{"provider", c.GetName() + " (" + eval.EnumShort(s.GetKind()) + ")"},
		{"origin", origin},
		{"transports", strings.Join(c.GetTransports(), ", ")},
		{"created", stamp(s.GetCreatedAt())},
		{"updated", stamp(s.GetUpdatedAt())},
	}
	for _, f := range c.GetFields() {
		value, set := s.GetConfig()[f.GetName()]
		if !set {
			value = f.GetDefault()
			if value == "" {
				value = "-"
			} else {
				value += " (default)"
			}
		}
		rows = append(rows, []string{f.GetName(), value})
	}
	if st.GetError() != "" {
		rows = append(rows, []string{"error", st.GetError()})
	}
	table(w, nil, rows)
}

func runSourcesProviders(ctx context.Context, e *env, args []string) error {
	fs := e.flags("sources providers")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.sources.ListProviders(ctx, connect.NewRequest(&v1.ListProvidersRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, p := range resp.Msg.GetProviders() {
			var settings []string
			for _, f := range p.GetFields() {
				s := f.GetName()
				if f.GetRequired() {
					s += "*"
				}
				if f.GetDefault() != "" {
					s += "=" + f.GetDefault()
				}
				settings = append(settings, s)
			}
			origin := "seeded"
			if p.GetConfigured() {
				origin = "config only"
			}
			rows = append(rows, []string{eval.EnumShort(p.GetKind()), p.GetName(), origin, strings.Join(p.GetTransports(), ","), strings.Join(settings, " ")})
		}
		table(w, []string{"KIND", "PROVIDER", "DEFAULT", "TRANSPORTS", "SETTINGS"}, rows)
	})
}

func runSourcesAdd(ctx context.Context, e *env, args []string) error {
	fs := e.flags("sources add")
	kind := fs.String("kind", "", "provider, one of "+strings.Join(sourceKinds(), ", "))
	name := fs.String("name", "", "display name, the id when empty")
	var settings multi
	fs.Var(&settings, "set", "setting as name=value, repeatable, see nebu sources providers")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 || *kind == "" {
		return fmt.Errorf("usage: nebu sources add <id> --kind KIND [--name NAME] [--set name=value]...")
	}
	k, err := parseSourceKind(*kind)
	if err != nil {
		return err
	}
	cfg, err := parseParams(settings)
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	src := &v1.Source{Id: positional[0], Kind: k, Name: *name, Config: cfg}
	resp, err := cl.sources.CreateSource(ctx, connect.NewRequest(&v1.CreateSourceRequest{Source: src}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { sourceRow(w, resp.Msg.GetSource()) })
}

func runSourcesUpdate(ctx context.Context, e *env, args []string) error {
	fs := e.flags("sources update")
	name := fs.String("name", "", "display name")
	var settings, unset multi
	fs.Var(&settings, "set", "setting as name=value, repeatable, merged into what the source has")
	fs.Var(&unset, "unset", "setting to drop back to the provider default, repeatable")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu sources update <id> [--name NAME] [--set name=value]... [--unset name]...")
	}
	cfg, err := parseParams(settings)
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	current, err := e.findSource(ctx, cl, positional[0])
	if err != nil {
		return err
	}
	// The update replaces every setting, so start from what the source has and change only what was passed
	src := current.GetSource()
	if src.Config == nil {
		src.Config = map[string]string{}
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "name" {
			src.Name = *name
		}
	})
	for k, v := range cfg {
		src.Config[k] = v
	}
	for _, k := range unset {
		delete(src.Config, k)
	}
	resp, err := cl.sources.UpdateSource(ctx, connect.NewRequest(&v1.UpdateSourceRequest{Source: src}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { sourceRow(w, resp.Msg.GetSource()) })
}

func runSourcesRemove(ctx context.Context, e *env, args []string) error {
	fs := e.flags("sources remove")
	force := fs.Bool("force", false, "remove the watches of the source and widen the wants narrowed to it first")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu sources remove <id> [--force]")
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.sources.DeleteSource(ctx, connect.NewRequest(&v1.DeleteSourceRequest{Id: positional[0], Force: *force}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "removed %s\n", resp.Msg.GetSource().GetId()) })
}
