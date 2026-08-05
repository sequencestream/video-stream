package store

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrAccountNotFound is returned when no watched account has the given id.
	ErrAccountNotFound = errors.New("radar account not found")
	// ErrAccountExists is returned when the same (platform, handle) is imported
	// twice. It is distinct from a generic constraint failure so the caller can
	// say "you already watch this account" instead of "database error", which
	// is the difference between a usable import form and a broken one.
	ErrAccountExists = errors.New("radar account already imported")
)

// RadarAccount is one account the user chose to watch.
//
// Accounts are imported by the user, never discovered by crawling. That is the
// whole compliance stance of the radar: every row here exists because someone
// typed a handle in, so there is no question of what we are allowed to read.
type RadarAccount struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	// Handle is the account's identifier on the platform. Paired with Platform
	// it is unique; on its own it is not, because the same creator name on two
	// platforms is two audiences.
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name,omitempty"`
	// Category is the baseline bucket. Two accounts only get compared against
	// each other when they share one: a cooking channel's normal view count is
	// a tech channel's viral hit, and pooling them makes both baselines wrong.
	Category string `json:"category,omitempty"`
	// Followers is the account's size, the first thing the residual corrects
	// for. Zero means unknown, which the baseline fit treats as a sample it
	// cannot use rather than as an account with no audience.
	Followers int64 `json:"followers"`
	// Owned marks the user's own account. Owned accounts are the only source of
	// completion rate, because no platform exposes it publicly.
	Owned      bool      `json:"owned"`
	AddedAt    time.Time `json:"added_at"`
	LastPolled time.Time `json:"last_polled_at,omitempty"`
}

// RadarObservation is one reading of one post's metrics at one moment.
//
// A post accumulates many observations. Keeping every reading rather than
// overwriting is what makes the second derivative of the save and completion
// rates computable at all; with one row per post there is no series to
// differentiate.
type RadarObservation struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	// PostID is the platform's identifier for the work, which is what ties the
	// readings of one post into a series.
	PostID string `json:"post_id"`
	Title  string `json:"title,omitempty"`
	// DurationSeconds is the production cost proxy. It is a crude one and the
	// arbitrage measure says so, but it is the only cost signal a public metric
	// page carries.
	DurationSeconds int64     `json:"duration_seconds,omitempty"`
	PublishedAt     time.Time `json:"published_at"`
	ObservedAt      time.Time `json:"observed_at"`

	Views    int64 `json:"views"`
	Likes    int64 `json:"likes"`
	Comments int64 `json:"comments"`
	Shares   int64 `json:"shares"`
	Saves    int64 `json:"saves"`

	// CompletionRate is in [0,1] and is zero for accounts the user does not
	// own. Zero therefore means "not available" as well as "nobody finished
	// it"; the residual treats the two the same way, by skipping the sample,
	// because a completion rate of exactly zero does not occur in practice.
	CompletionRate float64 `json:"completion_rate,omitempty"`

	// CommentSamples is how many comments were read to produce
	// UnansweredQuestions. Without it the count is unreadable: nine unanswered
	// questions out of ten comments and out of ten thousand are opposite
	// findings.
	CommentSamples      int `json:"comment_samples,omitempty"`
	UnansweredQuestions int `json:"unanswered_questions,omitempty"`
}

// SaveRate is saves per view, in [0,1]. Zero views gives zero rather than an
// error: a post nobody watched has no save rate to speak of, and the baseline
// fit drops it as a sample.
func (o RadarObservation) SaveRate() float64 {
	if o.Views <= 0 {
		return 0
	}
	return float64(o.Saves) / float64(o.Views)
}

// RadarStore persists watched accounts and their metric observations.
type RadarStore interface {
	// PutAccount imports an account, or returns ErrAccountExists when the same
	// (platform, handle) is already watched.
	PutAccount(ctx context.Context, a RadarAccount) error
	// UpdateAccount overwrites the mutable fields of an existing account:
	// follower count, display name, category and last poll time. Import is kept
	// separate so that re-importing cannot silently reset a follower count that
	// the poller has since refreshed.
	UpdateAccount(ctx context.Context, a RadarAccount) error
	// Accounts lists watched accounts, oldest first. An empty platform means
	// every platform.
	Accounts(ctx context.Context, platform string) ([]RadarAccount, error)
	// Account returns one account, or ErrAccountNotFound.
	Account(ctx context.Context, id string) (RadarAccount, error)
	// AppendObservations records readings. It appends; it never replaces an
	// earlier reading of the same post.
	AppendObservations(ctx context.Context, obs []RadarObservation) error
	// Observations returns readings of posts published at or after since,
	// newest observation first, capped at limit. An empty accountID means every
	// watched account.
	Observations(ctx context.Context, accountID string, since time.Time, limit int) ([]RadarObservation, error)
}
