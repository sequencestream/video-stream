package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrWizardSessionNotFound = errors.New("wizard session not found")
var ErrWizardOperationNotFound = errors.New("wizard operation not found")
var ErrWizardOperationExists = errors.New("wizard operation already exists")
var ErrWizardVersionConflict = errors.New("wizard session version conflict")
var ErrWizardSessionBusy = errors.New("wizard session has an active operation")

var _ WizardStore = (*SQLiteStore)(nil)

func (s *SQLiteStore) CreateWizardSession(ctx context.Context, rec WizardSessionRecord) error {
	now := rec.CreatedAt.UTC().UnixMilli()
	if now == 0 {
		now = time.Now().UTC().UnixMilli()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO wizard_sessions (id, current_step, status, topic, category, project_id, state_json, cost_micros, failed_step, error, hook_confirm_ms, version, active_operation_id, failed_operation_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.CurrentStep, rec.Status, rec.Topic, rec.Category, rec.ProjectID,
		rec.StateJSON, rec.CostMicros, rec.FailedStep, rec.Error, rec.HookConfirmMS,
		normalizeWizardVersion(rec.Version), rec.ActiveOperationID, rec.FailedOperationID, now, now)
	return err
}

func (s *SQLiteStore) CreateWizardSessionWithOperation(ctx context.Context, rec WizardSessionRecord, op WizardOperationRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().UnixMilli()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wizard_operations (operation_id, session_id, kind, step, expected_version, request_json, request_hash, status, result_json, error_code, error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		op.ID, rec.ID, op.Kind, op.Step, op.ExpectedVersion, op.RequestJSON, op.RequestHash,
		op.Status, op.ResultJSON, op.ErrorCode, op.Error, now, now); err != nil {
		if isSQLiteUnique(err) {
			return ErrWizardOperationExists
		}
		return err
	}
	created := rec.CreatedAt.UTC().UnixMilli()
	if created == 0 {
		created = now
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wizard_sessions (id, current_step, status, topic, category, project_id, state_json, cost_micros, failed_step, error, hook_confirm_ms, version, active_operation_id, failed_operation_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.CurrentStep, rec.Status, rec.Topic, rec.Category, rec.ProjectID, rec.StateJSON,
		rec.CostMicros, rec.FailedStep, rec.Error, rec.HookConfirmMS, normalizeWizardVersion(rec.Version),
		rec.ActiveOperationID, rec.FailedOperationID, created, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) UpdateWizardSession(ctx context.Context, rec WizardSessionRecord) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE wizard_sessions SET current_step=?, status=?, project_id=?, state_json=?, cost_micros=?, failed_step=?, error=?, hook_confirm_ms=?, version=?, active_operation_id=?, failed_operation_id=?, updated_at=?
		 WHERE id=?`,
		rec.CurrentStep, rec.Status, rec.ProjectID, rec.StateJSON, rec.CostMicros,
		rec.FailedStep, rec.Error, rec.HookConfirmMS, normalizeWizardVersion(rec.Version),
		rec.ActiveOperationID, rec.FailedOperationID, time.Now().UTC().UnixMilli(), rec.ID)
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
		`SELECT id, current_step, status, topic, category, project_id, state_json, cost_micros, failed_step, error, hook_confirm_ms, version, active_operation_id, failed_operation_id, created_at, updated_at
		 FROM wizard_sessions WHERE id=?`, id).
		Scan(&rec.ID, &rec.CurrentStep, &rec.Status, &rec.Topic, &rec.Category, &rec.ProjectID,
			&rec.StateJSON, &rec.CostMicros, &rec.FailedStep, &rec.Error, &rec.HookConfirmMS,
			&rec.Version, &rec.ActiveOperationID, &rec.FailedOperationID, &created, &updated)
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

func (s *SQLiteStore) GetWizardOperation(ctx context.Context, id string) (WizardOperationRecord, error) {
	var op WizardOperationRecord
	var created, updated int64
	err := s.db.QueryRowContext(ctx,
		`SELECT operation_id, session_id, kind, step, expected_version, request_json, request_hash, status, result_json, error_code, error, created_at, updated_at
		 FROM wizard_operations WHERE operation_id=?`, id).
		Scan(&op.ID, &op.SessionID, &op.Kind, &op.Step, &op.ExpectedVersion, &op.RequestJSON,
			&op.RequestHash, &op.Status, &op.ResultJSON, &op.ErrorCode, &op.Error, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return WizardOperationRecord{}, ErrWizardOperationNotFound
	}
	if err != nil {
		return WizardOperationRecord{}, err
	}
	op.CreatedAt, op.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return op, nil
}

func (s *SQLiteStore) PutWizardOperation(ctx context.Context, op WizardOperationRecord) error {
	now := time.Now().UTC().UnixMilli()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO wizard_operations (operation_id, session_id, kind, step, expected_version, request_json, request_hash, status, result_json, error_code, error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		op.ID, op.SessionID, op.Kind, op.Step, op.ExpectedVersion, op.RequestJSON, op.RequestHash,
		op.Status, op.ResultJSON, op.ErrorCode, op.Error, now, now)
	if isSQLiteUnique(err) {
		return ErrWizardOperationExists
	}
	return err
}

// ClaimWizardOperation atomically journals an operation and locks the expected
// session version. It returns the pre-operation session snapshot.
func (s *SQLiteStore) ClaimWizardOperation(ctx context.Context, sessionID string, expectedVersion int64, allowFailed bool, op WizardOperationRecord) (WizardSessionRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WizardSessionRecord{}, err
	}
	defer tx.Rollback()
	rec, err := getWizardSessionTx(ctx, tx, sessionID)
	if err != nil {
		return WizardSessionRecord{}, err
	}
	if rec.Version != expectedVersion {
		return WizardSessionRecord{}, ErrWizardVersionConflict
	}
	if rec.ActiveOperationID != "" || rec.Status == "processing" {
		return WizardSessionRecord{}, ErrWizardSessionBusy
	}
	if allowFailed {
		if rec.Status != "failed" {
			return WizardSessionRecord{}, ErrWizardVersionConflict
		}
	} else if rec.Status != "active" {
		return WizardSessionRecord{}, ErrWizardVersionConflict
	}
	now := time.Now().UTC().UnixMilli()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wizard_operations (operation_id, session_id, kind, step, expected_version, request_json, request_hash, status, result_json, error_code, error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?)`,
		op.ID, sessionID, op.Kind, op.Step, op.ExpectedVersion, op.RequestJSON, op.RequestHash, WizardOperationRunning, now, now); err != nil {
		if isSQLiteUnique(err) {
			return WizardSessionRecord{}, ErrWizardOperationExists
		}
		return WizardSessionRecord{}, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE wizard_sessions SET status='processing', active_operation_id=?, updated_at=?
		 WHERE id=? AND version=? AND active_operation_id=''`, op.ID, now, sessionID, expectedVersion)
	if err != nil {
		return WizardSessionRecord{}, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return WizardSessionRecord{}, ErrWizardVersionConflict
	}
	if err := tx.Commit(); err != nil {
		return WizardSessionRecord{}, err
	}
	return rec, nil
}

// FinishWizardOperation commits the result and unlocks the session together.
func (s *SQLiteStore) FinishWizardOperation(ctx context.Context, rec WizardSessionRecord, op WizardOperationRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().UnixMilli()
	res, err := tx.ExecContext(ctx,
		`UPDATE wizard_sessions SET current_step=?, status=?, project_id=?, state_json=?, cost_micros=?, failed_step=?, error=?, hook_confirm_ms=?, version=?, active_operation_id='', failed_operation_id=?, updated_at=?
		 WHERE id=? AND active_operation_id=?`,
		rec.CurrentStep, rec.Status, rec.ProjectID, rec.StateJSON, rec.CostMicros, rec.FailedStep,
		rec.Error, rec.HookConfirmMS, rec.Version, rec.FailedOperationID, now, rec.ID, op.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrWizardVersionConflict
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE wizard_operations SET status=?, result_json=?, error_code=?, error=?, updated_at=?
		 WHERE operation_id=? AND status='running'`, op.Status, op.ResultJSON, op.ErrorCode, op.Error, now, op.ID)
	if err != nil {
		return err
	}
	n, _ = res.RowsAffected()
	if n != 1 {
		return ErrWizardVersionConflict
	}
	return tx.Commit()
}

// RecoverWizardOperations converts work owned by a dead daemon into an
// explicit failed session. The current daemon is single-instance by design.
func (s *SQLiteStore) RecoverWizardOperations(ctx context.Context, reason string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().UnixMilli()
	rows, err := tx.QueryContext(ctx, `SELECT operation_id, session_id FROM wizard_operations WHERE status='running'`)
	if err != nil {
		return 0, err
	}
	var pairs [][2]string
	for rows.Next() {
		var p [2]string
		if err := rows.Scan(&p[0], &p[1]); err != nil {
			rows.Close()
			return 0, err
		}
		pairs = append(pairs, p)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, p := range pairs {
		if _, err := tx.ExecContext(ctx, `UPDATE wizard_operations SET status='interrupted', error_code='operation_interrupted', error=?, updated_at=? WHERE operation_id=?`, reason, now, p[0]); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE wizard_sessions SET status='failed', failed_step=current_step, error=?, failed_operation_id=?, active_operation_id='', version=version+1, updated_at=? WHERE id=? AND active_operation_id=?`,
			reason, p[0], now, p[1], p[0]); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(pairs), nil
}

func getWizardSessionTx(ctx context.Context, tx *sql.Tx, id string) (WizardSessionRecord, error) {
	var rec WizardSessionRecord
	var created, updated int64
	err := tx.QueryRowContext(ctx,
		`SELECT id, current_step, status, topic, category, project_id, state_json, cost_micros, failed_step, error, hook_confirm_ms, version, active_operation_id, failed_operation_id, created_at, updated_at FROM wizard_sessions WHERE id=?`, id).
		Scan(&rec.ID, &rec.CurrentStep, &rec.Status, &rec.Topic, &rec.Category, &rec.ProjectID, &rec.StateJSON,
			&rec.CostMicros, &rec.FailedStep, &rec.Error, &rec.HookConfirmMS, &rec.Version,
			&rec.ActiveOperationID, &rec.FailedOperationID, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return WizardSessionRecord{}, ErrWizardSessionNotFound
	}
	if err != nil {
		return WizardSessionRecord{}, err
	}
	rec.CreatedAt, rec.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return rec, nil
}

func normalizeWizardVersion(v int64) int64 {
	if v <= 0 {
		return 1
	}
	return v
}

func isSQLiteUnique(err error) bool {
	return err != nil && (contains(err.Error(), "UNIQUE constraint failed") || contains(err.Error(), "constraint failed"))
}

func contains(s, part string) bool {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
