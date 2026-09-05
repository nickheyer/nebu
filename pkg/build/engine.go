package build

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nickheyer/nebu/pkg/archive"
	"github.com/nickheyer/nebu/pkg/build/sandbox"
	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/transfer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const cacheDirName = "cache"

// Reports which step is running
type Progress func(done, total int, name string)

// Runs builds under one root directory
type Engine struct {
	Root    string
	Patches map[string][]byte
	Jobs    int
	// Where a latest ref reads releases from
	Sources *sources.Registry
	// Moves archives and patches under the transfer limits
	Fetcher *transfer.Fetcher
	Log     *slog.Logger
}

// Makes the record a build runs from, without the network
func (s *Selection) Build() *v1.Build {
	return &v1.Build{
		RecipeId:  s.Recipe.Spec.GetId(),
		RuntimeId: s.Recipe.Spec.GetRuntimeId(),
		Variant:   s.Variant.GetId(),
		Ref:       s.Ref,
		Sandbox:   s.Sandbox,
		Image:     s.Image,
		Vars:      s.Vars,
		Facts:     s.Facts,
		CreatedAt: timestamppb.Now(),
	}
}

// Resolves the ref, selects patches, and assigns id and directory
func (e *Engine) Resolve(ctx context.Context, s *Selection, b *v1.Build) error {
	rc := s.Recipe
	tctx := s.context()
	if rc.releases != nil && (b.Ref == "" || b.Ref == Latest) {
		repo, err := rc.releases.Render(tctx)
		if err != nil {
			return err
		}
		sourceID := DefaultReleaseSource
		if rc.source != nil {
			if sourceID, err = rc.source.Render(tctx); err != nil {
				return err
			}
		}
		tag, err := latestTag(ctx, e.Sources, strings.TrimSpace(sourceID), strings.TrimSpace(repo))
		if err != nil {
			return fmt.Errorf("resolve release: %w", err)
		}
		b.Ref = tag
	}
	if rc.archive != nil && b.Ref == "" {
		return fmt.Errorf("%w: recipe %s needs a ref for its archive", ErrSelection, rc.Spec.GetId())
	}
	s.Ref = b.Ref
	if err := s.renderVars(); err != nil {
		return err
	}
	b.Vars = s.Vars
	if b.Image != "" || s.Sandbox != v1.SandboxKind_SANDBOX_KIND_OCI {
		b.Image = s.Image
	}
	b.Patches = nil
	for _, p := range rc.patches {
		ok, err := p.when.Holds(s.env)
		if err != nil {
			return fmt.Errorf("patch %s: %w", p.spec.GetId(), err)
		}
		if ok {
			b.Patches = append(b.Patches, p.spec.GetId())
		}
	}
	contents := map[string][]byte{}
	for _, p := range rc.patches {
		switch {
		case p.spec.GetContent() != "":
			contents[p.spec.GetId()] = []byte(p.spec.GetContent())
		case p.spec.GetFile() != "":
			data, ok := e.Patches[p.spec.GetFile()]
			if !ok {
				return fmt.Errorf("patch %s: file %s not found in any spec directory", p.spec.GetId(), p.spec.GetFile())
			}
			contents[p.spec.GetId()] = data
		default:
			contents[p.spec.GetId()] = []byte(p.spec.GetUrl())
		}
	}
	b.Id = hashBuild(b, rc.Spec, contents)
	b.Dir = filepath.Join(e.Root, rc.Spec.GetRuntimeId(), b.Id)
	return nil
}

// Fetches, patches, runs steps, and collects outputs, writing a transcript
func (e *Engine) Run(ctx context.Context, s *Selection, b *v1.Build, out io.Writer, progress Progress) error {
	rc := s.Recipe
	if progress == nil {
		progress = func(int, int, string) {}
	}
	ctx, cancel := context.WithTimeout(ctx, rc.Timeout())
	defer cancel()
	if b.Dir == "" {
		return fmt.Errorf("build has no directory, resolve it first")
	}
	for _, sub := range []string{srcDir, outDir} {
		os.RemoveAll(filepath.Join(b.Dir, sub))
	}
	if err := os.MkdirAll(filepath.Join(b.Dir, outDir), 0o755); err != nil {
		return err
	}
	fmt.Fprintf(out, "build %s recipe %s variant %s ref %s sandbox %s\n", b.GetId(), rc.Spec.GetId(), b.GetVariant(), b.GetRef(), eval.EnumShort(b.GetSandbox()))
	for _, kv := range sortedPairs(b.GetVars()) {
		fmt.Fprintf(out, "var %s\n", kv)
	}
	runner, err := e.runner(s, b)
	if err != nil {
		return err
	}
	jobs := e.Jobs
	if jobs <= 0 {
		jobs = runtime.NumCPU()
	}
	tctx := s.context()
	tctx["jobs"] = jobs
	tctx["root"] = runner.Path("")
	tctx["dir"] = runner.Path(srcDir)
	tctx["out"] = runner.Path(outDir)
	total := len(rc.steps) + 3
	progress(0, total, "fetching source")
	commit, err := e.fetch(ctx, s, b, tctx, out)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	b.Commit = commit
	tctx["commit"] = commit
	progress(1, total, "applying patches")
	if err := e.patch(ctx, s, b, out); err != nil {
		return err
	}
	for i, st := range rc.steps {
		name := st.spec.GetName()
		if name == "" {
			name = fmt.Sprintf("step %d", i+1)
		}
		if st.when != nil {
			ok, err := st.when.Bool(stepEnv(s, tctx))
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if !ok {
				fmt.Fprintf(out, "skip %s\n", name)
				continue
			}
		}
		progress(2+i, total, name)
		command := make([]string, 0, len(st.command))
		for _, t := range st.command {
			arg, err := t.Render(tctx)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			// Empty renders come from unset vars and are dropped
			if arg = strings.TrimSpace(arg); arg != "" {
				command = append(command, arg)
			}
		}
		env, err := eval.RenderTemplates(st.env, tctx)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		dir := srcDir
		if st.dir != nil {
			rendered, err := st.dir.Render(tctx)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if rendered = strings.TrimSpace(rendered); rendered != "" {
				dir = filepath.Join(srcDir, rendered)
			}
		}
		fmt.Fprintf(out, "--- %s: %s\n", name, strings.Join(command, " "))
		started := time.Now()
		if err := runner.Run(ctx, sandbox.Step{Name: name, Command: command, Env: env, Dir: dir}, out); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%s: %w", name, err)
		}
		fmt.Fprintf(out, "--- %s done in %s\n", name, time.Since(started).Round(time.Second))
	}
	progress(total-1, total, "collecting outputs")
	if err := e.collect(s, b, tctx, out); err != nil {
		return err
	}
	progress(total, total, "done")
	return nil
}

func (e *Engine) runner(s *Selection, b *v1.Build) (sandbox.Runner, error) {
	if b.GetSandbox() != v1.SandboxKind_SANDBOX_KIND_OCI {
		return sandbox.NewHost(b.GetDir()), nil
	}
	if s.CLI == "" || b.GetImage() == "" {
		return nil, fmt.Errorf("%w: container sandbox needs a cli and an image", ErrSelection)
	}
	args := make([]string, 0, len(s.Recipe.sandboxArgs))
	for _, t := range s.Recipe.sandboxArgs {
		a, err := t.Render(s.context())
		if err != nil {
			return nil, err
		}
		args = append(args, a)
	}
	return sandbox.NewOCI(b.GetDir(), s.CLI, b.GetImage(), args), nil
}

func (e *Engine) fetch(ctx context.Context, s *Selection, b *v1.Build, tctx map[string]any, out io.Writer) (string, error) {
	rc := s.Recipe
	dest := filepath.Join(b.GetDir(), srcDir)
	switch {
	case rc.archive != nil:
		rawURL, err := rc.archive.Render(tctx)
		if err != nil {
			return "", err
		}
		subdir := ""
		if rc.subdir != nil {
			if subdir, err = rc.subdir.Render(tctx); err != nil {
				return "", err
			}
		}
		if err := e.fetchArchive(ctx, strings.TrimSpace(rawURL), dest, strings.TrimSpace(subdir), out); err != nil {
			return "", err
		}
		return b.GetRef(), nil
	case rc.repo != nil:
		repo, err := rc.repo.Render(tctx)
		if err != nil {
			return "", err
		}
		// Git moves the tree at its own pace, but not through a paused window
		if e.Fetcher != nil {
			if err := e.Fetcher.Hold(ctx); err != nil {
				return "", err
			}
		}
		return sources.Checkout(ctx, strings.TrimSpace(repo), b.GetRef(), dest, out)
	}
	return b.GetRef(), os.MkdirAll(dest, 0o755)
}

func (e *Engine) patch(ctx context.Context, s *Selection, b *v1.Build, out io.Writer) error {
	tree := filepath.Join(b.GetDir(), srcDir)
	selected := map[string]bool{}
	for _, id := range b.GetPatches() {
		selected[id] = true
	}
	for _, p := range s.Recipe.patches {
		if !selected[p.spec.GetId()] {
			continue
		}
		var data []byte
		switch {
		case p.spec.GetContent() != "":
			data = []byte(p.spec.GetContent())
		case p.spec.GetFile() != "":
			data = e.Patches[p.spec.GetFile()]
		default:
			cached := filepath.Join(e.Root, cacheDirName, cacheName(p.spec.GetUrl()))
			if err := e.download(ctx, p.spec.GetUrl(), cached, out); err != nil {
				return fmt.Errorf("patch %s: %w", p.spec.GetId(), err)
			}
			var err error
			if data, err = os.ReadFile(cached); err != nil {
				return err
			}
		}
		parsed, err := parseUnified(data)
		if err != nil {
			return fmt.Errorf("patch %s: %w", p.spec.GetId(), err)
		}
		strip := int(p.spec.GetStrip())
		if p.spec.GetStrip() == 0 {
			strip = 1
		}
		touched, err := applyPatches(tree, parsed, strip)
		if err != nil {
			return fmt.Errorf("patch %s: %w", p.spec.GetId(), err)
		}
		fmt.Fprintf(out, "patch %s applied to %s\n", p.spec.GetId(), strings.Join(touched, ", "))
	}
	return nil
}

// Copies outputs into out and checks the binary is present
func (e *Engine) collect(s *Selection, b *v1.Build, tctx map[string]any, out io.Writer) error {
	tree := filepath.Join(b.GetDir(), srcDir)
	dest := filepath.Join(b.GetDir(), outDir)
	for _, t := range s.Recipe.outputs {
		pattern, err := t.Render(tctx)
		if err != nil {
			return err
		}
		pattern = strings.TrimSpace(pattern)
		matches, err := filepath.Glob(filepath.Join(tree, filepath.FromSlash(pattern)))
		if err != nil {
			return err
		}
		if len(matches) == 0 && !strings.ContainsAny(pattern, "*?[") {
			return fmt.Errorf("output %s not produced", pattern)
		}
		for _, m := range matches {
			if err := copyEntry(m, filepath.Join(dest, filepath.Base(m))); err != nil {
				return err
			}
		}
	}
	binary, err := s.Recipe.binary.Render(tctx)
	if err != nil {
		return err
	}
	b.Binary = filepath.Join(dest, filepath.FromSlash(strings.TrimSpace(binary)))
	info, err := os.Stat(b.Binary)
	if err != nil || info.IsDir() {
		return fmt.Errorf("binary %s not produced", binary)
	}
	if err := os.Chmod(b.Binary, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(out, "binary %s\n", b.Binary)
	return nil
}

// Copies a file or directory tree, preserving symlinks and modes
func copyEntry(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		os.Remove(dst)
		return os.Symlink(target, dst)
	case info.IsDir():
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyEntry(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return archive.WriteFile(dst, in, info.Mode().Perm())
}

// Expression env for step conditions, host facts plus vars
func stepEnv(s *Selection, tctx map[string]any) map[string]any {
	env := make(map[string]any, len(s.env)+4)
	for k, v := range s.env {
		env[k] = v
	}
	vars := make(map[string]any, len(s.Vars))
	for k, v := range s.Vars {
		vars[k] = v
	}
	env["vars"] = vars
	env["variant"] = s.Variant.GetId()
	env["ref"] = s.Ref
	env["sandbox"] = eval.EnumShort(s.Sandbox)
	return env
}
