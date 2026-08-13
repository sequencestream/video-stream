package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	_                       ArtifactStore     = (*SQLiteStore)(nil)
	_                       RecompileRunStore = (*SQLiteStore)(nil)
	ErrRecompileRunNotFound                   = errors.New("recompile run not found")
)

// PutArtifact records an artifact, replacing any earlier one with the same key.
//
// Replacing rather than rejecting a duplicate is deliberate: the same key
// re-rendered is the same content through the same pipeline, so the newer row
// is the better measurement of what it costs and how long it comes out.
func (s *SQLiteStore) PutArtifact(ctx context.Context, a Artifact) error {
	if a.RenderCacheKey == "" {
		return errors.New("artifact render_cache_key must not be empty")
	}
	// A non-positive duration would pass every budget check that happens to
	// start at zero and silently license reuse of a broken artifact.
	if a.DurationMS <= 0 {
		return fmt.Errorf("artifact %s: duration_ms must be positive, got %d", a.RenderCacheKey, a.DurationMS)
	}
	if a.CostMicros < 0 {
		return fmt.Errorf("artifact %s: cost_micros must not be negative, got %d", a.RenderCacheKey, a.CostMicros)
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO artifacts (render_cache_key, duration_ms, uri, cost_micros, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(render_cache_key) DO UPDATE SET
		   duration_ms = excluded.duration_ms,
		   uri         = excluded.uri,
		   cost_micros = excluded.cost_micros,
		   created_at  = excluded.created_at`,
		a.RenderCacheKey, a.DurationMS, a.URI, a.CostMicros, a.CreatedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("put artifact %s: %w", a.RenderCacheKey, err)
	}
	return nil
}

// Artifact returns the artifact for a key, or ErrArtifactNotFound.
func (s *SQLiteStore) Artifact(ctx context.Context, renderCacheKey string) (Artifact, error) {
	var (
		a         Artifact
		createdAt int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT render_cache_key, duration_ms, uri, cost_micros, created_at
		 FROM artifacts WHERE render_cache_key = ?`, renderCacheKey).
		Scan(&a.RenderCacheKey, &a.DurationMS, &a.URI, &a.CostMicros, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, ErrArtifactNotFound
	}
	if err != nil {
		return Artifact{}, fmt.Errorf("get artifact %s: %w", renderCacheKey, err)
	}
	a.CreatedAt = time.UnixMilli(createdAt).UTC()
	return a, nil
}

// RecordRun appends one recompilation outcome.
func (s *SQLiteStore) RecordRun(ctx context.Context, r RecompileRun) error {
	if r.ID == "" {
		return errors.New("recompile run id must not be empty")
	}
	if r.InvalidatedSegs < 0 || r.InvalidatedSegs > r.TotalSegs {
		return fmt.Errorf("recompile run %s: invalidated_segs %d is not within [0,%d]",
			r.ID, r.InvalidatedSegs, r.TotalSegs)
	}
	if r.CacheHits < 0 || r.RegeneratedSegs < 0 || r.ElapsedMS < 0 || r.ActualCostMicros < 0 {
		return fmt.Errorf("recompile run %s: execution metrics must not be negative", r.ID)
	}
	if r.PlannedAt.IsZero() {
		r.PlannedAt = time.Now()
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO recompile_runs (id, project_id, planned_at, total_segs, invalidated_segs,
		                             full_rerun, boundary, cost_saved_micros, cache_hits,
		                             regenerated_segs, elapsed_ms, actual_cost_micros)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET project_id=excluded.project_id, planned_at=excluded.planned_at,
		   total_segs=excluded.total_segs, invalidated_segs=excluded.invalidated_segs,
		   full_rerun=excluded.full_rerun, boundary=excluded.boundary, cost_saved_micros=excluded.cost_saved_micros`,
		r.ID, r.ProjectID, r.PlannedAt.UTC().UnixMilli(), r.TotalSegs, r.InvalidatedSegs,
		boolToInt(r.FullRerun), r.Boundary, r.CostSavedMicros, r.CacheHits, r.RegeneratedSegs,
		r.ElapsedMS, r.ActualCostMicros)
	if err != nil {
		return fmt.Errorf("record recompile run %s: %w", r.ID, err)
	}
	return nil
}

func (s *SQLiteStore) RecordRecompileExecution(ctx context.Context, runID string, cacheHits, regeneratedSegs int, elapsedMS, actualCostMicros int64) error {
	if runID == "" {
		return errors.New("recompile run id must not be empty")
	}
	if cacheHits < 0 || regeneratedSegs < 0 || elapsedMS < 0 || actualCostMicros < 0 {
		return fmt.Errorf("recompile run %s: execution metrics must not be negative", runID)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE recompile_runs
		SET cache_hits=?, regenerated_segs=?, elapsed_ms=?, actual_cost_micros=? WHERE id=?`,
		cacheHits, regeneratedSegs, elapsedMS, actualCostMicros, runID)
	if err != nil {
		return fmt.Errorf("record recompile execution %s: %w", runID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRecompileRunNotFound
	}
	return nil
}

// RecompileRuns returns runs newest first, capped at limit.
func (s *SQLiteStore) RecompileRuns(ctx context.Context, projectID string, limit int) ([]RecompileRun, error) {
	if limit <= 0 {
		limit = 1000
	}

	// One statement with a sentinel rather than two: '' means every project,
	// and no project id is ever empty because RecordRun would have to have been
	// handed one.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, planned_at, total_segs, invalidated_segs,
		        full_rerun, boundary, cost_saved_micros, cache_hits, regenerated_segs,
		        elapsed_ms, actual_cost_micros
		 FROM recompile_runs
		 WHERE ? = '' OR project_id = ?
		 ORDER BY planned_at DESC, id DESC LIMIT ?`, projectID, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recompile runs: %w", err)
	}
	defer rows.Close()

	var out []RecompileRun
	for rows.Next() {
		var (
			r         RecompileRun
			plannedAt int64
			fullRerun int
		)
		if err := rows.Scan(&r.ID, &r.ProjectID, &plannedAt, &r.TotalSegs, &r.InvalidatedSegs,
			&fullRerun, &r.Boundary, &r.CostSavedMicros, &r.CacheHits, &r.RegeneratedSegs,
			&r.ElapsedMS, &r.ActualCostMicros); err != nil {
			return nil, fmt.Errorf("scan recompile run: %w", err)
		}
		r.PlannedAt = time.UnixMilli(plannedAt).UTC()
		r.FullRerun = fullRerun != 0
		out = append(out, r)
	}
	return out, rows.Err()
}
