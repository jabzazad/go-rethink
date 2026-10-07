package decision

import (
	"context"
	"sync"
	"time"
)

// RateLimiter allows at most N calls per sliding minute. It mirrors a provider's
// per-minute limit (e.g. Vercel's free tier: 5 requests/min per model) so we fail
// fast and fall back locally instead of collecting 429s.
type RateLimiter struct {
	mu    sync.Mutex
	rpm   int
	calls []time.Time
}

func NewRateLimiter(rpm int) *RateLimiter { return &RateLimiter{rpm: rpm} }

func (l *RateLimiter) RPM() int { return l.rpm }

// TryAcquire takes a slot if one is free right now.
func (l *RateLimiter) TryAcquire() bool {
	ok, _ := l.try(time.Now())
	return ok
}

// Wait blocks until a slot is free (or ctx ends), then takes it.
func (l *RateLimiter) Wait(ctx context.Context) error {
	for {
		ok, retry := l.try(time.Now())
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retry):
		}
	}
}

func (l *RateLimiter) try(now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-time.Minute)
	i := 0
	for i < len(l.calls) && !l.calls[i].After(cut) {
		i++
	}
	l.calls = l.calls[i:]
	if len(l.calls) < l.rpm {
		l.calls = append(l.calls, now)
		return true, 0
	}
	return false, l.calls[0].Sub(cut) + 10*time.Millisecond
}

type pacedKey struct{}

// WithPaced marks ctx so rate-limited deciders wait for a slot instead of failing fast.
func WithPaced(ctx context.Context) context.Context { return context.WithValue(ctx, pacedKey{}, true) }

func isPaced(ctx context.Context) bool { v, _ := ctx.Value(pacedKey{}).(bool); return v }
