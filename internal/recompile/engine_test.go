package recompile_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/recompile"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

// memoryCache is a hand-rolled stand-in for the artifact store. It stays here
// rather than pulling in SQLite so a planning bug is not diagnosed through a
// database.
type memoryCache struct {
	artifacts map[string]store.Artifact
	err       error
}

func newMemoryCache() *memoryCache {
	return &memoryCache{artifacts: map[string]store.Artifact{}}
}

func (c *memoryCache) Artifact(_ context.Context, key string) (store.Artifact, error) {
	if c.err != nil {
		return store.Artifact{}, c.err
	}
	a, ok := c.artifacts[key]
	if !ok {
		return store.Artifact{}, store.ErrArtifactNotFound
	}
	return a, nil
}

// fill records an artifact for every seg in the project at its exact budget
// midpoint, i.e. the state right after a clean full render.
func (c *memoryCache) fill(p model.Project, costMicros int64) {
	for _, s := range p.Segs {
		c.artifacts[s.RenderCacheKey] = store.Artifact{
			RenderCacheKey: s.RenderCacheKey,
			DurationMS:     s.DurationBudget.TargetMS(),
			CostMicros:     costMicros,
			CreatedAt:      time.Now().UTC(),
		}
	}
}

type memoryRuns struct {
	runs []store.RecompileRun
}

func (r *memoryRuns) RecordRun(_ context.Context, run store.RecompileRun) error {
	r.runs = append(r.runs, run)
	return nil
}

func (r *memoryRuns) RecompileRuns(_ context.Context, projectID string, _ int) ([]store.RecompileRun, error) {
	if projectID == "" {
		return r.runs, nil
	}
	var out []store.RecompileRun
	for _, run := range r.runs {
		if run.ProjectID == projectID {
			out = append(out, run)
		}
	}
	return out, nil
}

// fiveSegScript builds the script the acceptance case is written against:
// five segs, s4 composed against s3, everything else independent.
func fiveSegScript(t *testing.T) model.Project {
	t.Helper()

	p := model.NewProject("p1", "five段脚本", time.Unix(0, 0))
	p.Segs = []model.Seg{
		model.NewSeg("s1", "第一段：开场白", 2000),
		model.NewSeg("s2", "第二段：背景交代", 2000),
		model.NewSeg("s3", "第三段：转折", 2000),
		model.NewSeg("s4", "第四段：承接转折的后续", 2000),
		model.NewSeg("s5", "第五段：收尾", 2000),
	}
	p.Segs[3].DependsOn = []string{"s3"}
	p.Seal()
	if err := p.Validate(); err != nil {
		t.Fatalf("fixture is invalid: %v", err)
	}
	return p
}

func editText(t *testing.T, p model.Project, segID, text string) model.Project {
	t.Helper()

	next := p
	next.Segs = append([]model.Seg(nil), p.Segs...)
	for i := range next.Segs {
		if next.Segs[i].SegID == segID {
			next.Segs[i].Text = text
			next.Seal()
			return next
		}
	}
	t.Fatalf("no seg %s in the fixture", segID)
	return model.Project{}
}

func plan(t *testing.T, e *recompile.Engine, previous, next model.Project) recompile.Plan {
	t.Helper()

	p, err := e.Plan(context.Background(), previous, next)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return p
}

func assertSegs(t *testing.T, label string, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

// This is the headline acceptance case: rewriting one line in the middle of a
// five-段 script must re-render that seg and what depends on it, and nothing
// else. If this test ever starts invalidating s1, s2 or s5, the incremental
// path has stopped paying for itself.
func TestEditingOneSegInvalidatesOnlyItAndItsDependents(t *testing.T) {
	previous := fiveSegScript(t)
	cache := newMemoryCache()
	cache.fill(previous, 3_000_000) // $3 a seg, the cost this feature exists to avoid

	engine := recompile.New(recompile.Options{Cache: cache})
	next := editText(t, previous, "s3", "第三段：换一句完全不同的转折")

	got := plan(t, engine, previous, next)

	if got.FullRerun {
		t.Fatalf("a plain line edit forced a full rerun: %s", got.Reason)
	}
	assertSegs(t, "invalidated", got.Invalidated, []string{"s3", "s4"})
	assertSegs(t, "reused", got.Reused, []string{"s1", "s2", "s5"})
	if got.CostSavedMicros != 9_000_000 {
		t.Fatalf("cost saved = %d micros, want 9000000 (three reused segs at $3)", got.CostSavedMicros)
	}
	if rate := got.InvalidationRate(); rate != 0.4 {
		t.Fatalf("invalidation rate = %v, want 0.4", rate)
	}
}

// A seg whose own text moved may still hit the cache, because the render cache
// key excludes seg_id and the same wording may have been rendered in another
// project. Its dependents are invalid regardless: they were composed against
// content that has now changed, whatever artifacts happen to exist.
func TestAnEditedSegCanStillHitTheCacheButItsDependentsCannot(t *testing.T) {
	previous := fiveSegScript(t)
	next := editText(t, previous, "s3", "第三段：换一句完全不同的转折")

	// Pretend the new wording was rendered before, somewhere else.
	cache := newMemoryCache()
	cache.fill(next, 1_000_000)

	got := plan(t, recompile.New(recompile.Options{Cache: cache}), previous, next)

	assertSegs(t, "invalidated", got.Invalidated, []string{"s4"})
	assertSegs(t, "reused", got.Reused, []string{"s1", "s2", "s3", "s5"})
}

// The budget is the second gate of a cache hit. An artifact whose real duration
// has fallen outside the budget must not be reused even though its key matches,
// otherwise the seg renders at a length the rest of the cut was not built for.
func TestAnArtifactOutsideTheBudgetIsNotReused(t *testing.T) {
	previous := fiveSegScript(t)
	cache := newMemoryCache()
	cache.fill(previous, 1_000_000)

	stale := cache.artifacts[previous.Segs[1].RenderCacheKey]
	stale.DurationMS = previous.Segs[1].DurationBudget.MaxMS + 1
	cache.artifacts[stale.RenderCacheKey] = stale

	got := plan(t, recompile.New(recompile.Options{Cache: cache}), previous, previous)

	assertSegs(t, "invalidated", got.Invalidated, []string{"s2"})
	assertSegs(t, "reused", got.Reused, []string{"s1", "s3", "s4", "s5"})
}

// An empty cache is not an error state. It is what a first compile looks like,
// and it must produce a plan rather than a failure.
func TestAFirstCompileWithNoCacheInvalidatesEverything(t *testing.T) {
	next := fiveSegScript(t)

	got := plan(t, recompile.New(recompile.Options{}), model.Project{}, next)

	if got.FullRerun {
		t.Fatal("a first compile reported a boundary; there was no edit to cross one")
	}
	assertSegs(t, "invalidated", got.Invalidated, []string{"s1", "s2", "s3", "s4", "s5"})
	if len(got.Reused) != 0 {
		t.Fatalf("reused = %v, want nothing", got.Reused)
	}
}

// A first compile still consults the cache. Two projects that say the same
// thing share artifacts even if neither has ever seen the other; that sharing
// is the reason seg_id stays out of the render cache key.
func TestAFirstCompileStillReusesArtifactsFromAnotherProject(t *testing.T) {
	other := fiveSegScript(t)
	cache := newMemoryCache()
	cache.fill(other, 2_000_000)

	fresh := other
	fresh.ID = "p2"

	got := plan(t, recompile.New(recompile.Options{Cache: cache}), model.Project{}, fresh)

	if len(got.Invalidated) != 0 {
		t.Fatalf("invalidated = %v, want nothing: every seg exists in the cache", got.Invalidated)
	}
	if got.CostSavedMicros != 10_000_000 {
		t.Fatalf("cost saved = %d micros, want 10000000", got.CostSavedMicros)
	}
}

// Rewiring depends_on leaves every hash untouched, so the cache will happily
// hand back an artifact for the rewired seg. It must not be taken: that
// artifact was produced when the seg followed nothing, and it now follows s4.
func TestRewiringADependencyInvalidatesTheRewiredSeg(t *testing.T) {
	previous := fiveSegScript(t)
	cache := newMemoryCache()
	cache.fill(previous, 1_000_000)

	next := previous
	next.Segs = append([]model.Seg(nil), previous.Segs...)
	next.Segs[4].DependsOn = []string{"s4"} // s5 now follows s4
	next.Seal()

	got := plan(t, recompile.New(recompile.Options{Cache: cache}), previous, next)

	assertSegs(t, "invalidated", got.Invalidated, []string{"s5"})
}

// Planning must not be able to fail because measurement failed. A recorder that
// errors is a broken observer, and a broken observer that stops the pipeline is
// worse than no observer at all.
func TestPlanSucceedsWhenRecordingFails(t *testing.T) {
	previous := fiveSegScript(t)
	engine := recompile.New(recompile.Options{Runs: failingRuns{}})

	if _, err := engine.Plan(context.Background(), previous, previous); err != nil {
		t.Fatalf("Plan failed because its recorder did: %v", err)
	}
}

type failingRuns struct{}

func (failingRuns) RecordRun(context.Context, store.RecompileRun) error {
	return context.DeadlineExceeded
}

func (failingRuns) RecompileRuns(context.Context, string, int) ([]store.RecompileRun, error) {
	return nil, context.DeadlineExceeded
}

func TestPlanReportsOneTelemetryEventPerPlan(t *testing.T) {
	previous := fiveSegScript(t)
	cache := newMemoryCache()
	cache.fill(previous, 1_000_000)

	reporter := telemetry.NewMemoryReporter()
	engine := recompile.New(recompile.Options{Cache: cache, Reporter: reporter})

	next := editText(t, previous, "s3", "第三段：改了")
	plan(t, engine, previous, next)

	events := reporter.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want exactly 1", len(events))
	}
	if events[0].Name != "recompile.planned" {
		t.Fatalf("event name = %q, want recompile.planned", events[0].Name)
	}
	for _, key := range []string{
		"project_id", "total_segs", "invalidated_segs", "reused_segs",
		"invalidation_rate", "full_rerun", "boundary", "cost_saved_micros",
	} {
		if _, ok := events[0].Attributes[key]; !ok {
			t.Fatalf("event is missing attribute %q", key)
		}
	}
	if got := events[0].Attributes["invalidated_segs"]; got != 2 {
		t.Fatalf("invalidated_segs = %v, want 2", got)
	}
}

// A cycle has to surface as a planning error rather than a partial plan: a plan
// missing the segs the cycle swallowed would silently under-report what needs
// re-rendering.
func TestPlanRejectsAProjectWhoseGraphDoesNotResolve(t *testing.T) {
	broken := fiveSegScript(t)
	broken.Segs = append([]model.Seg(nil), broken.Segs...)
	broken.Segs[2].DependsOn = []string{"s4"} // s3 <-> s4
	broken.Seal()

	_, err := recompile.New(recompile.Options{}).Plan(context.Background(), model.Project{}, broken)
	if err == nil {
		t.Fatal("planning a cyclic project succeeded")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error %q does not name the cycle", err)
	}
}

func TestReportAggregatesRecordedRuns(t *testing.T) {
	previous := fiveSegScript(t)
	cache := newMemoryCache()
	cache.fill(previous, 1_000_000)

	runs := &memoryRuns{}
	engine := recompile.New(recompile.Options{Cache: cache, Runs: runs})

	next := editText(t, previous, "s3", "第三段：改了")
	plan(t, engine, previous, next)

	report, err := engine.Report(context.Background(), "p1")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if report.Runs != 1 {
		t.Fatalf("runs = %d, want 1", report.Runs)
	}
	if report.InvalidatedSegs != 2 || report.ReusedSegs != 3 {
		t.Fatalf("invalidated/reused = %d/%d, want 2/3", report.InvalidatedSegs, report.ReusedSegs)
	}
	if report.CostSavedMicros != 3_000_000 {
		t.Fatalf("cost saved = %d micros, want 3000000", report.CostSavedMicros)
	}
}

// An engine with nowhere to record runs must not invent a verdict. Reporting
// "viable" off zero evidence is the specific failure this whole telemetry
// story exists to prevent.
func TestReportWithoutARunStoreClaimsNothing(t *testing.T) {
	report, err := recompile.New(recompile.Options{}).Report(context.Background(), "")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if report.Runs != 0 {
		t.Fatalf("runs = %d, want 0", report.Runs)
	}
	if got := report.Verdict(); got != recompile.VerdictInsufficientData {
		t.Fatalf("verdict = %q, want %q", got, recompile.VerdictInsufficientData)
	}
}
