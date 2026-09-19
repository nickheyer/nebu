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

// Hugging Face CLI transport using hf download into scratch storage. Locators use
// repo@revision/path. Files download on first access. Listing and range reads are unsupported.
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

// Cannot list, the Hub API does that
func (h *HFCLI) List(context.Context, string) ([]*v1.Artifact, error) {
	return nil, fmt.Errorf("listing through the CLI: %w", ErrUnsupported)
}

// Creates a blob downloaded on first read or materialization.
func (h *HFCLI) Open(ctx context.Context, locator string, size int64) (Blob, error) {
	repo, ref, rel := splitRepoRef(locator)
	if repo == "" || rel == "" {
		return nil, fmt.Errorf("locator %q: want repo@revision/path", locator)
	}
	return &lazyBlob{size: size, land: func(ctx context.Context, progress func(int64)) (Blob, error) {
		path, err := h.download(ctx, repo, ref, rel, progress)
		if err != nil {
			return nil, err
		}
		return openFile(path, true)
	}}, nil
}

// Downloads a file and reads it whole, capped
func (h *HFCLI) Read(ctx context.Context, locator string, max int64) ([]byte, error) {
	b, err := h.Open(ctx, locator, 0)
	if err != nil {
		return nil, err
	}
	defer b.Close()
	path, err := b.(Materializer).Materialize(ctx, nil)
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

// Reports file growth until the CLI download completes.
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
