package sources

import (
	"context"
	"sync"
	"time"
)

// Caches one value for a while, refilling on demand
type Memo[T any] struct {
	TTL time.Duration
	mu  sync.Mutex
	at  time.Time
	val T
	ok  bool
}

// Returns the cached value or fills it, keeping a stale value when filling fails
func (m *Memo[T]) Get(ctx context.Context, fill func(ctx context.Context) (T, error)) (T, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ok && (m.TTL <= 0 || time.Since(m.at) < m.TTL) {
		return m.val, nil
	}
	val, err := fill(ctx)
	if err != nil {
		if m.ok {
			return m.val, nil
		}
		return val, err
	}
	m.val, m.ok, m.at = val, true, time.Now()
	return val, nil
}
