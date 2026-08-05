package radar

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

// Store is the persistence the engine needs.
//
// It restates store.RadarStore rather than importing the name so that the
// dependency points the right way: a test double implements this, and the
// engine never learns that SQLite exists.
type Store interface {
	PutAccount(ctx context.Context, a store.RadarAccount) error
	UpdateAccount(ctx context.Context, a store.RadarAccount) error
	Accounts(ctx context.Context, platform string) ([]store.RadarAccount, error)
	Account(ctx context.Context, id string) (store.RadarAccount, error)
	AppendObservations(ctx context.Context, obs []store.RadarObservation) error
	Observations(ctx context.Context, accountID string, since time.Time, limit int) ([]store.RadarObservation, error)
}

// Options configures an Engine. Every field has a working zero value.
type Options struct {
	// Store persists accounts and observations. Nil makes every call return an
	// error rather than silently succeeding: unlike a cache, a missing store
	// here means the user's imported accounts went nowhere.
	Store Store
	// Poller reads registered sources. Nil means nothing is polled, which is
	// the shipped configuration — the radar is fed by import.
	Poller *Poller
	// Reporter receives a radar.scanned event per signal computation.
	Reporter telemetry.Reporter
	Logger   *slog.Logger
}

// Engine imports accounts, ingests metric readings and turns them into signals.
type Engine struct {
	store    Store
	poller   *Poller
	reporter telemetry.Reporter
	logger   *slog.Logger
}

// ErrNoStore means the engine was built without persistence.
var ErrNoStore = errors.New("radar has no store configured")

// New builds an Engine.
func New(opts Options) *Engine {
	e := &Engine{store: opts.Store, poller: opts.Poller, reporter: opts.Reporter, logger: opts.Logger}
	if e.reporter == nil {
		e.reporter = telemetry.Nop()
	}
	if e.logger == nil {
		e.logger = slog.New(slog.DiscardHandler)
	}
	return e
}

// ImportAccount adds one account to the watch list.
//
// The MaxAccounts ceiling is enforced here rather than in the store because it
// is a rate-limit judgement, not a storage invariant: twenty accounts is what
// the polling schedule can read without provoking a platform, and the number
// would move if the schedule did.
func (e *Engine) ImportAccount(ctx context.Context, a Account) (store.RadarAccount, error) {
	if e.store == nil {
		return store.RadarAccount{}, ErrNoStore
	}
	a = a.normalise()

	existing, err := e.store.Accounts(ctx, "")
	if err != nil {
		return store.RadarAccount{}, err
	}
	if len(existing) >= MaxAccounts {
		return store.RadarAccount{}, fmt.Errorf("%w: %d of %d accounts already imported",
			ErrTooManyAccounts, len(existing), MaxAccounts)
	}

	record := store.RadarAccount{
		ID:          newID(),
		Platform:    a.Platform,
		Handle:      a.Handle,
		DisplayName: a.DisplayName,
		Category:    a.Category,
		Followers:   a.Followers,
		Owned:       a.Owned,
		AddedAt:     time.Now().UTC(),
	}
	if err := e.store.PutAccount(ctx, record); err != nil {
		return store.RadarAccount{}, err
	}
	return record, nil
}

// Accounts lists the watch list. An empty platform means every platform.
func (e *Engine) Accounts(ctx context.Context, platform string) ([]store.RadarAccount, error) {
	if e.store == nil {
		return nil, ErrNoStore
	}
	return e.store.Accounts(ctx, platform)
}

// Ingest records readings and returns how many were stored.
//
// Comment bodies are reduced to an unanswered-question count here and are not
// persisted. Keeping the texts would mean this product holds a searchable
// archive of other people's comments in order to compute one ratio, which is a
// liability bought for nothing.
func (e *Engine) Ingest(ctx context.Context, readings []Reading) (int, error) {
	if e.store == nil {
		return 0, ErrNoStore
	}
	if len(readings) == 0 {
		return 0, nil
	}

	observations := make([]store.RadarObservation, 0, len(readings))
	for _, r := range readings {
		if err := r.Validate(); err != nil {
			return 0, err
		}
		if r.ObservedAt.IsZero() {
			r.ObservedAt = time.Now().UTC()
		}
		questions := MeasureQuestionDensity(r.CommentSample)

		observations = append(observations, store.RadarObservation{
			ID:                  newID(),
			AccountID:           r.AccountID,
			PostID:              r.PostID,
			Title:               r.Title,
			DurationSeconds:     r.DurationSeconds,
			PublishedAt:         r.PublishedAt.UTC(),
			ObservedAt:          r.ObservedAt.UTC(),
			Views:               r.Views,
			Likes:               r.Likes,
			Comments:            r.Comments,
			Shares:              r.Shares,
			Saves:               r.Saves,
			CompletionRate:      r.CompletionRate,
			CommentSamples:      questions.Sampled,
			UnansweredQuestions: questions.Unanswered,
		})
	}

	if err := e.store.AppendObservations(ctx, observations); err != nil {
		return 0, err
	}
	return len(observations), nil
}

// PollOnce reads every watched account through its registered source and
// ingests whatever comes back.
//
// With no poller and no sources this is a no-op that reports zero, which is the
// shipped behaviour. It is wired anyway so that adding an official platform API
// later is a Register call rather than a new code path.
func (e *Engine) PollOnce(ctx context.Context) (PollResult, error) {
	if e.store == nil {
		return PollResult{}, ErrNoStore
	}
	if e.poller == nil {
		return PollResult{}, nil
	}

	records, err := e.store.Accounts(ctx, "")
	if err != nil {
		return PollResult{}, err
	}
	accounts := make([]Account, 0, len(records))
	for _, r := range records {
		accounts = append(accounts, accountFromRecord(r))
	}

	result, err := e.poller.Poll(ctx, accounts, e.windowStart())
	if err != nil {
		return result, err
	}
	if len(result.Readings) > 0 {
		if _, err := e.Ingest(ctx, result.Readings); err != nil {
			return result, err
		}
	}
	return result, nil
}

// Query narrows a signal listing.
type Query struct {
	Platform string
	Category string
	// HotOnly drops everything below HotThresholdZ.
	HotOnly bool
	// Limit caps the result. Zero means 100.
	Limit int
}

// Signal is one post, scored.
type Signal struct {
	PostID      string    `json:"post_id"`
	AccountID   string    `json:"account_id"`
	Platform    string    `json:"platform"`
	Handle      string    `json:"handle"`
	Category    string    `json:"category,omitempty"`
	Title       string    `json:"title,omitempty"`
	Followers   int64     `json:"followers"`
	Views       int64     `json:"views"`
	PublishedAt time.Time `json:"published_at"`
	ObservedAt  time.Time `json:"observed_at"`

	Residuals Residuals `json:"residuals"`
	// IdentityMismatch is how far the post outran its own account, in [0,1].
	IdentityMismatch float64         `json:"identity_mismatch"`
	Acceleration     Acceleration    `json:"acceleration"`
	Arbitrage        Arbitrage       `json:"arbitrage"`
	Questions        QuestionDensity `json:"questions"`
}

// Signals scores every post observed inside the window.
//
// The baseline is always fitted over every category, and the query filter is
// applied afterwards. Fitting only the requested slice would make a post's
// score depend on what the caller asked to see, so the same post would be hot
// on one screen and ordinary on another.
func (e *Engine) Signals(ctx context.Context, q Query) ([]Signal, error) {
	if e.store == nil {
		return nil, ErrNoStore
	}

	accountRecords, err := e.store.Accounts(ctx, "")
	if err != nil {
		return nil, err
	}
	accounts := make(map[string]store.RadarAccount, len(accountRecords))
	for _, a := range accountRecords {
		accounts[a.ID] = a
	}

	observations, err := e.store.Observations(ctx, "", e.windowStart(), 0)
	if err != nil {
		return nil, err
	}

	series := groupByPost(observations, accounts)
	latest := make([]Sample, 0, len(series))
	for _, readings := range series {
		latest = append(latest, readings[len(readings)-1])
	}
	baselines := FitBaselines(latest)

	signals := make([]Signal, 0, len(series))
	hot := 0
	for _, readings := range series {
		current := readings[len(readings)-1]
		residuals := Residual(current, baselines[current.Category])
		acceleration := MeasureAcceleration(readings)

		if residuals.Hot {
			hot++
		}
		signals = append(signals, Signal{
			PostID:           current.PostID,
			AccountID:        current.AccountID,
			Platform:         current.Platform,
			Handle:           accounts[current.AccountID].Handle,
			Category:         current.Category,
			Title:            current.Title,
			Followers:        current.Followers,
			Views:            current.Views,
			PublishedAt:      current.PublishedAt,
			ObservedAt:       current.ObservedAt,
			Residuals:        residuals,
			IdentityMismatch: MeasureIdentityMismatch(current, residuals),
			Acceleration:     acceleration,
			Arbitrage:        MeasureArbitrage(current, residuals, acceleration),
			Questions:        density(current.UnansweredQuestions, current.CommentSamples),
		})
	}

	e.report(ctx, len(signals), hot, len(baselines))
	return filterAndRank(signals, q), nil
}

// groupByPost turns raw observations into per-post series, oldest reading
// first, joined to the account that published them.
//
// Posts from an account that is no longer watched are dropped. Their residual
// would need a follower count that is no longer being refreshed, and a stale
// denominator produces a confidently wrong score rather than a missing one.
func groupByPost(observations []store.RadarObservation, accounts map[string]store.RadarAccount) map[string][]Sample {
	series := map[string][]Sample{}
	for _, o := range observations {
		account, ok := accounts[o.AccountID]
		if !ok {
			continue
		}
		key := o.AccountID + "\x00" + o.PostID
		series[key] = append(series[key], sampleOf(o, account))
	}
	for key := range series {
		slices.SortFunc(series[key], func(a, b Sample) int {
			return a.ObservedAt.Compare(b.ObservedAt)
		})
	}
	return series
}

func sampleOf(o store.RadarObservation, a store.RadarAccount) Sample {
	return Sample{
		PostID:              o.PostID,
		AccountID:           o.AccountID,
		Platform:            a.Platform,
		Category:            a.Category,
		Followers:           a.Followers,
		Owned:               a.Owned,
		Title:               o.Title,
		DurationSeconds:     o.DurationSeconds,
		PublishedAt:         o.PublishedAt,
		ObservedAt:          o.ObservedAt,
		Views:               o.Views,
		Likes:               o.Likes,
		Comments:            o.Comments,
		Shares:              o.Shares,
		Saves:               o.Saves,
		CompletionRate:      o.CompletionRate,
		CommentSamples:      o.CommentSamples,
		UnansweredQuestions: o.UnansweredQuestions,
	}
}

// filterAndRank applies the query and orders by score, strongest first.
func filterAndRank(signals []Signal, q Query) []Signal {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}

	out := make([]Signal, 0, len(signals))
	for _, s := range signals {
		if q.Platform != "" && s.Platform != q.Platform {
			continue
		}
		if q.Category != "" && s.Category != q.Category {
			continue
		}
		if q.HotOnly && !s.Residuals.Hot {
			continue
		}
		out = append(out, s)
	}

	// Post id breaks ties so that two posts with identical scores keep a stable
	// order between calls; without it the map iteration upstream would shuffle
	// them and the list would look alive when nothing had changed.
	slices.SortFunc(out, func(a, b Signal) int {
		if c := cmp.Compare(b.Residuals.Score, a.Residuals.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.PostID, b.PostID)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (e *Engine) windowStart() time.Time {
	return time.Now().UTC().AddDate(0, 0, -ObservationWindowDays)
}

// report emits the scan event. A telemetry failure is logged and swallowed for
// the same reason it is in the recompile engine: the observer must not be able
// to break the thing it observes.
func (e *Engine) report(ctx context.Context, posts, hot, categories int) {
	err := telemetry.Report(ctx, e.reporter, "radar.scanned", map[string]any{
		"posts":      posts,
		"hot":        hot,
		"categories": categories,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		e.logger.WarnContext(ctx, "reporting a radar scan failed", slog.String("error", err.Error()))
	}
}

func accountFromRecord(r store.RadarAccount) Account {
	return Account{
		ID:          r.ID,
		Platform:    r.Platform,
		Handle:      r.Handle,
		DisplayName: r.DisplayName,
		Category:    r.Category,
		Followers:   r.Followers,
		Owned:       r.Owned,
	}
}

// newID returns a random identifier for a stored row.
func newID() string {
	var b [16]byte
	// rand.Read on crypto/rand cannot fail as of Go 1.24; it panics instead.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
