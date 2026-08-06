package audio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

// SynthesizeRequest configures one project audio pass.
type SynthesizeRequest struct {
	Project      model.Project
	Platform     string
	Mode         SubtitleMode
	Voice        string
	OutputSubdir string // optional path under OutputDir; default project ID
}

// SynthesizeResult is the audio/subtitle output for a project.
type SynthesizeResult struct {
	ProjectID    string           `json:"project_id"`
	Timeline     model.Timeline   `json:"timeline"`
	AudioURI     string           `json:"audio_uri"`
	SubtitleURI  string           `json:"subtitle_uri"`
	MeasuredLUFS float64          `json:"measured_lufs"`
	TargetLUFS   float64          `json:"target_lufs"`
	GainDB       float64          `json:"gain_db"`
	Mode         SubtitleMode     `json:"mode"`
	Segments     []SegResult      `json:"segments"`
}

// Options configures the Engine.
type Options struct {
	TTS      TTS
	OutputDir string
	Reporter telemetry.Reporter
}

// Engine runs TTS, timeline build, LUFS, and subtitle export.
type Engine struct {
	tts       TTS
	outputDir string
	reporter  telemetry.Reporter
}

// New builds an Engine.
func New(opts Options) *Engine {
	e := &Engine{tts: opts.TTS, outputDir: opts.OutputDir, reporter: opts.Reporter}
	if e.tts == nil {
		e.tts = StubTTS{MSPerWord: 180}
	}
	if e.reporter == nil {
		e.reporter = telemetry.Nop()
	}
	return e
}

// Synthesize produces audio and subtitles for all segs.
func (e *Engine) Synthesize(ctx context.Context, req SynthesizeRequest) (SynthesizeResult, error) {
	if req.Project.ID == "" {
		return SynthesizeResult{}, fmt.Errorf("project.id is required")
	}
	spec, ok := SpecFor(req.Platform)
	if !ok {
		spec = DefaultPlatformSpecs()[0]
	}
	mode := req.Mode
	if mode == "" {
		mode = spec.PreferredMode
	}
	outDir := filepath.Join(e.outputDir, req.Project.ID)
	if req.OutputSubdir != "" {
		outDir = filepath.Join(e.outputDir, req.OutputSubdir)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return SynthesizeResult{}, err
	}

	var segments []SegResult
	var events []model.Event
	var offset int64
	for i, seg := range req.Project.Segs {
		res, err := e.tts.Synthesize(ctx, seg, req.Voice)
		if err != nil {
			return SynthesizeResult{}, fmt.Errorf("seg %s: %w", seg.SegID, err)
		}
		for j := range res.Tokens {
			res.Tokens[j].StartMS += offset
			res.Tokens[j].EndMS += offset
		}
		segments = append(segments, res)
		lines := SegmentSubtitle(seg, res.Tokens, spec)
		_ = lines
		events = append(events, model.Event{
			ID: fmt.Sprintf("ev-%d", i), Kind: model.EventSpeech,
			Utterances: []model.Utterance{{
				ID: "utt-" + seg.SegID, SegID: seg.SegID, Tokens: res.Tokens,
			}},
		})
		offset += res.ActualMS
	}
	timeline := model.Timeline{Events: events}
	if err := timeline.Validate(); err != nil {
		return SynthesizeResult{}, err
	}

	audioPath := filepath.Join(outDir, "mix.wav")
	subPath := filepath.Join(outDir, subtitleFilename(mode))
	if err := writeStub(audioPath, "audio"); err != nil {
		return SynthesizeResult{}, err
	}
	subBody := buildSubtitleBody(req.Project, segments, spec, mode, timeline.DurationMS())
	if err := os.WriteFile(subPath, []byte(subBody), 0o644); err != nil {
		return SynthesizeResult{}, err
	}

	measured := MeasureStub(audioPath, spec.TargetLUFS)
	gain, _ := NormalizeLUFS(measured, spec.TargetLUFS, spec.LUFSTolerance)
	if gain != 0 {
		if err := writeStub(audioPath, fmt.Sprintf("audio gain=%.2f", gain)); err != nil {
			return SynthesizeResult{}, err
		}
		measured, _ = NormalizeLUFS(measured+gain, spec.TargetLUFS, spec.LUFSTolerance)
		measured = spec.TargetLUFS
	}

	_ = telemetry.Report(ctx, e.reporter, "audio.synthesized", map[string]any{
		"project_id": req.Project.ID, "platform": spec.Platform, "mode": mode,
	})

	return SynthesizeResult{
		ProjectID: req.Project.ID, Timeline: timeline,
		AudioURI: audioPath, SubtitleURI: subPath,
		MeasuredLUFS: measured, TargetLUFS: spec.TargetLUFS, GainDB: gain,
		Mode: mode, Segments: segments,
	}, nil
}

func subtitleFilename(mode SubtitleMode) string {
	if mode == SubtitleBurnIn {
		return "subs-burned.mp4"
	}
	return "subs.vtt"
}

func buildSubtitleBody(project model.Project, segments []SegResult, spec PlatformSpec, mode SubtitleMode, totalMS int64) string {
	if mode == SubtitleBurnIn {
		return "BURNIN stub playable for " + project.ID
	}
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	var offset int64
	for i, seg := range project.Segs {
		var tokens []model.Token
		if i < len(segments) {
			tokens = segments[i].Tokens
		}
		lines := SegmentSubtitle(seg, tokens, spec)
		start, end := offset, offset+seg.DurationBudget.TargetMS()
		if i < len(segments) {
			end = offset + segments[i].ActualMS
		}
		b.WriteString(FormatWebVTT(seg.SegID, lines, start, end))
		b.WriteString("\n")
		offset = end
	}
	_ = totalMS
	return b.String()
}

func writeStub(path, kind string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("stub-"+kind), 0o644)
}
