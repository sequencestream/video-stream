package recompile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

// Cache resolves a render cache key to a previously produced artifact.
//
// It is narrower than store.ArtifactStore because planning never writes: an
// engine that could record artifacts could also record one it did not produce,
// and a cache holding an artifact nobody rendered serves frames that do not
// exist.
type Cache interface {
	Artifact(ctx context.Context, renderCacheKey string) (store.Artifact, error)
}

// Runs persists and reads back recompilation outcomes.
type Runs interface {
	RecordRun(ctx context.Context, r store.RecompileRun) error
	RecompileRuns(ctx context.Context, projectID string, limit int) ([]store.RecompileRun, error)
}

// Options configures an Engine. Every field has a working zero value.
type Options struct {
	// Cache is consulted for reusable artifacts. Nil means nothing is cached,
	// which is the correct reading of "no cache", not an error.
	Cache Cache
	// Runs persists outcomes for the report. Nil discards them, and Report
	// then says so by reporting zero runs rather than pretending to a verdict.
	Runs Runs
	// Reporter receives a recompile.planned event per plan.
	Reporter telemetry.Reporter
	Logger   *slog.Logger
}

// Engine plans recompilations and reports on how well they are going.
type Engine struct {
	cache    Cache
	runs     Runs
	reporter telemetry.Reporter
	logger   *slog.Logger
}

// New builds an Engine.
func New(opts Options) *Engine {
	e := &Engine{cache: opts.Cache, runs: opts.Runs, reporter: opts.Reporter, logger: opts.Logger}
	if e.reporter == nil {
		e.reporter = telemetry.Nop()
	}
	if e.logger == nil {
		e.logger = slog.New(slog.DiscardHandler)
	}
	return e
}

// Plan works out what the edit from previous to next has to produce again.
//
// A zero-valued previous means a first compile: there is no edit to classify,
// so no boundary can fire, but the cache is still consulted. Two projects that
// say the same thing share artifacts even if neither has ever seen the other,
// which is the reason the render cache key excludes seg_id.
//
// The plan is recorded and reported before it is returned. A telemetry or
// recording failure is logged and swallowed: measurement exists to observe the
// pipeline, and a pipeline that fails because its observer did is worse than an
// unobserved one.
func (e *Engine) Plan(ctx context.Context, previous, next model.Project) (Plan, error) {
	return e.PlanWithID(ctx, "", previous, next)
}

// PlanWithID records the plan under a stable id so retries replace the same
// measurement instead of inflating the invalidation report.
func (e *Engine) PlanWithID(ctx context.Context, runID string, previous, next model.Project) (Plan, error) {
	nextOrder, err := next.RenderOrder()
	if err != nil {
		return Plan{}, fmt.Errorf("plan recompile of project %s: %w", next.ID, err)
	}
	prevOrder, err := previousOrder(previous)
	if err != nil {
		return Plan{}, fmt.Errorf("plan recompile of project %s: %w", next.ID, err)
	}

	changed := diff(previous, next)

	plan := Plan{RunID: runID, ProjectID: next.ID}
	if len(previous.Segs) > 0 {
		if boundary, reason := detectBoundary(previous, next, prevOrder, nextOrder, changed.touched()); boundary != BoundaryNone {
			plan.FullRerun = true
			plan.Boundary = boundary
			plan.Reason = reason
			plan.Invalidated = nextOrder
			e.finish(ctx, runID, plan)
			return plan, nil
		}
	}

	dirty := dependentsOf(next, changed.touched())
	for _, segID := range nextOrder {
		seg, ok := next.Seg(segID)
		if !ok {
			continue
		}
		_, upstreamChanged := dirty[segID]
		_, rewired := changed.rewired[segID]
		if upstreamChanged || rewired {
			plan.Invalidated = append(plan.Invalidated, segID)
			continue
		}

		artifact, found, err := e.lookup(ctx, seg.RenderCacheKey)
		if err != nil {
			return Plan{}, fmt.Errorf("plan recompile of project %s: %w", next.ID, err)
		}
		if found && seg.CanReuse(artifact.RenderCacheKey, artifact.DurationMS) {
			plan.Reused = append(plan.Reused, segID)
			plan.CostSavedMicros += artifact.CostMicros
			continue
		}
		plan.Invalidated = append(plan.Invalidated, segID)
	}

	e.finish(ctx, runID, plan)
	return plan, nil
}

// Report aggregates recorded runs. An empty projectID covers every project.
func (e *Engine) Report(ctx context.Context, projectID string) (Report, error) {
	if e.runs == nil {
		return Report{}, nil
	}
	runs, err := e.runs.RecompileRuns(ctx, projectID, 0)
	if err != nil {
		return Report{}, fmt.Errorf("read recompile runs: %w", err)
	}
	return Aggregate(runs), nil
}

func (e *Engine) lookup(ctx context.Context, key string) (store.Artifact, bool, error) {
	if e.cache == nil || key == "" {
		return store.Artifact{}, false, nil
	}
	artifact, err := e.cache.Artifact(ctx, key)
	if errors.Is(err, store.ErrArtifactNotFound) {
		return store.Artifact{}, false, nil
	}
	if err != nil {
		return store.Artifact{}, false, err
	}
	return artifact, true, nil
}

// finish records and reports a plan. Neither failure is fatal; see Plan.
func (e *Engine) finish(ctx context.Context, runID string, p Plan) {
	if e.runs != nil {
		if runID == "" {
			runID = newRunID()
		}
		run := store.RecompileRun{
			ID:              runID,
			ProjectID:       p.ProjectID,
			PlannedAt:       time.Now().UTC(),
			TotalSegs:       p.TotalSegs(),
			InvalidatedSegs: len(p.Invalidated),
			FullRerun:       p.FullRerun,
			Boundary:        string(p.Boundary),
			CostSavedMicros: p.CostSavedMicros,
		}
		if err := e.runs.RecordRun(ctx, run); err != nil {
			e.logger.WarnContext(ctx, "recording a recompile run failed", slog.String("error", err.Error()))
		}
	}

	err := telemetry.Report(ctx, e.reporter, "recompile.planned", map[string]any{
		"project_id":        p.ProjectID,
		"total_segs":        p.TotalSegs(),
		"invalidated_segs":  len(p.Invalidated),
		"reused_segs":       len(p.Reused),
		"invalidation_rate": p.InvalidationRate(),
		"full_rerun":        p.FullRerun,
		"boundary":          string(p.Boundary),
		"cost_saved_micros": p.CostSavedMicros,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		e.logger.WarnContext(ctx, "reporting a recompile plan failed", slog.String("error", err.Error()))
	}
}

// changes is what moved between two versions of a project.
//
// The two sets are kept apart because they license different things. A seg
// whose wording moved may still be served from cache, since that wording may
// have been rendered before; a seg whose wiring moved may not, since the
// artifact under its unchanged key was produced against different upstream
// material.
type changes struct {
	// content holds new segs and segs whose render cache key shifted.
	content map[string]struct{}
	// rewired holds segs whose depends_on changed. depends_on deliberately
	// stays out of the render cache key, so nothing about the key reveals this.
	rewired map[string]struct{}
}

// touched is every seg that moved for any reason, and is what seeds
// invalidation of the segs composed against them.
func (c changes) touched() map[string]struct{} {
	out := make(map[string]struct{}, len(c.content)+len(c.rewired))
	maps.Copy(out, c.content)
	maps.Copy(out, c.rewired)
	return out
}

// diff compares two versions of a project.
//
// Removed segs are not listed: a seg that no longer exists cannot be
// re-rendered. What matters about a removal is its effect on the segs that
// pointed at it, and those surface here as their own depends_on change.
func diff(previous, next model.Project) changes {
	before := make(map[string]model.Seg, len(previous.Segs))
	for _, s := range previous.Segs {
		before[s.SegID] = s
	}

	c := changes{content: map[string]struct{}{}, rewired: map[string]struct{}{}}
	for _, s := range next.Segs {
		prev, existed := before[s.SegID]
		if !existed || prev.RenderCacheKey != s.RenderCacheKey {
			c.content[s.SegID] = struct{}{}
		}
		if existed && !slices.Equal(prev.DependsOn, s.DependsOn) {
			c.rewired[s.SegID] = struct{}{}
		}
	}
	return c
}

// dependentsOf returns the transitive dependents of the given segs, excluding
// those segs themselves.
//
// The exclusion is the interesting part. A seg whose own text changed may still
// hit the cache — that wording may have been rendered before, in this project
// or another one — so it is not invalid by definition. Its dependents are,
// because they were composed against content that has now moved, and no cached
// artifact of theirs was produced against the new content.
func dependentsOf(p model.Project, changed map[string]struct{}) map[string]struct{} {
	dependents := make(map[string][]string, len(p.Segs))
	for _, s := range p.Segs {
		for _, dep := range s.DependsOn {
			dependents[dep] = append(dependents[dep], s.SegID)
		}
	}

	dirty := map[string]struct{}{}
	queue := slices.Sorted(maps.Keys(changed))
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range dependents[current] {
			if _, seen := dirty[next]; seen {
				continue
			}
			dirty[next] = struct{}{}
			queue = append(queue, next)
		}
	}
	for id := range changed {
		delete(dirty, id)
	}
	return dirty
}

// previousOrder is RenderOrder for a project that may be the zero value.
func previousOrder(previous model.Project) ([]string, error) {
	if len(previous.Segs) == 0 {
		return nil, nil
	}
	return previous.RenderOrder()
}

// newRunID returns a random identifier for one recorded run.
func newRunID() string {
	var b [16]byte
	// rand.Read on crypto/rand cannot fail as of Go 1.24; it panics instead.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
