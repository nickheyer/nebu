package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

func runRuntimes(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("runtimes"), args, 0, 0, "runtimes"); err != nil {
		return err
	}
	resp, err := e.cl.runtimes.ListRuntimes(ctx, connect.NewRequest(&v1.ListRuntimesRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, rt := range resp.Msg.GetRuntimes() {
			m := rt.GetRuntime()
			rows = append(rows, []string{m.GetId(), m.GetName(), text.Enum(m.GetKind()), strings.Join(m.GetFormats(), ","), yes(rt.GetCompatible()), strings.Join(rt.GetUnmet(), "; ")})
		}
		table(w, []string{"ID", "NAME", "SERVES", "FORMATS", "COMPATIBLE", "UNMET"}, rows)
	})
}

// Prints one runtime with its install methods and every param --param may name
func runRuntimesShow(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("runtimes show"), args, 1, 1, "runtimes show <runtime>")
	if err != nil {
		return err
	}
	resp, err := e.cl.runtimes.GetRuntime(ctx, connect.NewRequest(&v1.GetRuntimeRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	rt := resp.Msg.GetRuntime()
	return e.print(rt, func(w io.Writer) {
		m := rt.GetRuntime()
		table(w, nil, [][]string{{"id", m.GetId()}, {"name", m.GetName()}, {"serves", text.Enum(m.GetKind()) + " models"}, {"formats", strings.Join(m.GetFormats(), ", ")}, {"api", text.Enum(m.GetApi())}, {"compatible", yes(rt.GetCompatible())}, {"unmet", strings.Join(rt.GetUnmet(), "; ")}})
		for _, opt := range rt.GetInstalls() {
			im := opt.GetMethod()
			section(w, "install "+im.GetId())
			rows := [][]string{{"how", methodText(im)}}
			if im.GetDescription() != "" {
				rows = append(rows, []string{"about", im.GetDescription()})
			}
			if len(opt.GetUnmet()) > 0 {
				rows = append(rows, []string{"unmet", strings.Join(opt.GetUnmet(), "; ")})
			}
			table(w, nil, rows)
			rows = nil
			for _, f := range opt.GetFields() {
				name := f.GetName()
				if f.GetRequired() {
					name += "*"
				}
				rows = append(rows, []string{name, text.Enum(f.GetType()), orDash(f.GetDefault()), strings.Join(f.GetChoices(), ","), f.GetDescription()})
			}
			table(w, []string{"SETTING", "TYPE", "DEFAULT", "CHOICES", "DESCRIPTION"}, rows)
		}
		section(w, "params")
		var rows [][]string
		for _, p := range m.GetParams() {
			rows = append(rows, []string{p.GetName(), text.Enum(p.GetType()), p.GetDefault(), strings.Join(p.GetChoices(), ","), p.GetDescription()})
		}
		table(w, []string{"NAME", "TYPE", "DEFAULT", "CHOICES", "DESCRIPTION"}, rows)
	})
}

// One line saying what an install method does
func methodText(im *v1.InstallMethod) string {
	switch im.GetKind() {
	case v1.InstallKind_INSTALL_KIND_ADOPTED:
		return "adopt " + strings.Join(im.GetBinaries(), " or ") + " from PATH"
	case v1.InstallKind_INSTALL_KIND_PREBUILT:
		return "download a release of " + im.GetReleases()
	case v1.InstallKind_INSTALL_KIND_BUILT:
		return "build recipe " + im.GetRecipeId()
	}
	return "-"
}

func runRuntimesInstalls(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("runtimes installs"), args, 0, 1, "runtimes installs [runtime]")
	if err != nil {
		return err
	}
	req := &v1.ListInstallsRequest{}
	if len(positional) == 1 {
		req.RuntimeId = positional[0]
	}
	resp, err := e.cl.runtimes.ListInstalls(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { installsTable(w, resp.Msg.GetInstalls()) })
}

func installsTable(w io.Writer, list []*v1.Install) {
	var rows [][]string
	for _, in := range list {
		rows = append(rows, []string{in.GetId(), in.GetRuntimeId(), text.Enum(in.GetKind()), in.GetVersion(), in.GetPath(), compact(in.GetFacts())})
	}
	table(w, []string{"ID", "RUNTIME", "KIND", "VERSION", "PATH", "FACTS"}, rows)
}

func runRuntimesAdopt(ctx context.Context, e *env, args []string) error {
	fs := e.flags("runtimes adopt")
	path := fs.String("path", "", "executable path, searched on PATH when empty")
	positional, err := e.parse(fs, args, 1, 1, "runtimes adopt <runtime> [--path P]")
	if err != nil {
		return err
	}
	resp, err := e.cl.runtimes.AdoptInstall(ctx, connect.NewRequest(&v1.AdoptInstallRequest{RuntimeId: positional[0], Path: *path}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { installsTable(w, []*v1.Install{resp.Msg.GetInstall()}) })
}

func runRuntimesInstall(ctx context.Context, e *env, args []string) error {
	fs := e.flags("runtimes install")
	method := fs.String("method", "", "install method id, see nebu runtimes show")
	var settings multi
	fs.Var(&settings, "set", "method setting as name=value, repeatable, see nebu runtimes show")
	usage := "runtimes install <runtime> --method ID [--set name=value]..."
	positional, err := e.parse(fs, args, 1, 1, usage)
	if err != nil {
		return err
	}
	if *method == "" {
		return fmt.Errorf("usage: nebu %s", usage)
	}
	values, err := pairs(settings, "setting")
	if err != nil {
		return err
	}
	resp, err := e.cl.runtimes.Install(ctx, connect.NewRequest(&v1.InstallRequest{RuntimeId: positional[0], Method: *method, Settings: values}))
	if err != nil {
		return err
	}
	return e.done(e.follow(ctx, resp.Msg.GetTask().GetId()))
}

func runRuntimesRemove(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("runtimes remove"), args, 1, 1, "runtimes remove <install-id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.runtimes.RemoveInstall(ctx, connect.NewRequest(&v1.RemoveInstallRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "removed %s\n", resp.Msg.GetInstall().GetId()) })
}

func runRuntimesRecipes(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("runtimes recipes"), args, 0, 1, "runtimes recipes [runtime]")
	if err != nil {
		return err
	}
	req := &v1.ListRecipesRequest{}
	if len(positional) == 1 {
		req.RuntimeId = positional[0]
	}
	resp, err := e.cl.builds.ListRecipes(ctx, connect.NewRequest(req))
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
			rows = append(rows, []string{r.GetId(), r.GetRuntimeId(), rs.GetVariant(), strings.Join(variants, ","), text.Enum(rs.GetSandbox()), strings.Join(rs.GetUnmet(), "; "), r.GetDescription()})
		}
		table(w, []string{"ID", "RUNTIME", "SELECTED", "VARIANTS", "SANDBOX", "UNMET", "DESCRIPTION"}, rows)
	})
}

func runBuild(ctx context.Context, e *env, args []string) error {
	fs := e.flags("build")
	recipe := fs.String("recipe", "", "recipe id, the one the runtime builds from when empty")
	variant := fs.String("variant", "", "variant id, selected from host facts when empty")
	sandboxName := fs.String("sandbox", "", "host or oci, config default when empty")
	image := fs.String("image", "", "container image for the oci sandbox")
	ref := fs.String("ref", "", "source ref, the recipe default when empty")
	force := fs.Bool("force", false, "rebuild even when this exact build already exists")
	detach := fs.Bool("detach", false, "start the task and return its id")
	var vars multi
	fs.Var(&vars, "var", "recipe var as name=value, repeatable")
	usage := "build <runtime> [flags]"
	positional, err := e.parse(fs, args, 0, 1, usage)
	if err != nil {
		return err
	}
	if len(positional) == 0 && *recipe == "" {
		return fmt.Errorf("usage: nebu %s", usage)
	}
	if *detach {
		if err := e.requireDaemon(); err != nil {
			return err
		}
	}
	varMap, err := pairs(vars, "var")
	if err != nil {
		return err
	}
	req := &v1.BuildRequest{RecipeId: *recipe, Variant: *variant, Image: *image, Ref: *ref, Force: *force, Vars: varMap}
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
	resp, err := e.cl.builds.Build(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	b := resp.Msg.GetBuild()
	if *detach {
		return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "%s %s\n", b.GetId(), resp.Msg.GetTask().GetId()) })
	}
	e.text("build %s recipe %s variant %s ref %s sandbox %s\n", b.GetId(), b.GetRecipeId(), b.GetVariant(), b.GetRef(), text.Enum(b.GetSandbox()))
	if _, err := e.follow(ctx, resp.Msg.GetTask().GetId()); err != nil {
		return err
	}
	final, err := e.cl.builds.GetBuild(ctx, connect.NewRequest(&v1.GetBuildRequest{Id: b.GetId()}))
	if err != nil {
		return err
	}
	fb := final.Msg.GetBuild()
	if fb.GetState() != v1.BuildState_BUILD_STATE_SUCCEEDED {
		return fmt.Errorf("build %s %s: %s", fb.GetId(), text.Enum(fb.GetState()), fb.GetError())
	}
	return e.print(final.Msg, func(w io.Writer) { fmt.Fprintf(w, "install %s at %s\n", fb.GetInstallId(), fb.GetBinary()) })
}

func runBuildsList(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("builds list"), args, 0, 1, "builds list [runtime]")
	if err != nil {
		return err
	}
	req := &v1.ListBuildsRequest{}
	if len(positional) == 1 {
		req.RuntimeId = positional[0]
	}
	resp, err := e.cl.builds.ListBuilds(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, b := range resp.Msg.GetBuilds() {
			rows = append(rows, []string{b.GetId(), b.GetRuntimeId(), b.GetRecipeId(), b.GetVariant(), b.GetRef(), loud(b.GetState()), text.Enum(b.GetSandbox()), b.GetInstallId(), b.GetError()})
		}
		table(w, []string{"ID", "RUNTIME", "RECIPE", "VARIANT", "REF", "STATE", "SANDBOX", "INSTALL", "ERROR"}, rows)
	})
}

func runBuildsShow(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("builds show"), args, 1, 1, "builds show <id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.builds.GetBuild(ctx, connect.NewRequest(&v1.GetBuildRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		b := resp.Msg.GetBuild()
		fmt.Fprintf(w, "%s %s %s\n", b.GetId(), b.GetRecipeId(), loud(b.GetState()))
		rows := [][]string{
			{"runtime", b.GetRuntimeId()},
			{"variant", b.GetVariant()},
			{"ref", b.GetRef()},
			{"commit", b.GetCommit()},
			{"sandbox", text.Enum(b.GetSandbox()) + " " + b.GetImage()},
			{"dir", b.GetDir()},
			{"binary", b.GetBinary()},
			{"install", b.GetInstallId()},
			{"task", b.GetTaskId()},
			{"created", when(b.GetCreatedAt(), time.RFC3339)},
			{"finished", when(b.GetFinishedAt(), time.RFC3339)},
			{"patches", strings.Join(b.GetPatches(), ", ")},
		}
		if b.GetError() != "" {
			rows = append(rows, []string{"error", b.GetError()})
		}
		table(w, nil, rows)
		section(w, "vars")
		table(w, nil, rowsOf(b.GetVars()))
		section(w, "facts")
		table(w, nil, rowsOf(b.GetFacts()))
	})
}

func runBuildsRemove(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("builds remove"), args, 1, 1, "builds remove <id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.builds.RemoveBuild(ctx, connect.NewRequest(&v1.RemoveBuildRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "removed %s\n", resp.Msg.GetBuild().GetId()) })
}
