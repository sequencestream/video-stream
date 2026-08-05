package radar

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// baseBackoff is the first pause after a platform rate-limits us. It doubles
// per consecutive refusal up to maxBackoff.
const baseBackoff = 2 * time.Minute

// maxBackoff caps the pause. Past an hour the radar has effectively stopped
// watching that platform, and continuing to double only delays the moment
// someone notices.
const maxBackoff = time.Hour

// Source fetches public metrics for one platform.
//
// No implementation ships with this package, and that is the design rather than
// an omission: a scraper for a platform that does not offer an API is the grey
// area this module was scoped to stay out of. The interface exists so that an
// official API, or a user-supplied exporter, can be plugged in without the
// polling, pacing and backoff machinery being rewritten around it.
type Source interface {
	// Platform is the identifier that matches Account.Platform.
	Platform() string
	// Recent returns readings of the account's posts published at or after
	// since. It must return ErrRateLimited, wrapped or not, when the platform
	// refuses for rate reasons; any other error is treated as a transient fault
	// affecting only this account.
	Recent(ctx context.Context, a Account, since time.Time) ([]Reading, error)
}

// PollResult is what one polling round did.
type PollResult struct {
	// Readings are the metrics collected, across every account polled.
	Readings []Reading
	// Polled is how many accounts were successfully read.
	Polled int
	// NoSource is how many accounts were skipped for having no registered
	// source. In the MVP this is normally every account, and it is counted
	// rather than logged as an error because it is the expected state.
	NoSource int
	// Failed is how many accounts errored for reasons other than rate limiting.
	Failed int
	// RateLimited is how many platforms asked us to slow down.
	RateLimited int
}

// Poller reads watched accounts through their platform sources, at a pace each
// platform will tolerate.
type Poller struct {
	mu           sync.Mutex
	sources      map[string]Source
	limiters     map[string]*Limiter
	backoff      map[string]time.Duration
	blockedUntil map[string]time.Time

	perMinute float64
	burst     int
	logger    *slog.Logger
	now       func() time.Time
}

// NewPoller builds a poller pacing each platform at perMinute requests per
// minute.
func NewPoller(perMinute float64, burst int, logger *slog.Logger) *Poller {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Poller{
		sources:      map[string]Source{},
		limiters:     map[string]*Limiter{},
		backoff:      map[string]time.Duration{},
		blockedUntil: map[string]time.Time{},
		perMinute:    perMinute,
		burst:        burst,
		logger:       logger,
		now:          time.Now,
	}
}

// Register adds a source. Registering a second source for a platform replaces
// the first.
func (p *Poller) Register(s Source) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sources[s.Platform()] = s
}

// Poll reads every account once, in order, respecting each platform's pace.
//
// Accounts are read one at a time rather than concurrently. Concurrency would
// finish sooner and is the wrong trade here: the binding constraint is the
// platform's rate limit, not our own throughput, and parallel workers sharing
// one limiter only make the pacing harder to reason about while producing the
// same total duration.
func (p *Poller) Poll(ctx context.Context, accounts []Account, since time.Time) (PollResult, error) {
	var result PollResult

	for _, account := range accounts {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		source, ok := p.sourceFor(account.Platform)
		if !ok {
			result.NoSource++
			continue
		}
		if p.blocked(account.Platform) {
			result.NoSource++
			continue
		}

		if err := p.limiterFor(account.Platform).Wait(ctx); err != nil {
			return result, err
		}

		readings, err := source.Recent(ctx, account, since)
		switch {
		case errors.Is(err, ErrRateLimited):
			// Stop reading this platform for a while. Retrying immediately is
			// how a rate limit becomes a ban, and the accounts we would skip
			// are still there next round.
			pause := p.penalise(account.Platform)
			result.RateLimited++
			p.logger.WarnContext(ctx, "radar source rate limited",
				slog.String("platform", account.Platform),
				slog.Duration("backoff", pause))
		case err != nil:
			result.Failed++
			p.logger.WarnContext(ctx, "radar source failed",
				slog.String("platform", account.Platform),
				slog.String("account", account.Handle),
				slog.String("error", err.Error()))
		default:
			p.clearPenalty(account.Platform)
			result.Polled++
			result.Readings = append(result.Readings, readings...)
		}
	}
	return result, nil
}

func (p *Poller) sourceFor(platform string) (Source, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sources[platform]
	return s, ok
}

func (p *Poller) limiterFor(platform string) *Limiter {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.limiters[platform]; ok {
		return l
	}
	// Per platform, not shared. A shared limiter would let a chatty platform
	// spend the budget of a quiet one, and the limits being respected belong to
	// each platform separately.
	l := NewLimiter(p.perMinute, p.burst)
	p.limiters[platform] = l
	return l
}

func (p *Poller) blocked(platform string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.blockedUntil[platform]
	return ok && p.now().Before(until)
}

// penalise doubles the platform's backoff and returns the new pause.
func (p *Poller) penalise(platform string) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	pause := p.backoff[platform] * 2
	if pause < baseBackoff {
		pause = baseBackoff
	}
	pause = min(pause, maxBackoff)
	p.backoff[platform] = pause
	p.blockedUntil[platform] = p.now().Add(pause)
	return pause
}

// clearPenalty resets the backoff after a successful read.
func (p *Poller) clearPenalty(platform string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.backoff, platform)
	delete(p.blockedUntil, platform)
}
