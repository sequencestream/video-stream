package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var _ RadarStore = (*SQLiteStore)(nil)

// PutAccount imports an account, or returns ErrAccountExists.
//
// Import rejects a duplicate rather than upserting. Re-importing is how a user
// corrects a typo in a handle, and if that quietly overwrote the row it would
// also discard the follower count and the poll cursor the radar has since
// gathered — losing exactly the history the residual needs.
func (s *SQLiteStore) PutAccount(ctx context.Context, a RadarAccount) error {
	if err := validateAccount(a); err != nil {
		return err
	}
	if a.AddedAt.IsZero() {
		a.AddedAt = time.Now()
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO radar_accounts (id, platform, handle, display_name, category,
		                             followers, owned, added_at, last_polled_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Platform, a.Handle, a.DisplayName, a.Category,
		a.Followers, boolToInt(a.Owned), a.AddedAt.UTC().UnixMilli(), unixMilliOrZero(a.LastPolled))
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAccountExists
		}
		return fmt.Errorf("put radar account %s/%s: %w", a.Platform, a.Handle, err)
	}
	return nil
}

// UpdateAccount overwrites the mutable fields of an existing account.
func (s *SQLiteStore) UpdateAccount(ctx context.Context, a RadarAccount) error {
	if strings.TrimSpace(a.ID) == "" {
		return errors.New("radar account id must not be empty")
	}
	if a.Followers < 0 {
		return fmt.Errorf("radar account %s: followers must not be negative, got %d", a.ID, a.Followers)
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE radar_accounts
		 SET display_name = ?, category = ?, followers = ?, last_polled_at = ?
		 WHERE id = ?`,
		a.DisplayName, a.Category, a.Followers, unixMilliOrZero(a.LastPolled), a.ID)
	if err != nil {
		return fmt.Errorf("update radar account %s: %w", a.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for radar account %s: %w", a.ID, err)
	}
	if n == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// Accounts lists watched accounts, oldest first.
func (s *SQLiteStore) Accounts(ctx context.Context, platform string) ([]RadarAccount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, platform, handle, display_name, category, followers, owned,
		        added_at, last_polled_at
		 FROM radar_accounts
		 WHERE ? = '' OR platform = ?
		 ORDER BY added_at ASC, id ASC`, platform, platform)
	if err != nil {
		return nil, fmt.Errorf("list radar accounts: %w", err)
	}
	defer rows.Close()

	var out []RadarAccount
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Account returns one account, or ErrAccountNotFound.
func (s *SQLiteStore) Account(ctx context.Context, id string) (RadarAccount, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, platform, handle, display_name, category, followers, owned,
		        added_at, last_polled_at
		 FROM radar_accounts WHERE id = ?`, id)

	a, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return RadarAccount{}, ErrAccountNotFound
	}
	if err != nil {
		return RadarAccount{}, fmt.Errorf("get radar account %s: %w", id, err)
	}
	return a, nil
}

// AppendObservations records readings in one transaction.
//
// All or nothing, because a half-written polling round produces a series with a
// hole in it, and the second derivative reads a hole as a sudden collapse in
// momentum rather than as missing data.
func (s *SQLiteStore) AppendObservations(ctx context.Context, obs []RadarObservation) error {
	if len(obs) == 0 {
		return nil
	}
	for i := range obs {
		if err := validateObservation(obs[i]); err != nil {
			return err
		}
		if obs[i].ObservedAt.IsZero() {
			obs[i].ObservedAt = time.Now()
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin append observations: %w", err)
	}
	defer tx.Rollback()

	for _, o := range obs {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO radar_observations (id, account_id, post_id, title, duration_seconds,
			                                 published_at, observed_at, views, likes, comments,
			                                 shares, saves, completion_rate, comment_samples,
			                                 unanswered_questions)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			o.ID, o.AccountID, o.PostID, o.Title, o.DurationSeconds,
			o.PublishedAt.UTC().UnixMilli(), o.ObservedAt.UTC().UnixMilli(),
			o.Views, o.Likes, o.Comments, o.Shares, o.Saves,
			o.CompletionRate, o.CommentSamples, o.UnansweredQuestions)
		if err != nil {
			return fmt.Errorf("append radar observation %s: %w", o.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit append observations: %w", err)
	}
	return nil
}

// Observations returns readings of posts published at or after since.
func (s *SQLiteStore) Observations(ctx context.Context, accountID string, since time.Time, limit int) ([]RadarObservation, error) {
	if limit <= 0 {
		limit = 5000
	}
	// A zero since means "no lower bound", which as a Unix millisecond value is
	// a very negative number rather than zero — time.Time's zero is year 1.
	var sinceMS int64
	if !since.IsZero() {
		sinceMS = since.UTC().UnixMilli()
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, account_id, post_id, title, duration_seconds, published_at, observed_at,
		        views, likes, comments, shares, saves, completion_rate,
		        comment_samples, unanswered_questions
		 FROM radar_observations
		 WHERE (? = '' OR account_id = ?) AND published_at >= ?
		 ORDER BY observed_at DESC, id DESC LIMIT ?`, accountID, accountID, sinceMS, limit)
	if err != nil {
		return nil, fmt.Errorf("list radar observations: %w", err)
	}
	defer rows.Close()

	var out []RadarObservation
	for rows.Next() {
		var (
			o                       RadarObservation
			publishedAt, observedAt int64
		)
		if err := rows.Scan(&o.ID, &o.AccountID, &o.PostID, &o.Title, &o.DurationSeconds,
			&publishedAt, &observedAt, &o.Views, &o.Likes, &o.Comments, &o.Shares, &o.Saves,
			&o.CompletionRate, &o.CommentSamples, &o.UnansweredQuestions); err != nil {
			return nil, fmt.Errorf("scan radar observation: %w", err)
		}
		o.PublishedAt = time.UnixMilli(publishedAt).UTC()
		o.ObservedAt = time.UnixMilli(observedAt).UTC()
		out = append(out, o)
	}
	return out, rows.Err()
}

func scanAccount(sc scanner) (RadarAccount, error) {
	var (
		a                     RadarAccount
		owned                 int
		addedAt, lastPolledAt int64
	)
	if err := sc.Scan(&a.ID, &a.Platform, &a.Handle, &a.DisplayName, &a.Category,
		&a.Followers, &owned, &addedAt, &lastPolledAt); err != nil {
		return RadarAccount{}, err
	}
	a.Owned = owned != 0
	a.AddedAt = time.UnixMilli(addedAt).UTC()
	if lastPolledAt != 0 {
		a.LastPolled = time.UnixMilli(lastPolledAt).UTC()
	}
	return a, nil
}

func validateAccount(a RadarAccount) error {
	if strings.TrimSpace(a.ID) == "" {
		return errors.New("radar account id must not be empty")
	}
	if strings.TrimSpace(a.Platform) == "" {
		return fmt.Errorf("radar account %s: platform must not be empty", a.ID)
	}
	if strings.TrimSpace(a.Handle) == "" {
		return fmt.Errorf("radar account %s: handle must not be empty", a.ID)
	}
	if a.Followers < 0 {
		return fmt.Errorf("radar account %s: followers must not be negative, got %d", a.ID, a.Followers)
	}
	return nil
}

func validateObservation(o RadarObservation) error {
	if strings.TrimSpace(o.ID) == "" {
		return errors.New("radar observation id must not be empty")
	}
	if strings.TrimSpace(o.AccountID) == "" {
		return fmt.Errorf("radar observation %s: account_id must not be empty", o.ID)
	}
	if strings.TrimSpace(o.PostID) == "" {
		return fmt.Errorf("radar observation %s: post_id must not be empty", o.ID)
	}
	if o.PublishedAt.IsZero() {
		return fmt.Errorf("radar observation %s: published_at must be set", o.ID)
	}
	if o.CompletionRate < 0 || o.CompletionRate > 1 {
		return fmt.Errorf("radar observation %s: completion_rate must be within [0,1], got %g", o.ID, o.CompletionRate)
	}
	// An unanswered count above the sample size is not a large number, it is a
	// broken denominator, and it would push the reported density above 1.
	if o.UnansweredQuestions < 0 || o.UnansweredQuestions > o.CommentSamples {
		return fmt.Errorf("radar observation %s: unanswered_questions %d is not within [0,%d]",
			o.ID, o.UnansweredQuestions, o.CommentSamples)
	}
	return nil
}

func unixMilliOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixMilli()
}

// isUniqueViolation recognises a UNIQUE constraint failure without depending on
// the driver's error type. modernc.org/sqlite returns a driver-private struct,
// so matching the message is the portable option; it is only used to pick a
// friendlier sentinel, and a miss degrades to the wrapped error.
func isUniqueViolation(err error) bool {
	return strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}
