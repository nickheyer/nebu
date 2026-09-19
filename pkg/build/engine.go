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
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/recipes"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/text"
	"github.com/nickheyer/nebu/pkg/transfer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const cacheDirName = "cache"

// Reports which step is running
type Progress func(done, total int, name string)

// Runs builds under one root directory
type Engine struct {
	Root string
	Jobs int
	// Where a latest ref reads releases from
	Sources *sources.Registry
	// Moves archives and patches under the transfer limits
	Fetcher *transfer.Fetcher
	Log     *slog.Logger
}

// Creates a build record without network access.
func (s *Selection) Build() *v1.Build {
	return &v1.Build{
		RecipeId:  s.Recipe.ID(),
		RuntimeId: s.Recipe.RuntimeID(),
		Variant:   s.Variant.ID,
		Ref:       s.Ref,
		Sandbox:   s.Sandbox,
		Image:     s.Image,
		Vars:      s.Vars,
		Facts:     s.Facts,
		CreatedAt: timestamppb.Now(),
	}
}

// Resolved build inputs using sandbox paths.
func (s *Selection) context(b *v1.Build, runner sandbox.Runner, jobs int) *recipes.Build {
	out := &recipes.Build{Ref: b.GetRef(), Commit: b.GetCommit(), Variant: b.GetVariant(), Vars: b.GetVars(), Jobs: jobs, Host: s.profile}
	if runner != nil {
		out.Root, out.Src, out.Out = runner.Path(""), runner.Path(srcDir), runner.Path(outDir)
	}
	return out
}

// Resolves the ref, selects patches, and assigns id and directory
func (e *Engine) Resolve(ctx context.Context, s *Selection, b *v1.Build) error {
	rc := s.Recipe
	src := rc.Source()
	if src.Releases != "" && (b.Ref == "" || b.Ref == Latest) {
		tag, err := latestTag(ctx, e.Sources, recipes.ReleaseSource, src.Releases)
		if err != nil {
			return fmt.Errorf("resolve release: %w", err)
		}
		b.Ref = tag
	}
	if src.Archive != nil && b.Ref == "" {
		return fmt.Errorf("%w: recipe %s needs a ref for its archive", ErrSelection, rc.ID())
	}
	s.Ref = b.Ref
	b.Vars = s.Vars
	if b.Image != "" || s.Sandbox != v1.SandboxKind_SANDBOX_KIND_OCI {
		b.Image = s.Image
	}
	b.Patches = nil
	contents := map[string][]byte{}
	bctx := s.context(b, nil, 1)
	for _, p := range rc.Patches() {
		if p.Applies == nil || p.Applies(bctx) {
			b.Patches = append(b.Patches, p.ID)
		}
		if len(p.Content) > 0 {
			contents[p.ID] = p.Content
		} else {
			contents[p.ID] = []byte(p.URL)
		}
	}
	b.Id = hashBuild(rc, b, s.context(b, nil, 1), contents)
	b.Dir = filepath.Join(e.Root, rc.RuntimeID(), b.Id)
	return nil
}

// Fetches, patches, runs steps, and collects outputs, writing a transcript
func (e *Engine) Run(ctx context.Context, s *Selection, b *v1.Build, out io.Writer, progress Progress) error {
	rc := s.Recipe
	if progress == nil {
		progress = func(int, int, string) {}
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout(rc))
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
	fmt.Fprintf(out, "build %s recipe %s variant %s ref %s sandbox %s\n", b.GetId(), rc.ID(), b.GetVariant(), b.GetRef(), text.Enum(b.GetSandbox()))
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
	bctx := s.context(b, runner, jobs)
	steps := rc.Steps(bctx)
	total := len(steps) + 3
	progress(0, total, "fetching source")
	commit, err := e.fetch(ctx, s, b, out)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	b.Commit = commit
	bctx.Commit = commit
	progress(1, total, "applying patches")
	if err := e.patch(ctx, s, b, out); err != nil {
		return err
	}
	for i, st := range rc.Steps(bctx) {
		name := st.Name
		if name == "" {
			name = fmt.Sprintf("step %d", i+1)
		}
		if len(st.Command) == 0 {
			return fmt.Errorf("%s: empty command", name)
		}
		progress(2+i, total, name)
		dir := srcDir
		if st.Dir != "" {
			dir = filepath.Join(srcDir, filepath.FromSlash(st.Dir))
		}
		fmt.Fprintf(out, "--- %s: %s\n", name, strings.Join(st.Command, " "))
		started := time.Now()
		if err := runner.Run(ctx, sandbox.Step{Name: name, Command: st.Command, Env: st.Env, Dir: dir}, out); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%s: %w", name, err)
		}
		fmt.Fprintf(out, "--- %s done in %s\n", name, time.Since(started).Round(time.Second))
	}
	progress(total-1, total, "collecting outputs")
	if err := e.collect(s, b, out); err != nil {
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
	return sandbox.NewOCI(b.GetDir(), s.CLI, b.GetImage(), s.Recipe.Sandbox().Args), nil
}

func (e *Engine) fetch(ctx context.Context, s *Selection, b *v1.Build, out io.Writer) (string, error) {
	src := s.Recipe.Source()
	dest := filepath.Join(b.GetDir(), srcDir)
	switch {
	case src.Archive != nil:
		if err := e.fetchArchive(ctx, src.Archive(b.GetRef()), dest, src.Subdir, out); err != nil {
			return "", err
		}
		return b.GetRef(), nil
	case src.Repo != "":
		// Respect paused windows before starting git.
		if e.Fetcher != nil {
			if err := e.Fetcher.Hold(ctx); err != nil {
				return "", err
			}
		}
		return sources.Checkout(ctx, src.Repo, b.GetRef(), dest, out)
	}
	return b.GetRef(), os.MkdirAll(dest, 0o755)
}

func (e *Engine) patch(ctx context.Context, s *Selection, b *v1.Build, out io.Writer) error {
	tree := filepath.Join(b.GetDir(), srcDir)
	selected := map[string]bool{}
	for _, id := range b.GetPatches() {
		selected[id] = true
	}
	for _, p := range s.Recipe.Patches() {
		if !selected[p.ID] {
			continue
		}
		data := p.Content
		if len(data) == 0 {
			cached := filepath.Join(e.Root, cacheDirName, cacheName(p.URL))
			if err := e.download(ctx, p.URL, cached, out); err != nil {
				return fmt.Errorf("patch %s: %w", p.ID, err)
			}
			var err error
			if data, err = os.ReadFile(cached); err != nil {
				return err
			}
		}
		parsed, err := parseUnified(data)
		if err != nil {
			return fmt.Errorf("patch %s: %w", p.ID, err)
		}
		strip := p.Strip
		if strip == 0 {
			strip = 1
		}
		touched, err := applyPatches(tree, parsed, strip)
		if err != nil {
			return fmt.Errorf("patch %s: %w", p.ID, err)
		}
		fmt.Fprintf(out, "patch %s applied to %s\n", p.ID, strings.Join(touched, ", "))
	}
	return nil
}

// Copies outputs into out and checks the binary is present
func (e *Engine) collect(s *Selection, b *v1.Build, out io.Writer) error {
	tree := filepath.Join(b.GetDir(), srcDir)
	dest := filepath.Join(b.GetDir(), outDir)
	for _, pattern := range s.Recipe.Outputs() {
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
	binary := strings.TrimSpace(s.Recipe.Binary())
	b.Binary = filepath.Join(dest, filepath.FromSlash(binary))
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
