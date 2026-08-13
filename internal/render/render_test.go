package render_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/audio"
	"github.com/sequencestream/video-stream/internal/label"
	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/recompile"
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
	if opts.Video == nil {
		opts.Video = render.StubVideoGenerator{OutputDir: opts.OutputDir}
	}
	if opts.Validator == nil {
		opts.Validator = render.StubOutputValidator{}
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

func TestFinalizedBGMStagePersistsParametersAndRejectsTrackChange(t *testing.T) {
	dir := t.TempDir()
	bgmPath := filepath.Join(dir, "music.wav")
	if err := os.WriteFile(bgmPath, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := openEngine(t, render.Options{BGMMixer: render.StubBGMMixer{}})
	req := render.RunRequest{
		RunID: "run-bgm", Project: sampleProject(t), Resolution: render.Resolution720p,
		Finalized: true, IncludeBGM: true,
		BGM: render.BGMConfig{URI: bgmPath, BPM: 100, BeatOffsetMS: 75, GainDB: -20},
	}
	result, err := eng.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.CompletedStages[len(result.CompletedStages)-1]; got != render.StageBGMBeat {
		t.Fatalf("last stage=%q want %q", got, render.StageBGMBeat)
	}
	run, _, err := eng.GetRun(t.Context(), req.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !run.IncludeBGM || run.BGMURI != bgmPath || run.BGMBPM != 100 || run.BGMBeatOffsetMS != 75 || run.BGMGainDB != -20 {
		t.Fatalf("stored BGM=%+v", run)
	}
	req.BGM.BPM = 101
	if _, err := eng.Run(t.Context(), req); err == nil || !strings.Contains(err.Error(), "different render request") {
		t.Fatalf("track-changing resume err=%v", err)
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

func TestRunAcceptsAndReturnsRecompilePlan(t *testing.T) {
	project := sampleProject(t)
	eng := openEngine(t, render.Options{})
	if _, err := eng.Run(t.Context(), render.RunRequest{
		Project: project, Resolution: render.Resolution720p, RunID: "run-prime-plan-cache",
	}); err != nil {
		t.Fatal(err)
	}
	plan := recompile.Plan{
		ProjectID:       project.ID,
		Invalidated:     []string{"hook"},
		Reused:          []string{"body"},
		CostSavedMicros: 42,
	}
	result, err := eng.Run(t.Context(), render.RunRequest{
		Project: project, Resolution: render.Resolution720p, RunID: "run-recompile-plan",
		RecompilePlan: &plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RecompilePlan == nil || result.RecompilePlan.ProjectID != project.ID ||
		len(result.RecompilePlan.Invalidated) != 1 || result.RecompilePlan.Invalidated[0] != "hook" ||
		len(result.RecompilePlan.Reused) != 1 || result.RecompilePlan.Reused[0] != "body" {
		t.Fatalf("executor returned plan %+v, want %+v", result.RecompilePlan, plan)
	}
}

func TestRecompilePlanReusesArtifactsAndGeneratesOnlyInvalidatedSegs(t *testing.T) {
	outputDir := t.TempDir()
	videos := &render.CountingVideoGenerator{Inner: render.StubVideoGenerator{OutputDir: outputDir}}
	eng := openEngine(t, render.Options{OutputDir: outputDir, Video: videos})
	before := sampleProject(t)

	first, err := eng.Run(t.Context(), render.RunRequest{
		Project: before, Resolution: render.Resolution720p, RunID: "run-before-edit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if videos.Calls != len(before.Segs) {
		t.Fatalf("initial video generations=%d want %d", videos.Calls, len(before.Segs))
	}
	beforeArtifacts := artifactsBySeg(first.SegArtifacts)

	after := before
	after.Segs = append([]model.Seg(nil), before.Segs...)
	after.Segs[1].Text = "Survey now shows 81 percent"
	after.Seal()
	plan := recompile.Plan{
		ProjectID: after.ID,
		Reused:    []string{"hook"},
		Invalidated: []string{
			"body",
		},
	}
	second, err := eng.Run(t.Context(), render.RunRequest{
		Project: after, Resolution: render.Resolution720p, RunID: "run-after-edit",
		RecompilePlan: &plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if videos.Calls != len(before.Segs)+1 {
		t.Fatalf("video generations after edit=%d want %d (only invalidated body regenerated)", videos.Calls, len(before.Segs)+1)
	}
	afterArtifacts := artifactsBySeg(second.SegArtifacts)
	if afterArtifacts["hook"].URI != beforeArtifacts["hook"].URI {
		t.Fatalf("reused hook URI=%q want cached %q", afterArtifacts["hook"].URI, beforeArtifacts["hook"].URI)
	}
	if afterArtifacts["body"].RenderCacheKey == beforeArtifacts["body"].RenderCacheKey {
		t.Fatalf("invalidated body kept old cache key %q", afterArtifacts["body"].RenderCacheKey)
	}
	if len(second.SharedContext) != len(after.Segs) {
		t.Fatalf("shared context keys=%d want %d after edit", len(second.SharedContext), len(after.Segs))
	}
}

func TestRecompilePlanRejectsMissingReusableArtifact(t *testing.T) {
	project := sampleProject(t)
	plan := recompile.Plan{
		ProjectID:   project.ID,
		Invalidated: []string{"hook"},
		Reused:      []string{"body"},
	}
	_, err := openEngine(t, render.Options{}).Run(t.Context(), render.RunRequest{
		Project: project, Resolution: render.Resolution720p, RunID: "run-missing-reuse",
		RecompilePlan: &plan,
	})
	if !errors.Is(err, render.ErrReusableArtifactUnavailable) {
		t.Fatalf("got %v, want ErrReusableArtifactUnavailable", err)
	}
}

func artifactsBySeg(artifacts []store.RenderSegArtifact) map[string]store.RenderSegArtifact {
	result := make(map[string]store.RenderSegArtifact, len(artifacts))
	for _, artifact := range artifacts {
		result[artifact.SegID] = artifact
	}
	return result
}

func TestRunRejectsRecompilePlanThatDoesNotPartitionProject(t *testing.T) {
	project := sampleProject(t)
	plan := recompile.Plan{ProjectID: project.ID, Invalidated: []string{"hook"}}
	_, err := openEngine(t, render.Options{}).Run(t.Context(), render.RunRequest{
		Project: project, Resolution: render.Resolution720p, RecompilePlan: &plan,
	})
	if err == nil || !strings.Contains(err.Error(), "omits seg \"body\"") {
		t.Fatalf("got %v, want omitted-seg plan error", err)
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

func TestRunPersistsSubtitleDeliveryAndRejectsModeChange(t *testing.T) {
	ctx := context.Background()
	eng := openEngine(t, render.Options{})
	req := render.RunRequest{
		Project: sampleProject(t), Resolution: render.Resolution720p, RunID: "run-subtitles",
		Platform: "douyin", SubtitleMode: audio.SubtitleBurnIn,
	}
	if _, err := eng.Run(ctx, req); err != nil {
		t.Fatal(err)
	}
	run, _, err := eng.GetRun(ctx, req.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Platform != "douyin" || run.SubtitleMode != "burn_in" {
		t.Fatalf("stored subtitle delivery=%s/%s", run.Platform, run.SubtitleMode)
	}
	req.SubtitleMode = audio.SubtitleSoft
	if _, err := eng.Run(ctx, req); err == nil || !strings.Contains(err.Error(), "different render request") {
		t.Fatalf("mode-changing resume err=%v", err)
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

type rejectingOutputValidator struct{}

func (rejectingOutputValidator) Validate(context.Context, string, render.OutputSpec) error {
	return errors.New("audio stream is corrupt")
}

func TestRenderRejectsInvalidOutputBeforeDelivery(t *testing.T) {
	outputDir := t.TempDir()
	eng := openEngine(t, render.Options{OutputDir: outputDir, Validator: rejectingOutputValidator{}})
	project := sampleProject(t)
	runID := "run-output-fail"
	_, err := eng.Run(t.Context(), render.RunRequest{
		Project: project, Resolution: render.Resolution720p, RunID: runID,
	})
	if !errors.Is(err, render.ErrOutputRejected) {
		t.Fatalf("got %v, want ErrOutputRejected", err)
	}
	run, _, getErr := eng.GetRun(t.Context(), runID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if run.Status != "failed" || run.OutputURI != "" || !strings.Contains(run.Error, "audio stream is corrupt") {
		t.Fatalf("rejected run was exposed as deliverable: %+v", run)
	}
	outPath := filepath.Join(outputDir, project.ID, "720p.mp4")
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatalf("rejected output remains at %s: %v", outPath, statErr)
	}
}
