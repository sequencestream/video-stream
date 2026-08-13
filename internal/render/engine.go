package render

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sequencestream/video-stream/internal/audio"
	"github.com/sequencestream/video-stream/internal/label"
	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/recompile"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

// RunRequest starts or resumes a render.
type RunRequest struct {
	RunID        string
	Project      model.Project
	Resolution   Resolution
	Finalized    bool
	IncludeBGM   bool
	BGM          BGMConfig
	ResumeFrom   string
	Platform     string
	SubtitleMode audio.SubtitleMode
	// RecompilePlan is the planner's exact decision for an edited preview.
	// Nil means this is a normal full render rather than an incremental edit.
	RecompilePlan *recompile.Plan
}

// RunResult is the outcome of a pipeline execution.
type RunResult struct {
	RunID           string                    `json:"run_id"`
	ProjectID       string                    `json:"project_id"`
	Resolution      Resolution                `json:"resolution"`
	OutputURI       string                    `json:"output_uri"`
	CompletedStages []string                  `json:"completed_stages"`
	SharedContext   []SharedVisual            `json:"shared_context"`
	SegArtifacts    []store.RenderSegArtifact `json:"seg_artifacts"`
	RecompilePlan   *recompile.Plan           `json:"recompile_plan,omitempty"`
}

// Options configures the Engine.
type Options struct {
	Store         store.RenderStore
	Artifacts     store.ArtifactStore
	OutputDir     string
	MediaDir      string
	FFmpegBinary  string
	FFprobeBinary string
	FFmpeg        FFmpeg
	Validator     OutputValidator
	Video         VideoGenerator
	Prompts       PromptGenerator
	Reporter      telemetry.Reporter
	Labels        label.Injector
	Audio         *audio.Engine
	BGMMixer      BGMMixer
	// StageHook is for tests: return an error to simulate a stage failure.
	StageHook func(stage string) error
}

// Engine runs the staged FFmpeg pipeline.
type Engine struct {
	store     store.RenderStore
	artifacts store.ArtifactStore
	outputDir string
	ffmpeg    FFmpeg
	validator OutputValidator
	video     VideoGenerator
	prompts   PromptGenerator
	reporter  telemetry.Reporter
	labels    label.Injector
	audio     *audio.Engine
	bgm       BGMMixer
	mediaDir  string
	stageHook func(stage string) error
}

// New builds an Engine with production-safe defaults. Tests without real media
// fixtures should explicitly inject StubFFmpeg, StubVideoGenerator, and
// StubOutputValidator.
func New(opts Options) *Engine {
	e := &Engine{
		store: opts.Store, artifacts: opts.Artifacts, outputDir: opts.OutputDir,
		ffmpeg: opts.FFmpeg, video: opts.Video, prompts: opts.Prompts,
		validator: opts.Validator,
		reporter:  opts.Reporter, stageHook: opts.StageHook, labels: opts.Labels,
		audio: opts.Audio, bgm: opts.BGMMixer, mediaDir: opts.MediaDir,
	}
	if e.labels == nil {
		e.labels = label.SidecarInjector{}
	}
	if e.ffmpeg == nil {
		e.ffmpeg = ExecFFmpeg{Binary: opts.FFmpegBinary}
	}
	if e.validator == nil {
		e.validator = ExecOutputValidator{
			FFprobeBinary: firstNonEmpty(opts.FFprobeBinary, defaultFFprobeBinary(opts.FFmpegBinary)),
			FFmpegBinary:  opts.FFmpegBinary,
		}
	}
	if e.video == nil {
		e.video = FFmpegVideoGenerator{Binary: opts.FFmpegBinary, OutputDir: opts.OutputDir, MediaDir: opts.MediaDir}
	}
	if e.prompts == nil {
		e.prompts = NopPromptGenerator{}
	}
	if e.bgm == nil {
		e.bgm = ExecBGMMixer{Binary: opts.FFmpegBinary}
	}
	if e.reporter == nil {
		e.reporter = telemetry.Nop()
	}
	return e
}

// Run executes the pipeline, optionally resuming from ResumeFrom.
func (e *Engine) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if e.store == nil {
		return RunResult{}, ErrNoStore
	}
	if err := req.Resolution.Validate(); err != nil {
		return RunResult{}, err
	}
	if req.RecompilePlan != nil {
		if err := req.RecompilePlan.ValidateFor(req.Project); err != nil {
			return RunResult{}, fmt.Errorf("invalid recompile plan: %w", err)
		}
	}
	if req.Platform == "" {
		req.Platform = "youtube"
	}
	if req.SubtitleMode == "" {
		if spec, ok := audio.SpecFor(req.Platform); ok {
			req.SubtitleMode = spec.PreferredMode
		} else {
			req.SubtitleMode = audio.SubtitleSoft
		}
	}
	if err := req.SubtitleMode.Validate(); err != nil {
		return RunResult{}, err
	}
	if req.IncludeBGM && !req.Finalized {
		return RunResult{}, ErrNotFinalized
	}
	if req.IncludeBGM {
		resolved, err := e.resolveBGM(req.Project.ID, req.BGM)
		if err != nil {
			return RunResult{}, err
		}
		req.BGM = resolved.withDefaults()
		if err := req.BGM.Validate(); err != nil {
			return RunResult{}, err
		}
	}
	if e.outputDir == "" {
		return RunResult{}, fmt.Errorf("render output dir is not configured")
	}
	if err := os.MkdirAll(e.outputDir, 0o755); err != nil {
		return RunResult{}, err
	}
	if req.RunID == "" {
		req.RunID = fmt.Sprintf("run-%d", time.Now().UTC().UnixNano())
	}

	stages := append([]string(nil), StageOrder...)
	if req.IncludeBGM {
		stages = append(stages, StageBGMBeat)
	}

	run, err := e.loadOrCreateRun(ctx, req)
	if err != nil {
		return RunResult{}, err
	}
	if run.Status == "completed" {
		shared, err := e.loadSharedContext(ctx, req.Project.ID)
		if err != nil {
			return RunResult{}, err
		}
		arts, err := e.store.RenderSegArtifacts(ctx, req.RunID)
		if err != nil {
			return RunResult{}, err
		}
		completed := append([]string(nil), StageOrder...)
		if req.IncludeBGM {
			completed = append(completed, StageBGMBeat)
		}
		return RunResult{RunID: run.ID, ProjectID: run.ProjectID, Resolution: req.Resolution,
			OutputURI: run.OutputURI, CompletedStages: completed, SharedContext: shared, SegArtifacts: arts,
			RecompilePlan: req.RecompilePlan}, nil
	}

	startIdx := 0
	if req.ResumeFrom != "" {
		startIdx, err = stageIndex(stages, req.ResumeFrom)
		if err != nil {
			return RunResult{}, err
		}
	} else if run.LastCompletedStage != "" && run.Status != "completed" {
		startIdx, err = stageIndex(stages, run.LastCompletedStage)
		if err != nil {
			return RunResult{}, err
		}
		startIdx++
	}

	shared, err := e.resolveSharedContext(ctx, req)
	if err != nil {
		return RunResult{}, err
	}

	stageFiles, err := e.priorStageFiles(ctx, req, stages, startIdx)
	if err != nil {
		return RunResult{}, err
	}
	completed := stages[:startIdx]

	for i := startIdx; i < len(stages); i++ {
		stage := stages[i]
		if stage == StageBGMBeat && !req.Finalized {
			return RunResult{}, ErrNotFinalized
		}
		if e.stageHook != nil {
			if err := e.stageHook(stage); err != nil {
				run.Status = "failed"
				run.Error = err.Error()
				run.LastCompletedStage = lastOf(completed)
				_ = e.store.UpdateRenderRun(ctx, run)
				return RunResult{}, fmt.Errorf("stage %s: %w", stage, err)
			}
		}

		files, err := e.runStage(ctx, req, stage, shared)
		if err != nil {
			run.Status = "failed"
			run.Error = err.Error()
			run.LastCompletedStage = lastOf(completed)
			_ = e.store.UpdateRenderRun(ctx, run)
			return RunResult{}, fmt.Errorf("stage %s: %w", stage, err)
		}
		stageFiles = append(stageFiles, files...)
		completed = append(completed, stage)

		run.LastCompletedStage = stage
		run.Status = "running"
		if err := e.store.UpdateRenderRun(ctx, run); err != nil {
			return RunResult{}, err
		}
	}

	outPath := filepath.Join(e.outputDir, req.Project.ID, string(req.Resolution)+".mp4")
	width, height := req.Resolution.Dimensions()
	durations := make([]time.Duration, len(req.Project.Segs))
	for i, seg := range req.Project.Segs {
		durations[i] = time.Duration(seg.DurationBudget.TargetMS()) * time.Millisecond
	}
	transition := 250 * time.Millisecond
	for _, duration := range durations {
		if duration <= transition {
			transition = 0
			break
		}
	}
	if err := e.ffmpeg.Mux(ctx, outPath, stageFiles, MuxPlan{
		Width: width, Height: height, FPS: 30,
		ClipDurations: durations, TransitionDuration: transition,
		SubtitleMode: req.SubtitleMode,
	}); err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		_ = e.store.UpdateRenderRun(ctx, run)
		return RunResult{}, fmt.Errorf("mux: %w", err)
	}

	l := label.Build(req.Project.ID, req.RunID)
	if err := label.InjectAndVerify(e.labels, outPath, l); err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		_ = e.store.UpdateRenderRun(ctx, run)
		return RunResult{}, fmt.Errorf("%w: %v", ErrLabelRejected, err)
	}
	outputDuration := time.Duration(0)
	for _, duration := range durations {
		outputDuration += duration
	}
	if err := e.validator.Validate(ctx, outPath, OutputSpec{
		Container: "mp4", Width: width, Height: height,
		Duration: outputDuration, DurationTolerance: 250 * time.Millisecond,
		RequireAudio: true,
	}); err != nil {
		run.Status = "failed"
		run.Error = fmt.Sprintf("%s: %v", ErrOutputRejected, err)
		_ = e.store.UpdateRenderRun(ctx, run)
		if removeErr := os.Remove(outPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return RunResult{}, fmt.Errorf("%w: %v (remove rejected output: %v)", ErrOutputRejected, err, removeErr)
		}
		return RunResult{}, fmt.Errorf("%w: %v", ErrOutputRejected, err)
	}

	run.Status = "completed"
	run.OutputURI = outPath
	run.LastCompletedStage = stages[len(stages)-1]
	if err := e.store.UpdateRenderRun(ctx, run); err != nil {
		return RunResult{}, err
	}

	segArts, err := e.store.RenderSegArtifacts(ctx, req.RunID)
	if err != nil {
		return RunResult{}, err
	}

	_ = telemetry.Report(ctx, e.reporter, "render.completed", map[string]any{
		"run_id": req.RunID, "project_id": req.Project.ID,
		"resolution": req.Resolution, "stages": len(completed),
	})

	return RunResult{
		RunID: req.RunID, ProjectID: req.Project.ID, Resolution: req.Resolution,
		OutputURI: outPath, CompletedStages: completed,
		SharedContext: shared, SegArtifacts: segArts, RecompilePlan: req.RecompilePlan,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (e *Engine) resolveSharedContext(ctx context.Context, req RunRequest) ([]SharedVisual, error) {
	stored, err := e.loadSharedContext(ctx, req.Project.ID)
	if err != nil {
		return nil, err
	}
	base := BuildSharedContext(req.Project)
	storedByKey := make(map[string]SharedVisual, len(stored))
	for _, visual := range stored {
		storedByKey[visual.RenderCacheKey] = visual
	}

	resolved := make([]SharedVisual, len(base))
	missing := make([]SharedVisual, 0, len(base))
	missingIndexes := make([]int, 0, len(base))
	for i, visual := range base {
		if cached, ok := storedByKey[visual.RenderCacheKey]; ok {
			resolved[i] = cached
			continue
		}
		missing = append(missing, visual)
		missingIndexes = append(missingIndexes, i)
	}
	if len(missing) == 0 {
		return resolved, nil
	}
	if req.Resolution == Resolution1080p {
		return nil, ErrPreviewRequired
	}

	enriched, err := e.prompts.Enrich(ctx, req.Project, missing)
	if err != nil {
		return nil, err
	}
	if len(enriched) != len(missing) {
		return nil, fmt.Errorf("prompt generator returned %d shared contexts for %d missing keys", len(enriched), len(missing))
	}
	for i, visual := range enriched {
		resolved[missingIndexes[i]] = visual
	}
	if err := e.saveSharedContext(ctx, req.Project.ID, resolved); err != nil {
		return nil, err
	}
	_ = telemetry.Report(ctx, e.reporter, "render.llm_prompts", map[string]any{
		"project_id": req.Project.ID, "keys": len(missing),
	})
	return resolved, nil
}

func (e *Engine) priorStageFiles(ctx context.Context, req RunRequest, stages []string, startIdx int) ([]string, error) {
	if startIdx == 0 {
		return nil, nil
	}
	var files []string
	for _, stage := range stages[:startIdx] {
		switch stage {
		case StageVisuals:
			arts, err := e.store.RenderSegArtifacts(ctx, req.RunID)
			if err != nil {
				return nil, err
			}
			for _, a := range arts {
				if a.Stage == StageVisuals {
					files = append(files, a.URI)
				}
			}
		case StageAudio, StageLoudness:
			path := filepath.Join(e.outputDir, req.Project.ID, req.RunID, stage+".wav")
			files = append(files, path)
		case StageSubtitles:
			files = append(files, filepath.Join(e.outputDir, req.Project.ID, req.RunID, "subtitles.vtt"))
		case StageBGMBeat:
			path := filepath.Join(e.outputDir, req.Project.ID, req.RunID, "bgm_aligned.wav")
			files = append(files, path)
		}
	}
	return files, nil
}

func (e *Engine) runStage(ctx context.Context, req RunRequest, stage string, shared []SharedVisual) ([]string, error) {
	switch stage {
	case StageVisuals:
		return e.runVisuals(ctx, req, shared)
	case StageAudio, StageSubtitles, StageLoudness:
		return e.runAudioStage(ctx, req, stage)
	case StageBGMBeat:
		return e.runBGMStage(ctx, req)
	case StageMux:
		return nil, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownStage, stage)
	}
}

func (e *Engine) resolveBGM(projectID string, cfg BGMConfig) (BGMConfig, error) {
	if strings.TrimSpace(cfg.URI) != "" {
		cfg.URI = strings.TrimPrefix(cfg.URI, "file://")
		return cfg, nil
	}
	if strings.TrimSpace(e.mediaDir) == "" {
		return BGMConfig{}, errors.New("include_bgm requires bgm.uri or a configured media directory")
	}
	for _, ext := range []string{".wav", ".mp3", ".m4a", ".aac", ".flac", ".ogg", ".opus"} {
		path := filepath.Join(e.mediaDir, projectID, "bgm"+ext)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			cfg.URI = path
			return cfg, nil
		} else if err != nil && !os.IsNotExist(err) {
			return BGMConfig{}, err
		}
	}
	return BGMConfig{}, fmt.Errorf("no BGM found for project %s; set bgm.uri or add media/%s/bgm.*", projectID, projectID)
}

func (e *Engine) runBGMStage(ctx context.Context, req RunRequest) ([]string, error) {
	runDir := filepath.Join(e.outputDir, req.Project.ID, req.RunID)
	output := filepath.Join(runDir, "bgm_aligned.wav")
	var cuts []time.Duration
	total := time.Duration(0)
	for i, seg := range req.Project.Segs {
		total += time.Duration(seg.DurationBudget.TargetMS()) * time.Millisecond
		if i < len(req.Project.Segs)-1 {
			cuts = append(cuts, total)
		}
	}
	spec, ok := audio.SpecFor(req.Platform)
	if !ok {
		return nil, fmt.Errorf("unknown audio platform %q", req.Platform)
	}
	result, err := e.bgm.Mix(ctx, BGMMixPlan{
		SpeechPath: filepath.Join(runDir, StageLoudness+".wav"), OutputPath: output,
		Config: req.BGM, CutPoints: cuts, TotalDuration: total,
		TargetLUFS: spec.TargetLUFS, ToleranceLUFS: spec.LUFSTolerance,
	})
	if err != nil {
		return nil, err
	}
	_ = telemetry.Report(ctx, e.reporter, "render.bgm_mixed", map[string]any{
		"project_id": req.Project.ID, "run_id": req.RunID, "bpm": req.BGM.BPM,
		"beat_phase_ms": result.TimelineBeatPhase.Milliseconds(), "source_start_ms": result.SourceStart.Milliseconds(),
		"output_lufs": result.OutputLUFS,
	})
	return []string{output}, nil
}

func (e *Engine) runVisuals(ctx context.Context, req RunRequest, shared []SharedVisual) ([]string, error) {
	byKey := make(map[string]SharedVisual, len(shared))
	for _, v := range shared {
		byKey[v.RenderCacheKey] = v
	}
	reused := make(map[string]struct{})
	if req.RecompilePlan != nil {
		for _, segID := range req.RecompilePlan.Reused {
			reused[segID] = struct{}{}
		}
	}
	var files []string
	for _, s := range req.Project.Segs {
		key := s.RenderCacheKey
		if key == "" {
			key = model.ComputeRenderCacheKey(s, req.Project.RenderProfile)
		}
		var uri string
		if _, ok := reused[s.SegID]; ok {
			var err error
			uri, err = e.reusableArtifactURI(ctx, s.SegID, key)
			if err != nil {
				return nil, err
			}
		} else {
			vis, ok := byKey[key]
			if !ok {
				return nil, fmt.Errorf("missing shared context for %s", key)
			}
			var err error
			uri, err = e.video.Generate(ctx, VideoGenInput{
				Resolution: req.Resolution, ProjectID: req.Project.ID, SegID: s.SegID,
				Text: s.Text, DurationMS: s.DurationBudget.TargetMS(), RenderCacheKey: key,
				Prompt: vis.Prompt, Seed: vis.Seed, RefURI: vis.RefURI,
			})
			if err != nil {
				return nil, err
			}
		}
		files = append(files, uri)
		rec := store.RenderSegArtifact{
			RunID: req.RunID, ProjectID: req.Project.ID, SegID: s.SegID,
			RenderCacheKey: key, Stage: StageVisuals, URI: uri,
		}
		if err := e.store.PutRenderSegArtifact(ctx, rec); err != nil {
			return nil, err
		}
		if _, ok := reused[s.SegID]; !ok && e.artifacts != nil {
			if err := e.artifacts.PutArtifact(ctx, store.Artifact{
				RenderCacheKey: key,
				DurationMS:     s.DurationBudget.TargetMS(),
				URI:            uri,
			}); err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}

func (e *Engine) reusableArtifactURI(ctx context.Context, segID, key string) (string, error) {
	if e.artifacts == nil {
		return "", fmt.Errorf("%w for seg %s: artifact store is not configured", ErrReusableArtifactUnavailable, segID)
	}
	artifact, err := e.artifacts.Artifact(ctx, key)
	if err != nil {
		return "", fmt.Errorf("%w for seg %s (%s): %v", ErrReusableArtifactUnavailable, segID, key, err)
	}
	uri := strings.TrimSpace(artifact.URI)
	if uri == "" {
		return "", fmt.Errorf("%w for seg %s (%s): URI is empty", ErrReusableArtifactUnavailable, segID, key)
	}
	info, err := os.Stat(uri)
	if err != nil {
		return "", fmt.Errorf("%w for seg %s (%s): %v", ErrReusableArtifactUnavailable, segID, key, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w for seg %s (%s): URI is not a regular file", ErrReusableArtifactUnavailable, segID, key)
	}
	return uri, nil
}

func (e *Engine) loadOrCreateRun(ctx context.Context, req RunRequest) (store.RenderRunRecord, error) {
	if req.RunID != "" {
		existing, err := e.store.GetRenderRun(ctx, req.RunID)
		if err == nil {
			if existing.ProjectID != req.Project.ID || existing.Resolution != string(req.Resolution) || existing.Finalized != req.Finalized || existing.Platform != req.Platform || existing.SubtitleMode != string(req.SubtitleMode) ||
				existing.IncludeBGM != req.IncludeBGM || existing.BGMURI != req.BGM.URI || existing.BGMBPM != req.BGM.BPM || existing.BGMBeatOffsetMS != req.BGM.BeatOffsetMS || existing.BGMGainDB != req.BGM.GainDB {
				return store.RenderRunRecord{}, fmt.Errorf("run id %s belongs to a different render request", req.RunID)
			}
			return existing, nil
		}
		if !errors.Is(err, store.ErrRenderRunNotFound) {
			return store.RenderRunRecord{}, err
		}
	}
	run := store.RenderRunRecord{
		ID: req.RunID, ProjectID: req.Project.ID,
		Resolution: string(req.Resolution), Platform: req.Platform, SubtitleMode: string(req.SubtitleMode), Status: "pending",
		Finalized:  req.Finalized,
		IncludeBGM: req.IncludeBGM, BGMURI: req.BGM.URI, BGMBPM: req.BGM.BPM,
		BGMBeatOffsetMS: req.BGM.BeatOffsetMS, BGMGainDB: req.BGM.GainDB,
	}
	if err := e.store.CreateRenderRun(ctx, run); err != nil {
		return store.RenderRunRecord{}, err
	}
	return run, nil
}

func (e *Engine) saveSharedContext(ctx context.Context, projectID string, ctxs []SharedVisual) error {
	recs := make([]store.RenderSharedContextRecord, len(ctxs))
	for i, c := range ctxs {
		recs[i] = store.RenderSharedContextRecord{
			ProjectID: projectID, RenderCacheKey: c.RenderCacheKey,
			Prompt: c.Prompt, Seed: c.Seed, RefURI: c.RefURI,
		}
	}
	return e.store.PutRenderSharedContext(ctx, projectID, recs)
}

func (e *Engine) loadSharedContext(ctx context.Context, projectID string) ([]SharedVisual, error) {
	recs, err := e.store.RenderSharedContext(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]SharedVisual, len(recs))
	for i, r := range recs {
		out[i] = SharedVisual{
			RenderCacheKey: r.RenderCacheKey, Prompt: r.Prompt,
			Seed: r.Seed, RefURI: r.RefURI,
		}
	}
	return out, nil
}

// TraceSeg finds artifacts for a seg across a completed run.
func TraceSeg(artifacts []store.RenderSegArtifact, segID string) []store.RenderSegArtifact {
	var out []store.RenderSegArtifact
	for _, a := range artifacts {
		if a.SegID == segID {
			out = append(out, a)
		}
	}
	return out
}

func stageIndex(stages []string, name string) (int, error) {
	for i, s := range stages {
		if s == name {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%w: %s", ErrUnknownStage, name)
}

func lastOf(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[len(xs)-1]
}

// GetRun returns a render run and its seg artifacts.
func (e *Engine) GetRun(ctx context.Context, runID string) (store.RenderRunRecord, []store.RenderSegArtifact, error) {
	if e.store == nil {
		return store.RenderRunRecord{}, nil, ErrNoStore
	}
	run, err := e.store.GetRenderRun(ctx, runID)
	if errors.Is(err, store.ErrRenderRunNotFound) {
		return store.RenderRunRecord{}, nil, ErrRunNotFound
	}
	if err != nil {
		return store.RenderRunRecord{}, nil, err
	}
	arts, err := e.store.RenderSegArtifacts(ctx, runID)
	if err != nil {
		return store.RenderRunRecord{}, nil, err
	}
	return run, arts, nil
}

func (e *Engine) runAudioStage(ctx context.Context, req RunRequest, stage string) ([]string, error) {
	runDir := filepath.Join(e.outputDir, req.Project.ID, req.RunID)
	path := filepath.Join(runDir, stage+".wav")
	if e.audio == nil {
		if stage == StageSubtitles {
			path = filepath.Join(runDir, "subtitles.vtt")
			if err := writeStubFile(path, "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nsubtitle"); err != nil {
				return nil, err
			}
			return []string{path}, nil
		}
		if err := writeStubFile(path, stage); err != nil {
			return nil, err
		}
		return []string{path}, nil
	}

	mixPath := filepath.Join(runDir, "mix.wav")
	subtitlePath := filepath.Join(runDir, "subs.vtt")
	mixExists, err := regularFileExists(mixPath)
	if err != nil {
		return nil, err
	}
	subtitlesExist, err := regularFileExists(subtitlePath)
	if err != nil {
		return nil, err
	}
	if !mixExists || !subtitlesExist {
		subdir := filepath.Join(req.Project.ID, req.RunID)
		if _, err := e.audio.Synthesize(ctx, audio.SynthesizeRequest{
			Project: req.Project, Platform: req.Platform, Mode: req.SubtitleMode,
			OutputSubdir: subdir,
		}); err != nil {
			return nil, err
		}
	}

	switch stage {
	case StageAudio:
		if err := copyFile(mixPath, path); err != nil {
			return nil, err
		}
	case StageSubtitles:
		subOut := filepath.Join(runDir, "subtitles.vtt")
		if err := copyFile(subtitlePath, subOut); err != nil {
			return nil, err
		}
		path = subOut
	case StageLoudness:
		if err := copyFile(mixPath, path); err != nil {
			return nil, err
		}
	}
	return []string{path}, nil
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("audio artifact %s is not a regular file", path)
	}
	return true, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
