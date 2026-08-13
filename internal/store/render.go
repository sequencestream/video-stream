package store

import (
	"context"
	"time"
)

// RenderRunRecord tracks one render pipeline execution.
type RenderRunRecord struct {
	ID                 string    `json:"id"`
	ProjectID          string    `json:"project_id"`
	Resolution         string    `json:"resolution"`
	Platform           string    `json:"platform"`
	SubtitleMode       string    `json:"subtitle_mode"`
	Status             string    `json:"status"`
	Finalized          bool      `json:"finalized"`
	IncludeBGM         bool      `json:"include_bgm"`
	BGMURI             string    `json:"bgm_uri,omitempty"`
	BGMBPM             float64   `json:"bgm_bpm,omitempty"`
	BGMBeatOffsetMS    int64     `json:"bgm_beat_offset_ms,omitempty"`
	BGMGainDB          float64   `json:"bgm_gain_db,omitempty"`
	LastCompletedStage string    `json:"last_completed_stage,omitempty"`
	OutputURI          string    `json:"output_uri,omitempty"`
	Error              string    `json:"error,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// RenderSharedContextRecord holds prompt/seed/ref shared across resolutions.
type RenderSharedContextRecord struct {
	ProjectID      string `json:"project_id"`
	RenderCacheKey string `json:"render_cache_key"`
	Prompt         string `json:"prompt"`
	Seed           string `json:"seed"`
	RefURI         string `json:"ref_uri,omitempty"`
}

// RenderSegArtifact links a pipeline stage output back to a source seg.
type RenderSegArtifact struct {
	RunID          string `json:"run_id"`
	ProjectID      string `json:"project_id"`
	SegID          string `json:"seg_id"`
	RenderCacheKey string `json:"render_cache_key"`
	Stage          string `json:"stage"`
	URI            string `json:"uri"`
}

// RenderStore persists render runs and traceability records.
type RenderStore interface {
	CreateRenderRun(ctx context.Context, run RenderRunRecord) error
	UpdateRenderRun(ctx context.Context, run RenderRunRecord) error
	GetRenderRun(ctx context.Context, runID string) (RenderRunRecord, error)
	PutRenderSharedContext(ctx context.Context, projectID string, ctxs []RenderSharedContextRecord) error
	RenderSharedContext(ctx context.Context, projectID string) ([]RenderSharedContextRecord, error)
	PutRenderSegArtifact(ctx context.Context, rec RenderSegArtifact) error
	RenderSegArtifacts(ctx context.Context, runID string) ([]RenderSegArtifact, error)
}
