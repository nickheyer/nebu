package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nickheyer/nebu/pkg/archive"
	"github.com/nickheyer/nebu/pkg/sources"
)

const downloadAttempts = 3

var commitLike = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// Picks the newest tag from a GitHub style release feed
func latestTag(ctx context.Context, feed string) (string, error) {
	client, err := sources.NewHTTP(feed, "")
	if err != nil {
		return "", err
	}
	var root any
	if _, err := client.JSON(ctx, feed, nil, &root); err != nil {
		return "", err
	}
	releases, ok := root.([]any)
	if !ok {
		releases = []any{root}
	}
	fallback := ""
	for _, r := range releases {
		rel, ok := r.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := rel["tag_name"].(string)
		if tag == "" {
			continue
		}
		draft, _ := rel["draft"].(bool)
		pre, _ := rel["prerelease"].(bool)
		if !draft && !pre {
			return tag, nil
		}
		if fallback == "" {
			fallback = tag
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("no release with a tag in %s", feed)
}

// Streams a URL into dest, reusing an existing complete file
func download(ctx context.Context, rawURL, dest string, out io.Writer) error {
	if info, err := os.Stat(dest); err == nil && info.Size() > 0 {
		fmt.Fprintf(out, "using cached %s\n", filepath.Base(dest))
		return nil
	}
	client, err := sources.NewHTTP(rawURL, "")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	var last error
	for attempt := 0; attempt < downloadAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		last = copyURL(ctx, client, rawURL, dest)
		if last == nil {
			return nil
		}
		fmt.Fprintf(out, "download failed: %v\n", last)
	}
	return last
}

func copyURL(ctx context.Context, client *sources.HTTP, rawURL, dest string) error {
	resp, err := client.Do(ctx, http.MethodGet, rawURL, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp := dest + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// Names a cache file for a URL, keeping the extension
func cacheName(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	base := path.Base(strings.SplitN(rawURL, "?", 2)[0])
	ext := ""
	for _, candidate := range []string{".tar.gz", ".tgz", ".tar", ".zip", ".patch", ".diff"} {
		if strings.HasSuffix(strings.ToLower(base), candidate) {
			ext = candidate
			break
		}
	}
	return hex.EncodeToString(sum[:8]) + ext
}

// Downloads and unpacks an archive so dest holds the tree
func fetchArchive(ctx context.Context, rawURL, cacheDir, dest, subdir string, out io.Writer) error {
	cached := filepath.Join(cacheDir, cacheName(rawURL))
	fmt.Fprintf(out, "fetching %s\n", rawURL)
	if err := download(ctx, rawURL, cached, out); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	if err := archive.Extract(cached, tmp); err != nil {
		os.Remove(cached)
		return fmt.Errorf("extract %s: %w", filepath.Base(cached), err)
	}
	tree := archive.Root(tmp)
	if subdir != "" {
		tree = filepath.Join(tmp, filepath.FromSlash(subdir))
		if info, err := os.Stat(tree); err != nil || !info.IsDir() {
			return fmt.Errorf("archive has no directory %s", subdir)
		}
	}
	os.RemoveAll(dest)
	if err := os.Rename(tree, dest); err != nil {
		return err
	}
	os.RemoveAll(tmp)
	return nil
}

// Clones a repository at ref into dest, returning the commit
func fetchGit(ctx context.Context, repo, ref, dest string, out io.Writer) (string, error) {
	os.RemoveAll(dest)
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Stdout, cmd.Stderr = out, out
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		return cmd.Run()
	}
	clone := func(args ...string) error {
		return run(append([]string{"clone"}, args...)...)
	}
	fmt.Fprintf(out, "cloning %s at %s\n", repo, ref)
	switch {
	case ref == "" || ref == Latest:
		if err := clone("--depth", "1", repo, dest); err != nil {
			return "", err
		}
	case commitLike.MatchString(ref):
		if err := clone(repo, dest); err != nil {
			return "", err
		}
		if err := run("-C", dest, "checkout", "--quiet", ref); err != nil {
			return "", err
		}
	default:
		if err := clone("--depth", "1", "--branch", ref, repo, dest); err != nil {
			return "", err
		}
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dest, "rev-parse", "HEAD")
	data, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
