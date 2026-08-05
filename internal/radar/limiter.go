package radar

import (
	"context"
	"sync"
	"time"
)

// Limiter is a token bucket that paces outbound requests to one platform.
//
// It is hand-written rather than pulled from golang.org/x/time/rate, which does
// this better. The reason is the dependency budget: this repository has six
// direct dependencies and a static build with no cgo, and forty lines that are
// exercised by a test are cheaper to own than a module in the supply chain for
// one call site. If a second caller ever needs weighted reservations or
// per-request cost, that trade flips.
type Limiter struct {
	mu sync.Mutex
	// interval is the minimum gap between two grants.
	interval time.Duration
	// burst is how many grants may be taken back to back after an idle period.
	burst int
	// tokens is fractional so that an idle period shorter than one interval
	// still accrues its share, instead of being rounded away.
	tokens float64
	last   time.Time
	// now is injected so the test can prove the pacing without sleeping
	// through it.
	now func() time.Time
}

// NewLimiter builds a limiter allowing perMinute requests per minute, with the
// given burst.
//
// A non-positive perMinute means unlimited, which is what a test double or a
// source hitting a local fixture wants; it is not treated as "block forever",
// because a zero-valued configuration should not deadlock the poller.
func NewLimiter(perMinute float64, burst int) *Limiter {
	l := &Limiter{burst: burst, now: time.Now}
	if l.burst < 1 {
		l.burst = 1
	}
	if perMinute > 0 {
		l.interval = time.Duration(float64(time.Minute) / perMinute)
	}
	l.tokens = float64(l.burst)
	l.last = l.now()
	return l
}

// Wait blocks until the next request may be sent, or until ctx is done.
func (l *Limiter) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delay := l.reserve()
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// reserve takes a token and returns how long the caller must wait before using
// it. Tokens are allowed to go negative: the request is granted, but the debt
// is repaid before the next one, which keeps a burst of callers in order rather
// than letting them all race for the same token.
func (l *Limiter) reserve() time.Duration {
	if l.interval <= 0 {
		return 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	elapsed := now.Sub(l.last)
	l.last = now
	l.tokens = min(l.tokens+float64(elapsed)/float64(l.interval), float64(l.burst))

	l.tokens--
	if l.tokens >= 0 {
		return 0
	}
	return time.Duration(-l.tokens * float64(l.interval))
}
