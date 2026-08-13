package store

import (
	"context"
	"errors"
	"time"
)

// ErrArtifactNotFound is returned when no artifact carries the given render
// cache key.
var ErrArtifactNotFound = errors.New("artifact not found")

// Artifact is one rendered product that a later compile may reuse.
//
// It exists because model.Seg.CanReuse needs two facts the seg itself cannot
// hold: whether an artifact for this key was ever produced, and how long it
// actually turned out. Without this record the render cache key has nothing to
// resolve to and every edit re-renders the whole video.
type Artifact struct {
	// RenderCacheKey is the identity of the artifact, not of the seg that
	// happened to produce it. Two segs with the same key share one row; that
	// sharing is the entire point of leaving seg_id out of the key.
	RenderCacheKey string `json:"render_cache_key"`
	// DurationMS is the artifact's real measured duration, which is what a
	// reuse check compares against the seg's budget. It is never the budget
	// midpoint: the whole reason the budget is an interval is that the two
	// differ.
	DurationMS int64 `json:"duration_ms"`
	// URI locates the bytes. Empty is allowed so a test or a dry run can
	// record what a compile would have cost without producing a file.
	URI string `json:"uri,omitempty"`
	// CostMicros is what producing this artifact cost, in millionths of a USD.
	//
	// Integers rather than a float amount: the saved-cost figure is a sum over
	// thousands of segs, and float error accumulates into exactly the headline
	// number this feature exists to report.
	CostMicros int64     `json:"cost_micros"`
	CreatedAt  time.Time `json:"created_at"`
}

// RecompileRun is one recorded recompilation outcome.
//
// Runs are persisted rather than counted in memory because the question they
// answer — is the invalidation rate low enough for this bet to hold — needs
// weeks of real edits. An in-memory counter resets on every restart and would
// never accumulate enough evidence to say anything.
type RecompileRun struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"project_id"`
	PlannedAt       time.Time `json:"planned_at"`
	TotalSegs       int       `json:"total_segs"`
	InvalidatedSegs int       `json:"invalidated_segs"`
	// FullRerun records that a boundary forced the whole project to re-render,
	// as opposed to every seg happening to be invalid on its own. The report
	// keeps the two apart because they call for different fixes.
	FullRerun bool `json:"full_rerun"`
	// Boundary names the boundary that forced the rerun, empty when none did.
	Boundary        string `json:"boundary,omitempty"`
	CostSavedMicros int64  `json:"cost_saved_micros"`
	// Execution fields are filled by the renderer after the accepted plan has
	// completed. Keeping planned and actual counters on the same stable run id
	// makes retries idempotent and exposes when execution diverges from intent.
	CacheHits        int   `json:"cache_hits"`
	RegeneratedSegs  int   `json:"regenerated_segs"`
	ElapsedMS        int64 `json:"elapsed_ms"`
	ActualCostMicros int64 `json:"actual_cost_micros"`
}

// ArtifactStore persists rendered artifacts by render cache key.
type ArtifactStore interface {
	// PutArtifact records an artifact, replacing any earlier one with the same
	// key.
	PutArtifact(ctx context.Context, a Artifact) error
	// Artifact returns the artifact for a key, or ErrArtifactNotFound.
	Artifact(ctx context.Context, renderCacheKey string) (Artifact, error)
}

// RecompileRunStore persists recompilation outcomes for the invalidation rate
// report.
type RecompileRunStore interface {
	// RecordRun appends one outcome.
	RecordRun(ctx context.Context, r RecompileRun) error
	// RecordRecompileExecution completes an already planned run's actual
	// measurements. It returns ErrRecompileRunNotFound for an unknown id.
	RecordRecompileExecution(ctx context.Context, runID string, cacheHits, regeneratedSegs int, elapsedMS, actualCostMicros int64) error
	// RecompileRuns returns runs newest first, capped at limit. An empty
	// projectID means every project.
	RecompileRuns(ctx context.Context, projectID string, limit int) ([]RecompileRun, error)
}
