// Package transfer downloads blobs in resumable chunks and verifies them.
package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nickheyer/nebu/pkg/sources"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

const (
	stateSuffix = ".json"
	hashBuffer  = 1 << 20
	baseBackoff = time.Second
	maxBackoff  = 30 * time.Second
)

// Returned when downloaded bytes do not match the digest
var ErrDigestMismatch = errors.New("digest mismatch")

// Reports byte deltas, negative when a chunk restarts
type Progress func(delta int64)

// Downloads blobs with resume, parallel chunks, and throttling
type Fetcher struct {
	Workers int
	Chunk   int64
	Retries int
	// Bytes per second by time of week, nil for no limit
	Schedule *Schedule
	Log      *slog.Logger

	mu      sync.Mutex
	limiter *rate.Limiter
}

type state struct {
	Size  int64  `json:"size"`
	Chunk int64  `json:"chunk"`
	Done  []bool `json:"done"`
}

// Builds a fetcher with no limits, Schedule setting the rate and windows
func New(workers int, chunk int64, retries int, log *slog.Logger) *Fetcher {
	return &Fetcher{Workers: max(workers, 1), Chunk: max(chunk, 1<<16), Retries: max(retries, 1), Log: log}
}

// Downloads to dest with resume and reports existing complete files.
func (f *Fetcher) Land(ctx context.Context, blob sources.Blob, dest string, progress Progress) (bool, error) {
	if info, err := os.Stat(dest); err == nil && info.Size() == blob.Size() {
		if progress != nil {
			progress(info.Size())
		}
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false, err
	}
	if _, err := f.Fetch(ctx, blob, dest+".partial", "", progress); err != nil {
		return false, err
	}
	return false, os.Rename(dest+".partial", dest)
}

// Waits for an active transfer window before an unmetered download.
func (f *Fetcher) Hold(ctx context.Context) error { return f.wait(ctx, 0) }

// Rate-limits a read of n bytes and waits through paused windows.
func (f *Fetcher) wait(ctx context.Context, n int) error {
	for {
		bps, pause := f.Schedule.At(time.Now())
		if pause && n > 0 {
			// Close active connections when a paused window begins.
			return errPaused
		}
		if pause {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pausePoll):
				continue
			}
		}
		if bps == 0 || n == 0 {
			return nil
		}
		return f.limiterFor(bps).WaitN(ctx, n)
	}
}

// Returns the token bucket, updating its rate for the current window.
func (f *Fetcher) limiterFor(bps uint64) *rate.Limiter {
	f.mu.Lock()
	defer f.mu.Unlock()
	burst := int(max(bps, 4<<20))
	if f.limiter == nil {
		f.limiter = rate.NewLimiter(rate.Limit(bps), burst)
	} else if f.limiter.Limit() != rate.Limit(bps) {
		f.limiter.SetLimit(rate.Limit(bps))
		f.limiter.SetBurst(burst)
	}
	return f.limiter
}

// Fetches a blob with resume, verifies it, returns the digest
func (f *Fetcher) Fetch(ctx context.Context, blob sources.Blob, partial, expected string, progress Progress) (string, error) {
	if progress == nil {
		progress = func(int64) {}
	}
	size := blob.Size()
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", err
	}
	defer file.Close()
	st, err := f.loadState(partial, size)
	if err != nil {
		return "", err
	}
	if info, err := file.Stat(); err != nil || info.Size() != size {
		if err := file.Truncate(size); err != nil {
			return "", err
		}
	}
	var mu sync.Mutex
	var pending []int
	for i, done := range st.Done {
		if done {
			progress(f.chunkLen(size, i))
		} else {
			pending = append(pending, i)
		}
	}
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(f.Workers)
	for _, i := range pending {
		eg.Go(func() error {
			off := int64(i) * f.Chunk
			if err := f.fetchChunk(gctx, blob, file, off, f.chunkLen(size, i), progress); err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			st.Done[i] = true
			return saveState(partial, st)
		})
	}
	if err := eg.Wait(); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	digest, err := HashFile(partial, nil)
	if err != nil {
		return "", err
	}
	if expected != "" && digest != expected {
		os.Remove(partial)
		os.Remove(partial + stateSuffix)
		return "", fmt.Errorf("%w: got %s want %s", ErrDigestMismatch, digest, expected)
	}
	os.Remove(partial + stateSuffix)
	return digest, nil
}

func (f *Fetcher) chunkLen(size int64, i int) int64 {
	return min(f.Chunk, size-int64(i)*f.Chunk)
}

func (f *Fetcher) chunks(size int64) int {
	if size == 0 {
		return 0
	}
	return int((size + f.Chunk - 1) / f.Chunk)
}

func (f *Fetcher) loadState(partial string, size int64) (*state, error) {
	st := &state{Size: size, Chunk: f.Chunk, Done: make([]bool, f.chunks(size))}
	data, err := os.ReadFile(partial + stateSuffix)
	if err != nil {
		return st, nil
	}
	var saved state
	if json.Unmarshal(data, &saved) == nil && saved.Size == size && saved.Chunk == f.Chunk && len(saved.Done) == len(st.Done) {
		return &saved, nil
	}
	return st, nil
}

func saveState(partial string, st *state) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := partial + stateSuffix + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, partial+stateSuffix)
}

// Downloads a URL to partial with transfer limits and returns its digest. Known sizes with range
// support use resumable chunks. Other downloads restart on failure.
func (f *Fetcher) FetchURL(ctx context.Context, client *sources.HTTP, rawURL, partial string, progress Progress) (string, error) {
	if resp, err := client.Do(ctx, http.MethodHead, rawURL, nil, nil); err == nil {
		resp.Body.Close()
		if resp.ContentLength > 0 && resp.Header.Get("Accept-Ranges") == "bytes" {
			return f.Fetch(ctx, sources.NewRangeBlob(client, rawURL, resp.ContentLength), partial, "", progress)
		}
	}
	if progress == nil {
		progress = func(int64) {}
	}
	err := f.retry(ctx, progress, func() (int64, error) {
		if err := f.wait(ctx, 0); err != nil {
			return 0, err
		}
		resp, err := client.Do(ctx, http.MethodGet, rawURL, nil, nil)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		file, err := os.Create(partial)
		if err != nil {
			return 0, err
		}
		n, err := io.Copy(file, &meter{ctx: ctx, r: resp.Body, fetcher: f, progress: progress})
		if cerr := file.Close(); err == nil {
			err = cerr
		}
		return n, err
	})
	if err != nil {
		return "", err
	}
	return HashFile(partial, nil)
}

// Retries with backoff and reverses failed progress increments.
func (f *Fetcher) retry(ctx context.Context, progress Progress, try func() (int64, error)) error {
	var last error
	for attempt := 0; attempt < max(f.Retries, 1); attempt++ {
		if attempt > 0 {
			f.Log.Debug("retrying", "attempt", attempt, "err", last)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(min(baseBackoff<<(attempt-1), maxBackoff)):
			}
		}
		written, err := try()
		if err == nil {
			return nil
		}
		progress(-written)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Resume paused chunks after the window without counting a failure.
		if errors.Is(err, errPaused) {
			attempt--
			continue
		}
		last = err
	}
	return last
}

func (f *Fetcher) fetchChunk(ctx context.Context, blob sources.Blob, file *os.File, off, length int64, progress Progress) error {
	return f.retry(ctx, progress, func() (int64, error) {
		written, err := f.copyChunk(ctx, blob, file, off, length, progress)
		if err == nil && written != length {
			err = fmt.Errorf("short chunk at %d: %d of %d bytes", off, written, length)
		}
		return written, err
	})
}

func (f *Fetcher) copyChunk(ctx context.Context, blob sources.Blob, file *os.File, off, length int64, progress Progress) (int64, error) {
	// Wait through pauses before opening the connection.
	if err := f.wait(ctx, 0); err != nil {
		return 0, err
	}
	rc, err := sources.RangeOf(ctx, blob, off, length)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	reader := &meter{ctx: ctx, r: io.LimitReader(rc, length), fetcher: f, progress: progress}
	return io.Copy(io.NewOffsetWriter(file, off), reader)
}

// Signals that a paused window began while bytes were flowing
var errPaused = errors.New("transfer paused by a window")

// Reports progress and applies the rate limit as bytes flow
type meter struct {
	ctx      context.Context
	r        io.Reader
	fetcher  *Fetcher
	progress Progress
}

func (m *meter) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	if n > 0 {
		if lerr := m.fetcher.wait(m.ctx, n); lerr != nil {
			return n, lerr
		}
		m.progress(int64(n))
	}
	return n, err
}

// Hashes a file, reporting bytes read when progress is given
func HashFile(path string, progress Progress) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	buf := make([]byte, hashBuffer)
	for {
		n, err := file.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if progress != nil {
				progress(int64(n))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
