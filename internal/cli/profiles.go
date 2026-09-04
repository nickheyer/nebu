package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Finds one profile by id or name among the daemon's list
func (e *env) findProfile(ctx context.Context, cl *clients, runtimeID, ref string) (*v1.Profile, error) {
	resp, err := cl.runtimes.ListProfiles(ctx, connect.NewRequest(&v1.ListProfilesRequest{RuntimeId: runtimeID}))
	if err != nil {
		return nil, err
	}
	var found *v1.Profile
	for _, p := range resp.Msg.GetProfiles() {
		if p.GetId() == ref {
			return p, nil
		}
		if strings.EqualFold(p.GetName(), ref) {
			if found != nil {
				return nil, fmt.Errorf("%q names a profile of both %s and %s, pass its id or --runtime", ref, found.GetRuntimeId(), p.GetRuntimeId())
			}
			found = p
		}
	}
	if found == nil {
		return nil, fmt.Errorf("unknown profile %q, see nebu profiles", ref)
	}
	return found, nil
}

func profilesTable(w io.Writer, list []*v1.Profile) {
	var rows [][]string
	for _, p := range list {
		def := "-"
		if p.GetDefault() {
			def = "yes"
		}
		rows = append(rows, []string{p.GetId(), p.GetRuntimeId(), p.GetName(), def, compact(p.GetParams()), p.GetDescription()})
	}
	table(w, []string{"ID", "RUNTIME", "NAME", "DEFAULT", "PARAMS", "DESCRIPTION"}, rows)
}

func runProfiles(ctx context.Context, e *env, args []string) error {
	fs := e.flags("profiles")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	req := &v1.ListProfilesRequest{}
	if len(positional) == 1 {
		req.RuntimeId = positional[0]
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.runtimes.ListProfiles(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { profilesTable(w, resp.Msg.GetProfiles()) })
}

func runProfilesAdd(ctx context.Context, e *env, args []string) error {
	fs := e.flags("profiles add")
	description := fs.String("description", "", "what the profile is for")
	def := fs.Bool("default", false, "apply to every run of the runtime that names no profile")
	var params multi
	fs.Var(&params, "param", "runtime param as name=value, repeatable, see nebu runtimes list")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 2 {
		return fmt.Errorf("usage: nebu profiles add <runtime> <name> [--description D] [--param k=v]... [--default]")
	}
	paramMap, err := parseParams(params)
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
	p := &v1.Profile{RuntimeId: positional[0], Name: positional[1], Description: *description, Params: paramMap, Default: *def}
	resp, err := cl.runtimes.CreateProfile(ctx, connect.NewRequest(&v1.CreateProfileRequest{Profile: p}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { profilesTable(w, []*v1.Profile{resp.Msg.GetProfile()}) })
}

func runProfilesUpdate(ctx context.Context, e *env, args []string) error {
	fs := e.flags("profiles update")
	runtimeID := fs.String("runtime", "", "runtime the name belongs to, when the name is used by several")
	name := fs.String("name", "", "new name")
	description := fs.String("description", "", "what the profile is for")
	def := fs.Bool("default", false, "apply to every run of the runtime that names no profile, false to step down")
	var params, unset multi
	fs.Var(&params, "param", "param as name=value, repeatable, merged into what the profile has")
	fs.Var(&unset, "unset", "param to drop back to the runtime default, repeatable")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu profiles update <id|name> [--runtime R] [--name N] [--description D] [--param k=v]... [--unset k]... [--default=BOOL]")
	}
	paramMap, err := parseParams(params)
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
	// The update replaces every field, so start from what the profile has and change only what was passed
	p, err := e.findProfile(ctx, cl, *runtimeID, positional[0])
	if err != nil {
		return err
	}
	if p.Params == nil {
		p.Params = map[string]string{}
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "name":
			p.Name = *name
		case "description":
			p.Description = *description
		case "default":
			p.Default = *def
		}
	})
	for k, v := range paramMap {
		p.Params[k] = v
	}
	for _, k := range unset {
		delete(p.Params, k)
	}
	resp, err := cl.runtimes.UpdateProfile(ctx, connect.NewRequest(&v1.UpdateProfileRequest{Profile: p}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { profilesTable(w, []*v1.Profile{resp.Msg.GetProfile()}) })
}

func runProfilesRemove(ctx context.Context, e *env, args []string) error {
	fs := e.flags("profiles remove")
	runtimeID := fs.String("runtime", "", "runtime the name belongs to, when the name is used by several")
	force := fs.Bool("force", false, "clear the watches, wants, slots, and instances naming it first, they fall back to the runtime default")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu profiles remove <id|name> [--runtime R] [--force]")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	p, err := e.findProfile(ctx, cl, *runtimeID, positional[0])
	if err != nil {
		return err
	}
	resp, err := cl.runtimes.DeleteProfile(ctx, connect.NewRequest(&v1.DeleteProfileRequest{Id: p.GetId(), Force: *force}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "removed %s %s\n", resp.Msg.GetProfile().GetRuntimeId(), resp.Msg.GetProfile().GetName())
	})
}
