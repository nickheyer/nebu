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
	"os"
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
	Limiter *rate.Limiter
	Log     *slog.Logger
}

type state struct {
	Size  int64  `json:"size"`
	Chunk int64  `json:"chunk"`
	Done  []bool `json:"done"`
}

// Builds a fetcher, deriving a limiter from bytes per second
func New(workers int, chunk int64, retries int, bytesPerSecond uint64, log *slog.Logger) *Fetcher {
	f := &Fetcher{Workers: max(workers, 1), Chunk: max(chunk, 1<<16), Retries: max(retries, 1), Log: log}
	if bytesPerSecond > 0 {
		f.Limiter = rate.NewLimiter(rate.Limit(bytesPerSecond), int(max(bytesPerSecond, 4<<20)))
	}
	return f
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

func (f *Fetcher) fetchChunk(ctx context.Context, blob sources.Blob, file *os.File, off, length int64, progress Progress) error {
	var last error
	for attempt := 0; attempt < f.Retries; attempt++ {
		if attempt > 0 {
			f.Log.Debug("retrying chunk", "offset", off, "attempt", attempt, "err", last)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(min(baseBackoff<<(attempt-1), maxBackoff)):
			}
		}
		written, err := f.copyChunk(ctx, blob, file, off, length, progress)
		if err == nil && written == length {
			return nil
		}
		progress(-written)
		if err == nil {
			err = fmt.Errorf("short chunk at %d: %d of %d bytes", off, written, length)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		last = err
	}
	return last
}

func (f *Fetcher) copyChunk(ctx context.Context, blob sources.Blob, file *os.File, off, length int64, progress Progress) (int64, error) {
	rc, err := sources.RangeOf(ctx, blob, off, length)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	reader := &meter{ctx: ctx, r: io.LimitReader(rc, length), limiter: f.Limiter, progress: progress}
	return io.Copy(io.NewOffsetWriter(file, off), reader)
}

// Reports progress and applies the rate limit as bytes flow
type meter struct {
	ctx      context.Context
	r        io.Reader
	limiter  *rate.Limiter
	progress Progress
}

func (m *meter) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	if n > 0 {
		if m.limiter != nil {
			if lerr := m.limiter.WaitN(m.ctx, n); lerr != nil {
				return n, lerr
			}
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
