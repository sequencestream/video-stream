package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrPolishRunNotFound is returned when no polish run has the id.
	ErrPolishRunNotFound = errors.New("script polish run not found")
)

var _ ScriptStore = (*SQLiteStore)(nil)

// PutPolishRun inserts one polish run record.
func (s *SQLiteStore) PutPolishRun(ctx context.Context, r PolishRunRecord) error {
	if strings.TrimSpace(r.ID) == "" {
		return errors.New("polish run id must not be empty")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO script_polish_runs (id, project_id, stop_reason, tokens_used, cost_micros, rounds, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ProjectID, r.StopReason, r.TokensUsed, r.CostMicros, r.Rounds,
		r.CreatedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("put polish run %s: %w", r.ID, err)
	}
	return nil
}

// PolishRun returns one polish run by id.
func (s *SQLiteStore) PolishRun(ctx context.Context, id string) (PolishRunRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, stop_reason, tokens_used, cost_micros, rounds, created_at
		 FROM script_polish_runs WHERE id = ?`, id)
	var r PolishRunRecord
	var created int64
	if err := row.Scan(&r.ID, &r.ProjectID, &r.StopReason, &r.TokensUsed, &r.CostMicros, &r.Rounds, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PolishRunRecord{}, ErrPolishRunNotFound
		}
		return PolishRunRecord{}, fmt.Errorf("get polish run %s: %w", id, err)
	}
	r.CreatedAt = time.UnixMilli(created).UTC()
	return r, nil
}
