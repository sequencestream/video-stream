package recompile_test

import (
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/recompile"
)

// assertFullRerun is the shared assertion behind every boundary case: the
// project is produced again in full, the machine-readable code is right, and
// the message names the specific thing that tripped it. The last part matters
// most — "your edit triggered a full re-render" with no subject is a message a
// user cannot act on, and the boundaries are only defensible if they can
// explain themselves.
func assertFullRerun(t *testing.T, p recompile.Plan, want recompile.Boundary, mustName string) {
	t.Helper()

	if !p.FullRerun {
		t.Fatalf("expected a full rerun, got %d invalidated / %d reused", len(p.Invalidated), len(p.Reused))
	}
	if p.Boundary != want {
		t.Fatalf("boundary = %q, want %q", p.Boundary, want)
	}
	if len(p.Reused) != 0 {
		t.Fatalf("a full rerun reused %v", p.Reused)
	}
	if p.TotalSegs() != 5 {
		t.Fatalf("a full rerun covered %d segs, want all 5", p.TotalSegs())
	}
	if p.CostSavedMicros != 0 {
		t.Fatalf("a full rerun claimed %d micros saved", p.CostSavedMicros)
	}
	if p.Reason == "" {
		t.Fatal("boundary fired with no reason")
	}
	if !strings.Contains(p.Reason, mustName) {
		t.Fatalf("reason %q does not name %q", p.Reason, mustName)
	}
}

// boundaryEngine returns an engine whose cache holds every artifact of the
// given project, so any rerun in these tests is caused by a boundary rather
// than by a cache miss.
func boundaryEngine(p model.Project) *recompile.Engine {
	cache := newMemoryCache()
	cache.fill(p, 1_000_000)
	return recompile.New(recompile.Options{Cache: cache})
}

// Boundary 1. Frames carry the visual identity they were rendered under, so
// reusing them after a style change produces a video that visibly switches
// styles partway through.
func TestStyleAnchorChangeForcesAFullRerun(t *testing.T) {
	previous := fiveSegScript(t)
	previous.RenderProfile.StyleAnchor = "l2-warm"
	previous.Seal()

	next := previous
	next.Segs = append([]model.Seg(nil), previous.Segs...)
	next.RenderProfile.StyleAnchor = "l2-noir"
	next.Seal()

	got := plan(t, boundaryEngine(previous), previous, next)
	assertFullRerun(t, got, recompile.BoundaryStyleAnchor, "l2-noir")
}

// Boundary 2. Past the drift limit, shot lengths and pacing were chosen against
// a runtime that no longer exists, so kept frames are not merely old, they are
// cut wrong.
func TestDurationDriftPastTheLimitForcesAFullRerun(t *testing.T) {
	previous := fiveSegScript(t)

	next := previous
	next.Segs = append([]model.Seg(nil), previous.Segs...)
	// 10s total; stretching one seg from 2s to 4s is +20%, past the 15% limit.
	next.Segs[1].DurationBudget = model.NewDurationBudget(4000)
	next.Seal()

	got := plan(t, boundaryEngine(previous), previous, next)
	assertFullRerun(t, got, recompile.BoundaryDurationDrift, "15%")
}

// The complement of the case above, and the more important half: drift inside
// the limit must stay incremental. A boundary that fires on ordinary edits
// would quietly turn every recompilation into a full one.
func TestDurationDriftInsideTheLimitStaysIncremental(t *testing.T) {
	previous := fiveSegScript(t)

	next := previous
	next.Segs = append([]model.Seg(nil), previous.Segs...)
	// 10s total; 2000ms -> 3000ms is exactly +10%.
	next.Segs[1].DurationBudget = model.NewDurationBudget(3000)
	next.Seal()

	got := plan(t, boundaryEngine(previous), previous, next)
	if got.FullRerun {
		t.Fatalf("a 10%% runtime change forced a full rerun: %s", got.Reason)
	}
}

// Boundary 3. The arc is the emotion sequence, not the per-seg tag: reordering
// two segs leaves every tag intact and still re-lays out the whole arc.
func TestBeatReorderForcesAFullRerun(t *testing.T) {
	previous := fiveSegScript(t)
	previous.Segs[2].EmotionTag = model.EmotionUrgent
	previous.Seal()

	next := previous
	next.Segs = append([]model.Seg(nil), previous.Segs...)
	next.Segs[2].EmotionTag = model.EmotionNeutral
	next.Segs[1].EmotionTag = model.EmotionUrgent
	next.Seal()

	got := plan(t, boundaryEngine(previous), previous, next)
	assertFullRerun(t, got, recompile.BoundaryBeatReordered, "urgent")
}

// Boundary 4. Everything after the hook is cut against the pace it sets, so an
// opening rewrite is a re-plan of the whole video rather than an edit to its
// first two seconds.
func TestHookEditForcesAFullRerun(t *testing.T) {
	previous := fiveSegScript(t)
	next := editText(t, previous, "s1", "第一段：一个完全不同的钩子")

	got := plan(t, boundaryEngine(previous), previous, next)
	assertFullRerun(t, got, recompile.BoundaryHookEdited, "s1")
}

// Boundary 5. Shots inside one continuous action cannot be re-rendered
// piecemeal; the seam shows as a jump in the middle of the movement.
func TestContinuityGroupEditForcesAFullRerun(t *testing.T) {
	previous := fiveSegScript(t)
	previous.Segs[2].ContinuityGroup = "chase"
	previous.Segs[3].ContinuityGroup = "chase"
	previous.Seal()

	next := editText(t, previous, "s3", "第三段：追逐中的另一句")

	got := plan(t, boundaryEngine(previous), previous, next)
	assertFullRerun(t, got, recompile.BoundaryContinuityBroken, "chase")
}

// Regrouping never touches a seg's own content, so it never reaches the changed
// set. It still re-partitions exactly the shots the boundary exists to keep
// together, which is why membership is compared as well as content.
func TestRegroupingContinuityForcesAFullRerun(t *testing.T) {
	previous := fiveSegScript(t)
	previous.Segs[2].ContinuityGroup = "chase"
	previous.Segs[3].ContinuityGroup = "chase"
	previous.Seal()

	next := previous
	next.Segs = append([]model.Seg(nil), previous.Segs...)
	next.Segs[4].ContinuityGroup = "chase" // s5 joins the action
	next.Seal()

	got := plan(t, boundaryEngine(previous), previous, next)
	assertFullRerun(t, got, recompile.BoundaryContinuityBroken, "s5")
}

// Boundary 6. Shots generated in one call were conditioned on each other, so
// one regenerated alone no longer matches its neighbours.
func TestGenerationBatchEditForcesAFullRerun(t *testing.T) {
	previous := fiveSegScript(t)
	previous.Segs[1].GenerationBatch = "batch-7"
	previous.Segs[2].GenerationBatch = "batch-7"
	previous.Seal()

	next := editText(t, previous, "s2", "第二段：同批次里的另一句")

	got := plan(t, boundaryEngine(previous), previous, next)
	assertFullRerun(t, got, recompile.BoundaryBatchBroken, "batch-7")
}

// When two boundaries fire at once the reported one must not depend on which
// check happens to be written first. Style is reported over drift because a
// style change is a cause and the drift beside it is a coincidence.
func TestProjectWideBoundariesAreReportedBeforeLocalOnes(t *testing.T) {
	previous := fiveSegScript(t)
	previous.RenderProfile.StyleAnchor = "l2-warm"
	previous.Seal()

	next := previous
	next.Segs = append([]model.Seg(nil), previous.Segs...)
	next.RenderProfile.StyleAnchor = "l2-noir"
	next.Segs[1].DurationBudget = model.NewDurationBudget(6000)
	next.Segs[0].Text = "第一段：也改了钩子"
	next.Seal()

	got := plan(t, boundaryEngine(previous), previous, next)
	assertFullRerun(t, got, recompile.BoundaryStyleAnchor, "l2-noir")
}

// Segs outside any group must not make an ordinary edit look like a group
// change. Collecting them under the empty group name would trip both group
// boundaries on every edit and turn the whole feature off.
func TestAnEditOutsideAnyGroupStaysIncremental(t *testing.T) {
	previous := fiveSegScript(t)
	previous.Segs[0].ContinuityGroup = "opening"
	previous.Seal()

	next := editText(t, previous, "s3", "第三段：改了，但不在任何组里")

	got := plan(t, boundaryEngine(previous), previous, next)
	if got.FullRerun {
		t.Fatalf("an edit outside every group forced a full rerun: %s", got.Reason)
	}
	assertSegs(t, "invalidated", got.Invalidated, []string{"s3", "s4"})
}

// A first compile has nothing to compare against, so no boundary can fire. If
// one did, every project would begin life with a boundary on record and the
// report's boundary counts would be meaningless.
func TestAFirstCompileCrossesNoBoundary(t *testing.T) {
	next := fiveSegScript(t)
	next.RenderProfile.StyleAnchor = "l2-noir"
	next.Seal()

	got := plan(t, recompile.New(recompile.Options{}), model.Project{}, next)
	if got.FullRerun || got.Boundary != recompile.BoundaryNone {
		t.Fatalf("a first compile reported boundary %q: %s", got.Boundary, got.Reason)
	}
}
