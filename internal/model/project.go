package model

import (
	"errors"
	"fmt"
	"time"
)

// Project is the persisted unit: the seg graph, the render profile it was
// sealed under, and the timeline aligned to it.
type Project struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Title         string `json:"title,omitempty"`
	// RenderProfile is the pipeline configuration the render cache keys were
	// computed under. It lives on the project because one project renders with
	// one voice and one renderer; keeping it here is what lets Validate
	// recompute and verify every seg's render_cache_key.
	RenderProfile RenderProfile `json:"render_profile"`
	Segs          []Seg         `json:"segs"`
	Timeline      Timeline      `json:"timeline"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// NewProject builds an empty project stamped with the current schema version.
func NewProject(id, title string, now time.Time) Project {
	return Project{
		SchemaVersion: SchemaVersion,
		ID:            id,
		Title:         title,
		CreatedAt:     now.UTC(),
		UpdatedAt:     now.UTC(),
	}
}

// Seal recomputes every derived field in place.
//
// Callers must Seal after any edit; Validate refuses to pass a project whose
// derived fields disagree with a recomputation, so a forgotten Seal fails at
// the write instead of quietly serving a rendered artifact that no longer
// matches the script.
func (p *Project) Seal() {
	for i := range p.Segs {
		p.Segs[i].ContentHash = ComputeContentHash(p.Segs[i])
		p.Segs[i].RenderCacheKey = ComputeRenderCacheKey(p.Segs[i], p.RenderProfile)
	}
}

// Validate checks the whole document: schema version, every seg, the dependency
// graph, the derived hashes, and the timeline's link back to the segs.
func (p Project) Validate() error {
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("project %s: schema_version is %d, this binary writes %d", p.ID, p.SchemaVersion, SchemaVersion)
	}
	if p.ID == "" {
		return errors.New("project.id must not be empty")
	}
	if len(p.Segs) == 0 {
		return fmt.Errorf("project %s: must hold at least one seg", p.ID)
	}

	seen := make(map[string]struct{}, len(p.Segs))
	for _, s := range p.Segs {
		if err := s.Validate(); err != nil {
			return err
		}
		if _, dup := seen[s.SegID]; dup {
			return fmt.Errorf("project %s: seg_id %s appears more than once", p.ID, s.SegID)
		}
		seen[s.SegID] = struct{}{}

		if want := ComputeContentHash(s); s.ContentHash != want {
			return fmt.Errorf("%w: seg %s content_hash is %q, want %q; call Project.Seal after editing",
				ErrStaleDerived, s.SegID, s.ContentHash, want)
		}
		if want := ComputeRenderCacheKey(s, p.RenderProfile); s.RenderCacheKey != want {
			return fmt.Errorf("%w: seg %s render_cache_key is %q, want %q; call Project.Seal after editing",
				ErrStaleDerived, s.SegID, s.RenderCacheKey, want)
		}
	}

	if _, err := renderOrder(p.Segs); err != nil {
		return fmt.Errorf("project %s: %w", p.ID, err)
	}

	if err := p.Timeline.Validate(); err != nil {
		return fmt.Errorf("project %s: %w", p.ID, err)
	}
	for _, e := range p.Timeline.Events {
		for _, u := range e.Utterances {
			if _, ok := seen[u.SegID]; !ok {
				return fmt.Errorf("project %s: utterance %s is aligned to unknown seg %s", p.ID, u.ID, u.SegID)
			}
		}
	}
	return nil
}

// RenderOrder returns seg ids in dependency order, ties broken by id.
func (p Project) RenderOrder() ([]string, error) { return renderOrder(p.Segs) }

// Seg returns the seg with the given id.
func (p Project) Seg(segID string) (Seg, bool) {
	for _, s := range p.Segs {
		if s.SegID == segID {
			return s, true
		}
	}
	return Seg{}, false
}
