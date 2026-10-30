package enroll

import (
	"context"
	"sync"
	"time"
)

// Limiter — счётчики частоты (в production — Valkey, общий для всех инстансов; подключает вызывающий).
type Limiter interface {
	// Hit увеличивает счётчик key в окне window; ok=false — лимит превышен, retryAfter — до конца окна.
	Hit(ctx context.Context, key string, limit int, window time.Duration) (ok bool, retryAfter time.Duration, err error)
	// Exceeded — превышен ли лимит, без увеличения счётчика.
	Exceeded(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error)
}

// MemoryLimiter — фиксированное окно в памяти инстанса. Годится для dev и тестов и как запасной
// вариант; при нескольких инстансах лимит фактически умножается на их число.
type MemoryLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
	sweep   time.Time
}

type bucket struct {
	count int
	reset time.Time
}

// NewMemoryLimiter — лимитер в памяти.
func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{buckets: map[string]*bucket{}, now: time.Now}
}

func (l *MemoryLimiter) get(key string, window time.Duration) *bucket {
	now := l.now()
	if now.After(l.sweep) {
		for k, b := range l.buckets {
			if now.After(b.reset) {
				delete(l.buckets, k)
			}
		}
		l.sweep = now.Add(time.Minute)
	}
	b := l.buckets[key]
	if b == nil || now.After(b.reset) {
		b = &bucket{reset: now.Add(window)}
		l.buckets[key] = b
	}
	return b
}

// Hit — Limiter.Hit.
func (l *MemoryLimiter) Hit(_ context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.get(key, window)
	b.count++
	if b.count > limit {
		return false, b.reset.Sub(l.now()), nil
	}
	return true, 0, nil
}

// Exceeded — Limiter.Exceeded.
func (l *MemoryLimiter) Exceeded(_ context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.get(key, window)
	if b.count >= limit {
		return true, b.reset.Sub(l.now()), nil
	}
	return false, 0, nil
}
