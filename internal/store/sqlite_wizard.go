package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrWizardSessionNotFound = errors.New("wizard session not found")

var _ WizardStore = (*SQLiteStore)(nil)

func (s *SQLiteStore) CreateWizardSession(ctx context.Context, rec WizardSessionRecord) error {
	now := rec.CreatedAt.UTC().UnixMilli()
	if now == 0 {
		now = time.Now().UTC().UnixMilli()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO wizard_sessions (id, current_step, status, topic, category, project_id, state_json, cost_micros, failed_step, error, hook_confirm_ms, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.CurrentStep, rec.Status, rec.Topic, rec.Category, rec.ProjectID,
		rec.StateJSON, rec.CostMicros, rec.FailedStep, rec.Error, rec.HookConfirmMS, now, now)
	return err
}

func (s *SQLiteStore) UpdateWizardSession(ctx context.Context, rec WizardSessionRecord) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE wizard_sessions SET current_step=?, status=?, project_id=?, state_json=?, cost_micros=?, failed_step=?, error=?, hook_confirm_ms=?, updated_at=?
		 WHERE id=?`,
		rec.CurrentStep, rec.Status, rec.ProjectID, rec.StateJSON, rec.CostMicros,
		rec.FailedStep, rec.Error, rec.HookConfirmMS, time.Now().UTC().UnixMilli(), rec.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrWizardSessionNotFound
	}
	return nil
}

func (s *SQLiteStore) GetWizardSession(ctx context.Context, id string) (WizardSessionRecord, error) {
	var rec WizardSessionRecord
	var created, updated int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, current_step, status, topic, category, project_id, state_json, cost_micros, failed_step, error, hook_confirm_ms, created_at, updated_at
		 FROM wizard_sessions WHERE id=?`, id).
		Scan(&rec.ID, &rec.CurrentStep, &rec.Status, &rec.Topic, &rec.Category, &rec.ProjectID,
			&rec.StateJSON, &rec.CostMicros, &rec.FailedStep, &rec.Error, &rec.HookConfirmMS, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return WizardSessionRecord{}, ErrWizardSessionNotFound
	}
	if err != nil {
		return WizardSessionRecord{}, fmt.Errorf("get wizard session %s: %w", id, err)
	}
	rec.CreatedAt = time.UnixMilli(created).UTC()
	rec.UpdatedAt = time.UnixMilli(updated).UTC()
	return rec, nil
}
