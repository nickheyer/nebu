package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

type stepIn struct {
	Name    string            `json:"name,omitempty" jsonschema:"what the log calls the step"`
	Command []string          `json:"command,omitempty" jsonschema:"the words of the command, run without a shell"`
	Script  string            `json:"script,omitempty" jsonschema:"a shell script sh runs, in place of command"`
	Env     map[string]string `json:"env,omitempty" jsonschema:"environment variables for this step"`
	Dir     string            `json:"dir,omitempty" jsonschema:"working directory relative to the source tree"`
}

type patchIn struct {
	ID      string `json:"id,omitempty"`
	URL     string `json:"url,omitempty" jsonschema:"where the unified diff is fetched from"`
	Content string `json:"content,omitempty" jsonschema:"the unified diff itself, in place of url"`
	Strip   uint32 `json:"strip,omitempty" jsonschema:"leading path components stripped from the diff's paths (default 1)"`
}

type buildIn struct {
	RuntimeID      string            `json:"runtime_id" jsonschema:"runtime to build, such as llamacpp"`
	RecipeID       string            `json:"recipe_id,omitempty" jsonschema:"recipe to build from, the runtime's own when empty"`
	Variant        string            `json:"variant,omitempty" jsonschema:"backend to build, the first this host supports when empty"`
	Ref            string            `json:"ref,omitempty" jsonschema:"tag, branch, commit, or any other ref such as refs/pull/123/head, the newest release when empty"`
	Repo           string            `json:"repo,omitempty" jsonschema:"git repository to build in place of the recipe's, a URL or a GitHub owner/name"`
	Vars           map[string]string `json:"vars,omitempty" jsonschema:"recipe variables by name"`
	SkipPatches    []string          `json:"skip_patches,omitempty" jsonschema:"recipe patches to leave out, by id"`
	Patches        []patchIn         `json:"patches,omitempty" jsonschema:"diffs applied to the source after the recipe's patches"`
	Steps          []stepIn          `json:"steps,omitempty" jsonschema:"steps to run in place of the recipe's"`
	Env            map[string]string `json:"env,omitempty" jsonschema:"environment variables for every step"`
	Outputs        []string          `json:"outputs,omitempty" jsonschema:"paths under the source tree to keep in place of the recipe's, globs allowed"`
	Binary         string            `json:"binary,omitempty" jsonschema:"the server binary among what is kept, in place of the recipe's"`
	Sandbox        string            `json:"sandbox,omitempty" jsonschema:"host to build with this host's tools, oci to build in a container"`
	Image          string            `json:"image,omitempty" jsonschema:"container image for the oci sandbox"`
	TimeoutMinutes uint32            `json:"timeout_minutes,omitempty" jsonschema:"longest the build may run, the recipe's limit when empty"`
	Force          bool              `json:"force,omitempty" jsonschema:"build again when this exact build already exists"`
}

// The sandbox a name stands for, the configured one when empty
func sandboxOf(name string) (v1.SandboxKind, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "":
		return v1.SandboxKind_SANDBOX_KIND_UNSPECIFIED, nil
	case "host":
		return v1.SandboxKind_SANDBOX_KIND_HOST, nil
	case "oci", "container":
		return v1.SandboxKind_SANDBOX_KIND_OCI, nil
	}
	return 0, fmt.Errorf("sandbox must be host or oci, not %q", name)
}

// The build request a tool call describes
func (in buildIn) request() (*v1.BuildRequest, error) {
	sandbox, err := sandboxOf(in.Sandbox)
	if err != nil {
		return nil, err
	}
	req := &v1.BuildRequest{
		RuntimeId: in.RuntimeID, RecipeId: in.RecipeID, Variant: in.Variant, Ref: in.Ref, Repo: in.Repo, Vars: in.Vars, SkipPatches: in.SkipPatches,
		Env: in.Env, Outputs: in.Outputs, Binary: in.Binary, Sandbox: sandbox, Image: in.Image, TimeoutMs: in.TimeoutMinutes * 60_000, Force: in.Force,
	}
	for _, s := range in.Steps {
		req.Steps = append(req.Steps, &v1.BuildStep{Name: s.Name, Command: s.Command, Script: s.Script, Env: s.Env, Dir: s.Dir})
	}
	for _, p := range in.Patches {
		req.Patches = append(req.Patches, &v1.Patch{Id: p.ID, Url: p.URL, Content: p.Content, Strip: p.Strip})
	}
	return req, nil
}

// What the build tools say of the parts of a build a call may set
const buildParts = "Nothing about a build is fixed: ref takes any tag, branch, commit, or ref such as refs/pull/123/head, repo a fork, vars the recipe's variables, patches more diffs, env the environment, steps the commands themselves, and outputs and binary what is kept. " +
	"In a step's command, script, and env, {{.Src}}, {{.Out}}, and {{.Root}} are the source tree, the output directory, and the build's own directory, {{.Jobs}} the parallel jobs, {{.Ref}}, {{.Commit}}, and {{.Variant}} what is built, and {{.Vars.name}} a variable"

// Adds the tools listing recipes, previewing and starting builds, and removing builds and installs
func (n *nebu) addBuildTools(s *mcp.Server, ro *mcp.ToolAnnotations) {
	gone := &mcp.ToolAnnotations{DestructiveHint: ptr(true)}
	mcp.AddTool(s, &mcp.Tool{Name: "list_recipes", Description: "Lists the recipes runtimes build from source with, each with its variants, variables, patches, and what this host lacks for it", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in runtimeIn) (*mcp.CallToolResult, any, error) {
		r, err := n.builds.ListRecipes(ctx, connect.NewRequest(&v1.ListRecipesRequest{RuntimeId: in.RuntimeID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "preview_build", Description: "Resolves a build without running it: the commit its ref names, the steps it would run, what it keeps, and the build already made when this exact one exists. The steps answered are in the form build_runtime takes", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in buildIn) (*mcp.CallToolResult, any, error) {
		build, err := in.request()
		if err != nil {
			return nil, nil, err
		}
		req, err := checked(&v1.PreviewBuildRequest{Build: build})
		if err != nil {
			return nil, nil, err
		}
		r, err := n.builds.PreviewBuild(ctx, req)
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "build_runtime", Description: "Builds a runtime from source, installs what it built, and returns the build and its task. " + buildParts}, func(ctx context.Context, _ *mcp.CallToolRequest, in buildIn) (*mcp.CallToolResult, any, error) {
		build, err := in.request()
		if err != nil {
			return nil, nil, err
		}
		req, err := checked(build)
		if err != nil {
			return nil, nil, err
		}
		r, err := n.builds.Build(ctx, req)
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "list_builds", Description: "Lists builds newest first with their states, refs, commits, errors, and the requests they were made from", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in runtimeIn) (*mcp.CallToolResult, any, error) {
		r, err := n.builds.ListBuilds(ctx, connect.NewRequest(&v1.ListBuildsRequest{RuntimeId: in.RuntimeID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "get_build", Description: "Reads one build with its directory, task, install, and the request it was made from. Its log is its task's", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.builds.GetBuild(ctx, connect.NewRequest(&v1.GetBuildRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "remove_build", Description: "Removes a build, its tree, and the install it made", Annotations: gone}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.builds.RemoveBuild(ctx, connect.NewRequest(&v1.RemoveBuildRequest{Id: in.ID}))
		return answer(r, err)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "remove_install", Description: "Removes an installed runtime and the files it downloaded or built, refused while a slot or an instance uses it", Annotations: gone}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		r, err := n.runtimes.RemoveInstall(ctx, connect.NewRequest(&v1.RemoveInstallRequest{Id: in.ID}))
		return answer(r, err)
	})
}
