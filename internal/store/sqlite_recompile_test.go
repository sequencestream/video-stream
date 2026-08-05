package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/store"
)

func TestPutArtifactRoundTripsEveryField(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	want := store.Artifact{
		RenderCacheKey: "rk2:abc",
		DurationMS:     2040,
		URI:            "file:///tmp/abc.mp4",
		CostMicros:     3_500_000,
		CreatedAt:      time.UnixMilli(1_700_000_000_000).UTC(),
	}
	if err := s.PutArtifact(ctx, want); err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}

	got, err := s.Artifact(ctx, "rk2:abc")
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if got != want {
		t.Fatalf("round trip changed the artifact:\n got %+v\nwant %+v", got, want)
	}
}

func TestArtifactReportsAMissingKey(t *testing.T) {
	if _, err := openStore(t).Artifact(context.Background(), "rk2:nope"); !errors.Is(err, store.ErrArtifactNotFound) {
		t.Fatalf("got %v, want ErrArtifactNotFound", err)
	}
}

// The same key rendered twice is the same content through the same pipeline,
// so the newer row is the better measurement. Rejecting the second write would
// pin the cache to a stale duration forever.
func TestPutArtifactReplacesAnEarlierRenderOfTheSameKey(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	first := store.Artifact{RenderCacheKey: "rk2:abc", DurationMS: 2000, CostMicros: 100, CreatedAt: time.UnixMilli(1)}
	if err := s.PutArtifact(ctx, first); err != nil {
		t.Fatalf("PutArtifact first: %v", err)
	}
	second := store.Artifact{RenderCacheKey: "rk2:abc", DurationMS: 2100, URI: "file:///b.mp4", CostMicros: 200, CreatedAt: time.UnixMilli(2)}
	if err := s.PutArtifact(ctx, second); err != nil {
		t.Fatalf("PutArtifact second: %v", err)
	}

	got, err := s.Artifact(ctx, "rk2:abc")
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if got.DurationMS != 2100 || got.URI != "file:///b.mp4" || got.CostMicros != 200 {
		t.Fatalf("got %+v, want the second render", got)
	}
}

// A zero or negative duration passes every budget check that happens to start
// at zero, which would license reuse of a broken artifact.
func TestPutArtifactRejectsANonPositiveDuration(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	for _, duration := range []int64{0, -1} {
		err := s.PutArtifact(ctx, store.Artifact{RenderCacheKey: "rk2:abc", DurationMS: duration})
		if err == nil {
			t.Fatalf("duration %d was accepted", duration)
		}
		if _, err := s.Artifact(ctx, "rk2:abc"); !errors.Is(err, store.ErrArtifactNotFound) {
			t.Fatalf("a rejected artifact was written anyway: %v", err)
		}
	}
}

func TestPutArtifactRejectsAnEmptyKeyAndANegativeCost(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	if err := s.PutArtifact(ctx, store.Artifact{DurationMS: 2000}); err == nil {
		t.Fatal("an artifact with no render cache key was accepted")
	}
	if err := s.PutArtifact(ctx, store.Artifact{RenderCacheKey: "rk2:abc", DurationMS: 2000, CostMicros: -1}); err == nil {
		t.Fatal("a negative cost was accepted")
	}
}

func TestPutArtifactStampsAMissingCreatedAt(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	before := time.Now().Add(-time.Second)
	if err := s.PutArtifact(ctx, store.Artifact{RenderCacheKey: "rk2:abc", DurationMS: 2000}); err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}

	got, err := s.Artifact(ctx, "rk2:abc")
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if got.CreatedAt.Before(before) {
		t.Fatalf("created_at %v was not stamped", got.CreatedAt)
	}
}

func TestRecordRunRoundTripsEveryField(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	want := store.RecompileRun{
		ID:              "run-1",
		ProjectID:       "p1",
		PlannedAt:       time.UnixMilli(1_700_000_000_000).UTC(),
		TotalSegs:       5,
		InvalidatedSegs: 2,
		FullRerun:       false,
		Boundary:        "",
		CostSavedMicros: 9_000_000,
	}
	if err := s.RecordRun(ctx, want); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	got, err := s.RecompileRuns(ctx, "p1", 10)
	if err != nil {
		t.Fatalf("RecompileRuns: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d runs, want 1", len(got))
	}
	if got[0] != want {
		t.Fatalf("round trip changed the run:\n got %+v\nwant %+v", got[0], want)
	}
}

// full_rerun is a bool over the wire and an integer on disk; the report reads
// the two apart, so the conversion has to survive both directions.
func TestRecordRunRoundTripsAFullRerun(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	want := store.RecompileRun{
		ID: "run-1", ProjectID: "p1", PlannedAt: time.UnixMilli(1),
		TotalSegs: 5, InvalidatedSegs: 5, FullRerun: true, Boundary: "style_anchor",
	}
	if err := s.RecordRun(ctx, want); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	got, err := s.RecompileRuns(ctx, "p1", 10)
	if err != nil {
		t.Fatalf("RecompileRuns: %v", err)
	}
	if len(got) != 1 || !got[0].FullRerun || got[0].Boundary != "style_anchor" {
		t.Fatalf("got %+v, want a full rerun at the style_anchor boundary", got)
	}
}

// The invalidation rate is a ratio, so a run claiming more invalidated segs
// than it has segs would push the headline number above 100%.
func TestRecordRunRejectsAnImpossibleInvalidationCount(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	for _, invalidated := range []int{-1, 6} {
		run := store.RecompileRun{ID: "run-1", ProjectID: "p1", TotalSegs: 5, InvalidatedSegs: invalidated}
		if err := s.RecordRun(ctx, run); err == nil {
			t.Fatalf("invalidated_segs %d was accepted", invalidated)
		}
	}

	runs, err := s.RecompileRuns(ctx, "p1", 10)
	if err != nil {
		t.Fatalf("RecompileRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("a rejected run was written anyway: %+v", runs)
	}
}

func TestRecordRunRejectsAnEmptyID(t *testing.T) {
	if err := openStore(t).RecordRun(context.Background(), store.RecompileRun{ProjectID: "p1"}); err == nil {
		t.Fatal("a run with no id was accepted")
	}
}

func TestRecompileRunsAreNewestFirstAndCapped(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	for i, id := range []string{"run-1", "run-2", "run-3"} {
		run := store.RecompileRun{
			ID: id, ProjectID: "p1", PlannedAt: time.UnixMilli(int64(i+1) * 1000),
			TotalSegs: 5, InvalidatedSegs: 1,
		}
		if err := s.RecordRun(ctx, run); err != nil {
			t.Fatalf("RecordRun %s: %v", id, err)
		}
	}

	got, err := s.RecompileRuns(ctx, "p1", 2)
	if err != nil {
		t.Fatalf("RecompileRuns: %v", err)
	}
	if len(got) != 2 || got[0].ID != "run-3" || got[1].ID != "run-2" {
		t.Fatalf("got %+v, want run-3 then run-2", got)
	}
}

// The daemon-wide report asks for every project at once, so an empty project
// id has to mean "all" rather than "none".
func TestRecompileRunsWithoutAProjectIDCoversEveryProject(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	for i, projectID := range []string{"p1", "p2"} {
		run := store.RecompileRun{
			ID: "run-" + projectID, ProjectID: projectID,
			PlannedAt: time.UnixMilli(int64(i+1) * 1000), TotalSegs: 5, InvalidatedSegs: 1,
		}
		if err := s.RecordRun(ctx, run); err != nil {
			t.Fatalf("RecordRun %s: %v", projectID, err)
		}
	}

	all, err := s.RecompileRuns(ctx, "", 10)
	if err != nil {
		t.Fatalf("RecompileRuns all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d runs, want one per project: %+v", len(all), all)
	}

	one, err := s.RecompileRuns(ctx, "p1", 10)
	if err != nil {
		t.Fatalf("RecompileRuns p1: %v", err)
	}
	if len(one) != 1 || one[0].ProjectID != "p1" {
		t.Fatalf("got %+v, want only p1's run", one)
	}
}

func TestRecompileRunsIsEmptyBeforeAnyRun(t *testing.T) {
	got, err := openStore(t).RecompileRuns(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("RecompileRuns: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want no runs", got)
	}
}
