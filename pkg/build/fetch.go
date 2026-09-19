package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/nickheyer/nebu/pkg/archive"
	"github.com/nickheyer/nebu/pkg/sources"
)

// Returns the source's default release revision.
func latestTag(ctx context.Context, srcs *sources.Registry, sourceID, repo string) (string, error) {
	if srcs == nil {
		return "", fmt.Errorf("no sources to read releases from")
	}
	src, err := srcs.Get(sourceID)
	if err != nil {
		return "", err
	}
	revisions, err := src.Revisions(ctx, repo)
	if err != nil {
		return "", err
	}
	for _, r := range revisions {
		if r.GetDefault() {
			return r.GetName(), nil
		}
	}
	if len(revisions) > 0 {
		return revisions[0].GetName(), nil
	}
	return "", fmt.Errorf("no release with a tag in %s at %s", repo, src.Spec().GetId())
}

// Downloads with transfer limits, resume, and retries, reusing complete files.
func (e *Engine) download(ctx context.Context, rawURL, dest string, out io.Writer) error {
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
	if _, err := e.Fetcher.FetchURL(ctx, client, rawURL, dest+".partial", nil); err != nil {
		return err
	}
	return os.Rename(dest+".partial", dest)
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
func (e *Engine) fetchArchive(ctx context.Context, rawURL, dest, subdir string, out io.Writer) error {
	cached := filepath.Join(e.Root, cacheDirName, cacheName(rawURL))
	fmt.Fprintf(out, "fetching %s\n", rawURL)
	if err := e.download(ctx, rawURL, cached, out); err != nil {
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
