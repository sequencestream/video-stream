package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	ErrRenderRunNotFound = errors.New("render run not found")
)

var _ RenderStore = (*SQLiteStore)(nil)

func (s *SQLiteStore) CreateRenderRun(ctx context.Context, run RenderRunRecord) error {
	now := time.Now().UTC().UnixMilli()
	finalized := 0
	if run.Finalized {
		finalized = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO render_runs (id, project_id, resolution, status, finalized, last_completed_stage, output_uri, error, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.ProjectID, run.Resolution, run.Status, finalized,
		run.LastCompletedStage, run.OutputURI, run.Error, now)
	return err
}

func (s *SQLiteStore) UpdateRenderRun(ctx context.Context, run RenderRunRecord) error {
	finalized := 0
	if run.Finalized {
		finalized = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE render_runs SET status=?, finalized=?, last_completed_stage=?, output_uri=?, error=?, updated_at=?
		 WHERE id=?`,
		run.Status, finalized, run.LastCompletedStage, run.OutputURI, run.Error,
		time.Now().UTC().UnixMilli(), run.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrRenderRunNotFound
	}
	return nil
}

func (s *SQLiteStore) GetRenderRun(ctx context.Context, runID string) (RenderRunRecord, error) {
	var r RenderRunRecord
	var finalized int
	var updated int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, resolution, status, finalized, last_completed_stage, output_uri, error, updated_at
		 FROM render_runs WHERE id=?`, runID).
		Scan(&r.ID, &r.ProjectID, &r.Resolution, &r.Status, &finalized,
			&r.LastCompletedStage, &r.OutputURI, &r.Error, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return RenderRunRecord{}, ErrRenderRunNotFound
	}
	if err != nil {
		return RenderRunRecord{}, fmt.Errorf("get render run %s: %w", runID, err)
	}
	r.Finalized = finalized != 0
	r.UpdatedAt = time.UnixMilli(updated).UTC()
	return r, nil
}

func (s *SQLiteStore) PutRenderSharedContext(ctx context.Context, projectID string, ctxs []RenderSharedContextRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM render_shared_context WHERE project_id=?`, projectID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO render_shared_context (project_id, render_cache_key, prompt, seed, ref_uri)
		 VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range ctxs {
		if _, err := stmt.ExecContext(ctx, projectID, c.RenderCacheKey, c.Prompt, c.Seed, c.RefURI); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) RenderSharedContext(ctx context.Context, projectID string) ([]RenderSharedContextRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT project_id, render_cache_key, prompt, seed, ref_uri
		 FROM render_shared_context WHERE project_id=? ORDER BY render_cache_key`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RenderSharedContextRecord
	for rows.Next() {
		var r RenderSharedContextRecord
		if err := rows.Scan(&r.ProjectID, &r.RenderCacheKey, &r.Prompt, &r.Seed, &r.RefURI); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) PutRenderSegArtifact(ctx context.Context, rec RenderSegArtifact) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO render_seg_artifacts (run_id, project_id, seg_id, render_cache_key, stage, uri)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id, seg_id, stage) DO UPDATE SET uri=excluded.uri, render_cache_key=excluded.render_cache_key`,
		rec.RunID, rec.ProjectID, rec.SegID, rec.RenderCacheKey, rec.Stage, rec.URI)
	return err
}

func (s *SQLiteStore) RenderSegArtifacts(ctx context.Context, runID string) ([]RenderSegArtifact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id, project_id, seg_id, render_cache_key, stage, uri
		 FROM render_seg_artifacts WHERE run_id=? ORDER BY seg_id, stage`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RenderSegArtifact
	for rows.Next() {
		var r RenderSegArtifact
		if err := rows.Scan(&r.RunID, &r.ProjectID, &r.SegID, &r.RenderCacheKey, &r.Stage, &r.URI); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
