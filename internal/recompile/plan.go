// Package recompile decides what a script edit actually has to re-render.
//
// The bet this package carries is the project's first: editing one line should
// re-render the shots that line touches, not the whole video. It is also the
// project's largest technical risk, because under real editing behaviour the
// share of segs that fall out of cache may simply be too high for the bet to
// pay. Both halves are here on purpose — the engine that saves the work, and
// the telemetry that says whether it saved enough to matter. See Report.Verdict
// for the threshold at which the honest answer is that this does not work.
//
// The engine refuses to be clever at the edges. Six kinds of edit are declared
// unsafe to recompile incrementally, and hitting any of them re-runs the whole
// project with a stated reason rather than producing a video that is fast and
// visibly broken.
package recompile

import (
	"fmt"

	"github.com/sequencestream/video-stream/internal/model"
)

// Boundary names an edit that cannot be recompiled incrementally.
//
// These are honest limits, not unimplemented cases. Each one describes an edit
// whose effects genuinely spread past the segs it touches, and pretending
// otherwise produces a video with a visible seam.
type Boundary string

// The six boundaries. Every value has a stated failure mode; a boundary that
// cannot name what breaks without it does not belong here.
const (
	// BoundaryNone means no boundary was hit.
	BoundaryNone Boundary = ""
	// BoundaryStyleAnchor: the visual identity changed, so every frame in the
	// project was rendered under a style that no longer applies.
	BoundaryStyleAnchor Boundary = "style_anchor_changed"
	// BoundaryDurationDrift: total runtime moved far enough that pacing,
	// music and shot lengths were all planned against the wrong length.
	BoundaryDurationDrift Boundary = "duration_drift"
	// BoundaryBeatReordered: the emotional beat sequence changed, which
	// re-lays out the whole arc rather than one point in it.
	BoundaryBeatReordered Boundary = "beat_reordered"
	// BoundaryHookEdited: the opening changed. Everything after it is cut
	// against the pace and framing the hook establishes.
	BoundaryHookEdited Boundary = "hook_edited"
	// BoundaryContinuityBroken: an edit landed inside a run of shots that
	// form one continuous action, where a re-render of one shot alone shows
	// as a jump.
	BoundaryContinuityBroken Boundary = "continuity_broken"
	// BoundaryBatchBroken: an edit landed inside a set of shots generated in
	// one call, which were conditioned on each other.
	BoundaryBatchBroken Boundary = "batch_broken"
)

// DurationDriftPercent is how far total runtime may move before the edit counts
// as a re-plan rather than a revision.
//
// Beyond it, shot lengths, pacing and music placement were all chosen against a
// runtime that no longer exists, so reusing frames produces a video that is
// internally inconsistent rather than merely long.
const DurationDriftPercent = 15

// Plan is what one edit costs: which segs have to be produced again and which
// can be taken from cache.
type Plan struct {
	ProjectID string
	// FullRerun means a boundary forced the entire project to be produced
	// again.
	//
	// It is not merely "everything ended up invalidated". Changing the voice
	// also invalidates every seg, but for a reason that has nothing to do with
	// the boundaries; folding the two together would make the report unable to
	// say how often the boundaries actually fire, which is the number that
	// decides whether the boundaries were drawn in the right places.
	FullRerun bool
	// Boundary is the boundary that forced the rerun, BoundaryNone otherwise.
	Boundary Boundary
	// Reason is a human-readable explanation of Boundary, empty when no
	// boundary fired. It names the specific seg, group or figure that tripped
	// it, because "your edit triggered a full re-render" with no subject is a
	// message a user cannot act on.
	Reason string
	// Invalidated and Reused partition the project's segs, both in render
	// order so two runs over the same input produce identical plans.
	Invalidated []string
	Reused      []string
	// CostSavedMicros is what the reused artifacts originally cost to produce,
	// in millionths of a USD. It is money genuinely not spent again, summed
	// from recorded costs rather than estimated.
	CostSavedMicros int64
}

// TotalSegs is the size of the project the plan was computed over.
func (p Plan) TotalSegs() int { return len(p.Invalidated) + len(p.Reused) }

// InvalidationRate is the share of segs that must be produced again, in [0,1].
// An empty project reports 0: nothing was invalidated because nothing existed.
func (p Plan) InvalidationRate() float64 {
	total := p.TotalSegs()
	if total == 0 {
		return 0
	}
	return float64(len(p.Invalidated)) / float64(total)
}

// ValidateFor checks that a plan is a complete, unambiguous partition of the
// project it will be used to render. Plans cross a package boundary into the
// render executor, so accepting unknown, duplicate, or missing seg ids would
// make it possible to silently skip work.
func (p Plan) ValidateFor(project model.Project) error {
	if p.ProjectID != project.ID {
		return fmt.Errorf("recompile plan belongs to project %q, not %q", p.ProjectID, project.ID)
	}
	order, err := project.RenderOrder()
	if err != nil {
		return fmt.Errorf("validate recompile plan for project %s: %w", project.ID, err)
	}
	want := make(map[string]struct{}, len(order))
	for _, id := range order {
		want[id] = struct{}{}
	}
	seen := make(map[string]string, len(order))
	check := func(kind string, ids []string) error {
		for _, id := range ids {
			if _, ok := want[id]; !ok {
				return fmt.Errorf("recompile plan %s contains unknown seg %q", kind, id)
			}
			if previous, ok := seen[id]; ok {
				return fmt.Errorf("recompile plan seg %q appears in both %s and %s", id, previous, kind)
			}
			seen[id] = kind
		}
		return nil
	}
	if err := check("invalidated", p.Invalidated); err != nil {
		return err
	}
	if err := check("reused", p.Reused); err != nil {
		return err
	}
	if len(seen) != len(want) {
		for _, id := range order {
			if _, ok := seen[id]; !ok {
				return fmt.Errorf("recompile plan omits seg %q", id)
			}
		}
	}
	if p.FullRerun {
		if p.Boundary == BoundaryNone {
			return fmt.Errorf("full recompile plan has no boundary")
		}
		if len(p.Reused) != 0 || len(p.Invalidated) != len(order) {
			return fmt.Errorf("full recompile plan must invalidate all %d segs and reuse none", len(order))
		}
	} else if p.Boundary != BoundaryNone {
		return fmt.Errorf("incremental recompile plan unexpectedly names boundary %q", p.Boundary)
	}
	return nil
}

// String renders the plan as one line for logs and CLI output.
func (p Plan) String() string {
	if p.FullRerun {
		return fmt.Sprintf("full rerun of %d segs (%s): %s", p.TotalSegs(), p.Boundary, p.Reason)
	}
	return fmt.Sprintf("%d of %d segs invalidated, %d reused",
		len(p.Invalidated), p.TotalSegs(), len(p.Reused))
}
