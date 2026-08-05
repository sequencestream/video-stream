// Package radar turns imported competitor accounts into topic signals.
//
// The bet is narrow on purpose. A general web crawler over every platform's
// trending surface would cover more ground, but it lives in a legal grey zone
// and breaks whenever a platform reshuffles its markup — a cost out of all
// proportion to its value. What the user imports themselves, plus the numbers
// their own account's dashboard already shows them, is the one signal with no
// grey area at all, and it covers most of the value.
//
// The second stance is about what "hot" means. It is not a view count. A
// channel with two million followers clearing two hundred thousand views has
// done nothing remarkable, and a channel with two thousand followers doing the
// same has done something worth copying. So every number here is a residual:
// what the post did, minus what an account of that size, in that category, at
// that age, was expected to do. Absolute metrics are never compared.
//
// Nothing in this package writes to a platform. It reads public metrics that
// the user brought in, and computes. See doc/arch/radar.md.
package radar

import (
	"errors"
	"strings"
	"time"
)

var (
	// ErrTooManyAccounts means the import would push the watch list past
	// MaxAccounts.
	ErrTooManyAccounts = errors.New("radar watch list is full")
	// ErrNoSource means no polling source is registered for a platform. It is
	// the normal state of the MVP, which ships no scrapers at all, and is
	// distinct from a failure so the poller can skip rather than retry.
	ErrNoSource = errors.New("no radar source registered for platform")
	// ErrRateLimited means the platform asked us to slow down. Sources map an
	// HTTP 429 onto it so the poller can back off instead of hammering an
	// endpoint that is already refusing — continuing is how a rate limit turns
	// into a ban.
	ErrRateLimited = errors.New("platform rate limited the request")
)

// MaxAccounts is the ceiling on the watch list.
//
// Twenty is the top of the range the feature was scoped against, and it is
// enforced rather than advised because the ceiling is really a rate-limit
// budget: the polling schedule that keeps twenty accounts under every
// platform's limit does not survive two hundred.
const MaxAccounts = 20

// ObservationWindowDays is how far back a post may have been published and
// still count. Older work says something about the account but nothing about
// what is working now, which is the only question this module asks.
const ObservationWindowDays = 30

// Account is one watched account, as the radar package sees it.
type Account struct {
	ID          string
	Platform    string
	Handle      string
	DisplayName string
	Category    string
	Followers   int64
	// Owned marks the user's own account. It is the only source of completion
	// rate, because no platform publishes that number to third parties.
	Owned bool
}

// Comment is one sampled comment.
//
// Only two facts are kept: the text, so a question can be recognised, and
// whether the account's author has replied. The commenter is deliberately not
// recorded — the measure is about the topic gap, and storing who asked would
// turn a topic tool into a profile of named strangers for no analytical gain.
type Comment struct {
	Text string
	// AuthorReplied is true when the post's author answered this comment.
	AuthorReplied bool
}

// Reading is one fetch of one post's public metrics, before it is persisted.
//
// It carries raw comment texts, which the store never sees: Engine.Ingest
// reduces them to a question count on the way in. Comment bodies are other
// people's words, and keeping only the count is the smallest thing that answers
// the question being asked.
type Reading struct {
	AccountID       string
	PostID          string
	Title           string
	DurationSeconds int64
	PublishedAt     time.Time
	ObservedAt      time.Time

	Views    int64
	Likes    int64
	Comments int64
	Shares   int64
	Saves    int64

	// CompletionRate is in [0,1]; zero means unavailable, which is the normal
	// case for an account the user does not own.
	CompletionRate float64

	// CommentSample is the comments read for the unanswered-question density.
	// Empty is fine and yields a zero-sample density, not a zero density.
	CommentSample []Comment
}

// Validate rejects readings that would corrupt a baseline rather than merely
// be missing from it.
func (r Reading) Validate() error {
	if strings.TrimSpace(r.AccountID) == "" {
		return errors.New("reading account id must not be empty")
	}
	if strings.TrimSpace(r.PostID) == "" {
		return errors.New("reading post id must not be empty")
	}
	if r.PublishedAt.IsZero() {
		return errors.New("reading published_at must be set")
	}
	// Negative counters are not a small number, they are a parse failure, and
	// a single one poisons the log-space fit for the whole category.
	if r.Views < 0 || r.Likes < 0 || r.Comments < 0 || r.Shares < 0 || r.Saves < 0 {
		return errors.New("reading metrics must not be negative")
	}
	if r.CompletionRate < 0 || r.CompletionRate > 1 {
		return errors.New("reading completion_rate must be within [0,1]")
	}
	return nil
}

// normalise trims the fields that are compared or keyed on, so that a handle
// pasted with a trailing space does not become a second account.
func (a Account) normalise() Account {
	a.ID = strings.TrimSpace(a.ID)
	a.Platform = strings.ToLower(strings.TrimSpace(a.Platform))
	a.Handle = strings.TrimSpace(a.Handle)
	a.DisplayName = strings.TrimSpace(a.DisplayName)
	a.Category = strings.ToLower(strings.TrimSpace(a.Category))
	return a
}
