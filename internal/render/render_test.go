package render_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/label"
	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/render"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

func sampleProject(t *testing.T) model.Project {
	t.Helper()
	p := model.NewProject("p-render", "demo", time.Now().UTC())
	p.Segs = []model.Seg{
		model.NewSeg("hook", "Stop scrolling", 3000),
		model.NewSeg("body", "Survey shows 73 percent", 5000),
	}
	p.Segs[0].VisualPromptSlot = "hero-shot"
	p.Seal()
	return p
}

func openEngine(t *testing.T, opts render.Options) *render.Engine {
	t.Helper()
	dir := t.TempDir()
	db, err := store.OpenSQLite(filepath.Join(dir, "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if opts.OutputDir == "" {
		opts.OutputDir = filepath.Join(dir, "out")
	}
	if opts.FFmpeg == nil {
		opts.FFmpeg = render.StubFFmpeg{}
	}
	opts.Store = db
	opts.Artifacts = db
	return render.New(opts)
}

func Test720And1080ShareSeedRef(t *testing.T) {
	ctx := context.Background()
	prompts := &render.CountingPromptGenerator{}
	eng := openEngine(t, render.Options{Prompts: prompts})
	project := sampleProject(t)

	prev, err := eng.Run(ctx, render.RunRequest{
		Project: project, Resolution: render.Resolution720p, RunID: "run-720",
	})
	if err != nil {
		t.Fatal(err)
	}
	deliver, err := eng.Run(ctx, render.RunRequest{
		Project: project, Resolution: render.Resolution1080p, RunID: "run-1080",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !render.SameSharedContext(prev.SharedContext, deliver.SharedContext) {
		t.Fatalf("context mismatch: prev=%+v deliver=%+v", prev.SharedContext, deliver.SharedContext)
	}
	if prompts.Calls != 1 {
		t.Fatalf("LLM prompt calls = %d, want 1 (720p only)", prompts.Calls)
	}
}

func Test1080pWithoutPreviewFails(t *testing.T) {
	eng := openEngine(t, render.Options{})
	_, err := eng.Run(context.Background(), render.RunRequest{
		Project: sampleProject(t), Resolution: render.Resolution1080p,
	})
	if !errors.Is(err, render.ErrPreviewRequired) {
		t.Fatalf("got %v, want ErrPreviewRequired", err)
	}
}

func TestBGMBeforeFinalizedErrors(t *testing.T) {
	eng := openEngine(t, render.Options{})
	_, err := eng.Run(context.Background(), render.RunRequest{
		Project: sampleProject(t), Resolution: render.Resolution720p,
		IncludeBGM: true, Finalized: false,
	})
	if !errors.Is(err, render.ErrNotFinalized) {
		t.Fatalf("got %v, want ErrNotFinalized", err)
	}
}

func TestStageResumeFromFailure(t *testing.T) {
	ctx := context.Background()
	failAudio := true
	eng := openEngine(t, render.Options{
		StageHook: func(stage string) error {
			if stage == render.StageAudio && failAudio {
				return errors.New("audio stage simulated failure")
			}
			return nil
		},
	})
	project := sampleProject(t)
	runID := "run-resume"
	_, err := eng.Run(ctx, render.RunRequest{
		Project: project, Resolution: render.Resolution720p, RunID: runID,
	})
	if err == nil {
		t.Fatal("expected failure at audio stage")
	}

	failAudio = false
	result, err := eng.Run(ctx, render.RunRequest{
		Project: project, Resolution: render.Resolution720p, RunID: runID,
		ResumeFrom: render.StageAudio,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.OutputURI == "" {
		t.Fatal("missing output")
	}
}

func TestArtifactTracesToSeg(t *testing.T) {
	ctx := context.Background()
	eng := openEngine(t, render.Options{})
	result, err := eng.Run(ctx, render.RunRequest{
		Project: sampleProject(t), Resolution: render.Resolution720p, RunID: "run-trace",
	})
	if err != nil {
		t.Fatal(err)
	}
	traced := render.TraceSeg(result.SegArtifacts, "hook")
	if len(traced) != 1 || traced[0].RenderCacheKey == "" {
		t.Fatalf("got %+v", traced)
	}
}

func TestCompletedRunIsReturnedWithoutExecutingStagesAgain(t *testing.T) {
	ctx := context.Background()
	stageCalls := 0
	eng := openEngine(t, render.Options{StageHook: func(string) error { stageCalls++; return nil }})
	req := render.RunRequest{Project: sampleProject(t), Resolution: render.Resolution720p, RunID: "run-idempotent"}
	first, err := eng.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	callsAfterFirst := stageCalls
	second, err := eng.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if stageCalls != callsAfterFirst {
		t.Fatalf("stage calls=%d want %d", stageCalls, callsAfterFirst)
	}
	if second.OutputURI != first.OutputURI {
		t.Fatalf("output=%q want %q", second.OutputURI, first.OutputURI)
	}
}

func Test720pPreviewCompletesQuickly(t *testing.T) {
	ctx := context.Background()
	eng := openEngine(t, render.Options{})
	start := time.Now()
	_, err := eng.Run(ctx, render.RunRequest{
		Project: sampleProject(t), Resolution: render.Resolution720p, RunID: "run-fast",
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Minute {
		t.Fatalf("preview took %v, want under 2m", elapsed)
	}
}

func TestTelemetryReportsLLMOn720Only(t *testing.T) {
	ctx := context.Background()
	reporter := telemetry.NewMemoryReporter()
	prompts := &render.CountingPromptGenerator{}
	eng := openEngine(t, render.Options{Prompts: prompts, Reporter: reporter})
	project := sampleProject(t)
	if _, err := eng.Run(ctx, render.RunRequest{Project: project, Resolution: render.Resolution720p, RunID: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, render.RunRequest{Project: project, Resolution: render.Resolution1080p, RunID: "b"}); err != nil {
		t.Fatal(err)
	}
	llmEvents := 0
	for _, ev := range reporter.Events() {
		if ev.Name == "render.llm_prompts" {
			llmEvents++
		}
	}
	if llmEvents != 1 {
		t.Fatalf("llm telemetry events = %d, want 1", llmEvents)
	}
}

type brokenLabelInjector struct{}

func (brokenLabelInjector) Inject(string, label.Label) error { return nil }

func (brokenLabelInjector) Readback(string) (label.Label, error) {
	return label.Label{}, label.ErrReadbackMismatch
}

func TestRenderRejectsBrokenLabelReadback(t *testing.T) {
	ctx := context.Background()
	eng := openEngine(t, render.Options{Labels: brokenLabelInjector{}})
	_, err := eng.Run(ctx, render.RunRequest{
		Project: sampleProject(t), Resolution: render.Resolution720p, RunID: "run-label-fail",
	})
	if err == nil || !errors.Is(err, render.ErrLabelRejected) {
		t.Fatalf("got %v, want ErrLabelRejected", err)
	}
}
