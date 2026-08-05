package store

import (
	"context"
	"errors"
	"time"

	"github.com/sequencestream/video-stream/internal/model"
)

// ErrProjectNotFound is returned when a project id has no matching row.
var ErrProjectNotFound = errors.New("project not found")

// ProjectSummary is the listing view: enough to render an index without
// decoding every stored document.
type ProjectSummary struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	SchemaVersion int       `json:"schema_version"`
	SegCount      int       `json:"seg_count"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// SegRef locates one seg by the projected index.
type SegRef struct {
	ProjectID      string               `json:"project_id"`
	SegID          string               `json:"seg_id"`
	ContentHash    string               `json:"content_hash"`
	RenderCacheKey string               `json:"render_cache_key"`
	DurationBudget model.DurationBudget `json:"duration_budget_ms"`
	Protected      bool                 `json:"protected"`
}

// ProjectStore persists video projects.
//
// The document is stored whole as JSON and a small seg index is projected
// alongside it. The projection exists for exactly one query — find every seg
// sharing a render cache key — which is the query incremental recompilation is
// built on and which cannot be served by scanning documents. Keeping the
// document authoritative means adding a model field needs no DDL change,
// because the projection is rebuilt from scratch on every save.
type ProjectStore interface {
	// SaveProject validates and upserts a project, rebuilding its seg index.
	SaveProject(ctx context.Context, p model.Project) error
	// GetProject returns a project by id, or ErrProjectNotFound.
	GetProject(ctx context.Context, id string) (model.Project, error)
	// ListProjects returns summaries newest first, capped at limit.
	ListProjects(ctx context.Context, limit int) ([]ProjectSummary, error)
	// DeleteProject removes a project and its seg index.
	DeleteProject(ctx context.Context, id string) error
	// SegsByRenderCacheKey returns every indexed seg carrying the given key,
	// across all projects. A caller still has to check the cached artifact's
	// real duration against each seg's budget; see model.Seg.CanReuse.
	SegsByRenderCacheKey(ctx context.Context, key string) ([]SegRef, error)
}
