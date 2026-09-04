package sources

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const hfcliPoll = 500 * time.Millisecond

// The Hugging Face CLI as a transport: whole files land in a scratch
// directory through hf download, which uses Xet and parallel transfers the
// Hub tunes for its own repositories
//
// A locator is repo@revision/path. The CLI cannot list or read ranges, so
// listing answers ErrUnsupported and a blob opened through it materializes
// on first use.
type HFCLI struct {
	command  string
	endpoint string
	token    string
	dir      string
}

func newHFCLITransport(cfg map[string]string, env transportEnv) (Transport, error) {
	if cfg["command"] == "" {
		return nil, nil
	}
	if _, err := exec.LookPath(cfg["command"]); err != nil {
		return nil, err
	}
	return &HFCLI{command: cfg["command"], endpoint: cfg["endpoint"], token: envValue(cfg, "token_env"), dir: filepath.Join(env.cacheDir, TransportHFCLI)}, nil
}

// Splits repo@revision/path into its parts
func splitRepoRef(locator string) (repo, ref, rest string) {
	repo, tail, ok := strings.Cut(locator, "@")
	if !ok {
		return locator, "", ""
	}
	ref, rest, _ = strings.Cut(tail, "/")
	return repo, ref, rest
}

// Cannot list, the Hub API does that
func (h *HFCLI) List(context.Context, string) ([]*v1.Artifact, error) {
	return nil, fmt.Errorf("listing through the CLI: %w", ErrUnsupported)
}

// Opens a file the CLI will download when the blob is materialized or read
func (h *HFCLI) Open(ctx context.Context, locator string, size int64) (Blob, error) {
	repo, ref, rel := splitRepoRef(locator)
	if repo == "" || rel == "" {
		return nil, fmt.Errorf("locator %q: want repo@revision/path", locator)
	}
	return &cliBlob{cli: h, repo: repo, ref: ref, rel: rel, size: size}, nil
}

// Downloads a file and reads it whole, capped
func (h *HFCLI) Read(ctx context.Context, locator string, max int64) ([]byte, error) {
	b, err := h.Open(ctx, locator, 0)
	if err != nil {
		return nil, err
	}
	defer b.Close()
	path, err := b.(*cliBlob).Materialize(ctx, nil)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readAllCapped(f, max)
}

// Runs the download, reporting the growing file until the CLI returns
func (h *HFCLI) download(ctx context.Context, repo, ref, rel string, progress func(int64)) (string, error) {
	local := filepath.Join(h.dir, strings.ReplaceAll(repo, "/", "--"), ref)
	if err := os.MkdirAll(local, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(local, filepath.FromSlash(rel))
	os.Remove(dest)
	args := []string{"download", repo, rel, "--local-dir", local, "--quiet"}
	if ref != "" {
		args = append(args, "--revision", ref)
	}
	cmd := exec.CommandContext(ctx, h.command, args...)
	cmd.Env = append(os.Environ(), "HF_HUB_DISABLE_PROGRESS_BARS=1", "HF_HUB_DISABLE_TELEMETRY=1", "HF_HUB_ENABLE_HF_TRANSFER=0")
	if h.endpoint != "" {
		cmd.Env = append(cmd.Env, "HF_ENDPOINT="+h.endpoint)
	}
	if h.token != "" {
		cmd.Env = append(cmd.Env, "HF_TOKEN="+h.token)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%s: %w", h.command, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// The CLI writes into .cache/huggingface under the local dir before moving the file into place
	incomplete := filepath.Join(local, ".cache", "huggingface", "download", filepath.FromSlash(rel))
	var seen int64
	report := func() {
		var size int64
		for _, p := range []string{dest, incomplete + ".incomplete", incomplete} {
			if info, err := os.Stat(p); err == nil && info.Size() > size {
				size = info.Size()
			}
		}
		if matches, _ := filepath.Glob(incomplete + "*.incomplete"); len(matches) > 0 {
			for _, m := range matches {
				if info, err := os.Stat(m); err == nil && info.Size() > size {
					size = info.Size()
				}
			}
		}
		if size > seen && progress != nil {
			progress(size - seen)
			seen = size
		}
	}
	ticker := time.NewTicker(hfcliPoll)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			report()
			if err != nil {
				return "", fmt.Errorf("%s download %s: %w: %s", h.command, rel, err, strings.TrimSpace(stderr.String()))
			}
			if _, err := os.Stat(dest); err != nil {
				return "", fmt.Errorf("%s download %s: file not written", h.command, rel)
			}
			return dest, nil
		case <-ticker.C:
			report()
		}
	}
}

// A file the CLI downloads on demand, read in place afterwards
type cliBlob struct {
	cli  *HFCLI
	repo string
	ref  string
	rel  string
	size int64
	file Blob
}

// Downloads the file once and returns where it landed
func (b *cliBlob) Materialize(ctx context.Context, progress func(int64)) (string, error) {
	if b.file == nil {
		path, err := b.cli.download(ctx, b.repo, b.ref, b.rel, progress)
		if err != nil {
			return "", err
		}
		if b.file, err = openFile(path, true); err != nil {
			return "", err
		}
		b.size = b.file.Size()
	}
	return b.file.(*fileBlob).Name(), nil
}

func (b *cliBlob) ReadAt(p []byte, off int64) (int, error) {
	if _, err := b.Materialize(context.Background(), nil); err != nil {
		return 0, err
	}
	return b.file.ReadAt(p, off)
}

func (b *cliBlob) Size() int64 { return b.size }

func (b *cliBlob) Close() error {
	if b.file != nil {
		return b.file.Close()
	}
	return nil
}
