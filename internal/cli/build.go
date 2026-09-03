package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func runRuntimesRecipes(ctx context.Context, e *env, args []string) error {
	fs := e.flags("runtimes recipes")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	req := &v1.ListRecipesRequest{}
	if len(positional) == 1 {
		req.RuntimeId = positional[0]
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.builds.ListRecipes(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, rs := range resp.Msg.GetRecipes() {
			r := rs.GetRecipe()
			variants := make([]string, 0, len(r.GetVariants()))
			for _, v := range r.GetVariants() {
				variants = append(variants, v.GetId())
			}
			rows = append(rows, []string{r.GetId(), r.GetRuntimeId(), rs.GetVariant(), strings.Join(variants, ","), eval.EnumShort(rs.GetSandbox()), strings.Join(rs.GetUnmet(), "; "), r.GetDescription()})
		}
		table(w, []string{"ID", "RUNTIME", "SELECTED", "VARIANTS", "SANDBOX", "UNMET", "DESCRIPTION"}, rows)
	})
}

func runBuild(ctx context.Context, e *env, args []string) error {
	fs := e.flags("build")
	recipe := fs.String("recipe", "", "recipe id, the one the runtime manifest names when empty")
	variant := fs.String("variant", "", "variant id, selected from host facts when empty")
	sandboxName := fs.String("sandbox", "", "host or oci, config default when empty")
	image := fs.String("image", "", "container image for the oci sandbox")
	ref := fs.String("ref", "", "source ref, the recipe default when empty")
	force := fs.Bool("force", false, "rebuild even when this exact build already exists")
	detach := fs.Bool("detach", false, "start the task and return its id")
	var vars multi
	fs.Var(&vars, "var", "recipe var as name=value, repeatable")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) > 1 || (len(positional) == 0 && *recipe == "") {
		return fmt.Errorf("usage: nebu build <runtime> [flags]")
	}
	req := &v1.BuildRequest{RecipeId: *recipe, Variant: *variant, Image: *image, Ref: *ref, Force: *force, Vars: map[string]string{}}
	if len(positional) == 1 {
		req.RuntimeId = positional[0]
	}
	switch strings.ToLower(*sandboxName) {
	case "":
	case "host":
		req.Sandbox = v1.SandboxKind_SANDBOX_KIND_HOST
	case "oci", "container":
		req.Sandbox = v1.SandboxKind_SANDBOX_KIND_OCI
	default:
		return fmt.Errorf("sandbox %q: expected host or oci", *sandboxName)
	}
	for _, v := range vars {
		k, val, ok := strings.Cut(v, "=")
		if !ok {
			return fmt.Errorf("var %q: expected name=value", v)
		}
		req.Vars[k] = val
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if *detach {
		if err := e.requireDaemon(); err != nil {
			return err
		}
	}
	resp, err := cl.builds.Build(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	b := resp.Msg.GetBuild()
	if *detach {
		return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "%s %s\n", b.GetId(), resp.Msg.GetTask().GetId()) })
	}
	if e.json {
		if _, err := e.watchSilently(ctx, cl, resp.Msg.GetTask().GetId()); err != nil {
			return err
		}
		final, err := cl.builds.GetBuild(ctx, connect.NewRequest(&v1.GetBuildRequest{Id: b.GetId()}))
		if err != nil {
			return err
		}
		return e.print(final.Msg, nil)
	}
	fmt.Fprintf(e.out, "build %s recipe %s variant %s ref %s sandbox %s\n", b.GetId(), b.GetRecipeId(), b.GetVariant(), b.GetRef(), eval.EnumShort(b.GetSandbox()))
	if _, err := e.watch(ctx, cl, resp.Msg.GetTask().GetId()); err != nil {
		return err
	}
	final, err := cl.builds.GetBuild(ctx, connect.NewRequest(&v1.GetBuildRequest{Id: b.GetId()}))
	if err != nil {
		return err
	}
	fb := final.Msg.GetBuild()
	if fb.GetState() != v1.BuildState_BUILD_STATE_SUCCEEDED {
		return fmt.Errorf("build %s %s: %s", fb.GetId(), eval.EnumShort(fb.GetState()), fb.GetError())
	}
	fmt.Fprintf(e.out, "install %s at %s\n", fb.GetInstallId(), fb.GetBinary())
	return nil
}

func runBuildsList(ctx context.Context, e *env, args []string) error {
	fs := e.flags("builds list")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	req := &v1.ListBuildsRequest{}
	if len(positional) == 1 {
		req.RuntimeId = positional[0]
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.builds.ListBuilds(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, b := range resp.Msg.GetBuilds() {
			rows = append(rows, []string{b.GetId(), b.GetRuntimeId(), b.GetRecipeId(), b.GetVariant(), b.GetRef(), strings.ToUpper(eval.EnumShort(b.GetState())), eval.EnumShort(b.GetSandbox()), b.GetInstallId(), b.GetError()})
		}
		table(w, []string{"ID", "RUNTIME", "RECIPE", "VARIANT", "REF", "STATE", "SANDBOX", "INSTALL", "ERROR"}, rows)
	})
}

func runBuildsShow(ctx context.Context, e *env, args []string) error {
	fs := e.flags("builds show")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu builds show <id>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.builds.GetBuild(ctx, connect.NewRequest(&v1.GetBuildRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { renderBuild(w, resp.Msg.GetBuild()) })
}

func renderBuild(w io.Writer, b *v1.Build) {
	fmt.Fprintf(w, "%s %s %s\n", b.GetId(), b.GetRecipeId(), strings.ToUpper(eval.EnumShort(b.GetState())))
	rows := [][]string{
		{"runtime", b.GetRuntimeId()},
		{"variant", b.GetVariant()},
		{"ref", b.GetRef()},
		{"commit", b.GetCommit()},
		{"sandbox", eval.EnumShort(b.GetSandbox()) + " " + b.GetImage()},
		{"dir", b.GetDir()},
		{"binary", b.GetBinary()},
		{"install", b.GetInstallId()},
		{"task", b.GetTaskId()},
		{"created", stamp(b.GetCreatedAt())},
		{"finished", stamp(b.GetFinishedAt())},
		{"patches", strings.Join(b.GetPatches(), ", ")},
	}
	if b.GetError() != "" {
		rows = append(rows, []string{"error", b.GetError()})
	}
	table(w, nil, rows)
	section(w, "vars")
	table(w, nil, pairs(b.GetVars()))
	section(w, "facts")
	table(w, nil, pairs(b.GetFacts()))
}

func pairs(m map[string]string) [][]string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var rows [][]string
	for _, k := range keys {
		rows = append(rows, []string{k, m[k]})
	}
	return rows
}

func runBuildsRemove(ctx context.Context, e *env, args []string) error {
	fs := e.flags("builds remove")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu builds remove <id>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.builds.RemoveBuild(ctx, connect.NewRequest(&v1.RemoveBuildRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "removed %s\n", resp.Msg.GetBuild().GetId()) })
}

func runStoreExport(ctx context.Context, e *env, args []string) error {
	fs := e.flags("store export")
	source := fs.String("source", "", "limit to one source")
	group := fs.String("group", "", "limit to one group")
	dir := fs.String("dir", "", "mirror directory to write, required")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("usage: nebu store export --dir <mirror dir> [repo] [flags]")
	}
	req := &v1.ExportRequest{SourceId: *source, Group: *group, Dir: *dir}
	if len(positional) == 1 {
		req.Repo = positional[0]
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.store.Export(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	if e.json {
		final, err := e.watchSilently(ctx, cl, resp.Msg.GetTask().GetId())
		if err != nil {
			return err
		}
		return e.print(final, nil)
	}
	_, err = e.watch(ctx, cl, resp.Msg.GetTask().GetId())
	return err
}
