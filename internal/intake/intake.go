// Package intake turns a title and a block of narration prose into a sealed,
// renderable project.
//
// This is the step the pipeline was missing. Everything downstream — TTS,
// subtitles, the render cache, incremental recompilation — is defined over the
// seg graph, but nothing built one from the thing an author actually has: a
// script they wrote. Callers were left hand-writing seg JSON and hand-computing
// duration budgets, and a budget that disagrees with the synthesized audio is
// silently truncated narration rather than a failed render.
//
// The load-bearing decision is that budgets are measured, not guessed. Each
// line is synthesized once up front and its real duration becomes the centre of
// its budget interval, because the mux stage cuts video to the budget midpoint
// while the audio track carries whatever TTS produced. Guessing that number
// wrong is not a rounding error; it cuts words off the end of a sentence.
package intake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

// ErrEmptyScript means the narration held no speakable text.
var ErrEmptyScript = errors.New("script contains no narration")

// Prober measures how long a line of narration actually takes to speak.
//
// It is deliberately narrower than audio.TTS: intake needs one number per line
// and must not depend on word timings, mixing, or output paths.
type Prober interface {
	ProbeMS(ctx context.Context, text, voice string) (int64, error)
}

// Request is one script import.
type Request struct {
	// ProjectID is optional; a time-ordered id is derived from the title when
	// it is empty.
	ProjectID string `json:"project_id,omitempty"`
	Title     string `json:"title"`
	Script    string `json:"script"`
	Voice     string `json:"voice,omitempty"`
	MaxRunes  int    `json:"max_runes,omitempty"`
	MinRunes  int    `json:"min_runes,omitempty"`
}

// Line is one imported seg and the measurement its budget came from.
type Line struct {
	SegID    string `json:"seg_id"`
	Text     string `json:"text"`
	ProbedMS int64  `json:"probed_ms"`
}

// Result is the sealed project plus the measurements behind it.
type Result struct {
	Project  model.Project `json:"project"`
	Lines    []Line        `json:"lines"`
	TotalMS  int64         `json:"total_ms"`
	SegCount int           `json:"seg_count"`
}

// Options configures the Engine.
type Options struct {
	Projects store.ProjectStore
	Prober   Prober
	Voice    string
	Reporter telemetry.Reporter
	Now      func() time.Time
}

// Engine imports scripts into projects.
type Engine struct {
	projects store.ProjectStore
	prober   Prober
	voice    string
	reporter telemetry.Reporter
	now      func() time.Time
}

// New builds an Engine.
func New(opts Options) *Engine {
	e := &Engine{
		projects: opts.Projects, prober: opts.Prober,
		voice: opts.Voice, reporter: opts.Reporter, now: opts.Now,
	}
	if e.reporter == nil {
		e.reporter = telemetry.Nop()
	}
	if e.now == nil {
		e.now = time.Now
	}
	return e
}

// Import splits the script, measures every line, and persists the project.
func (e *Engine) Import(ctx context.Context, req Request) (Result, error) {
	if e.prober == nil {
		return Result{}, errors.New("intake has no duration prober configured")
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return Result{}, errors.New("title must not be empty")
	}

	lines := Split(req.Script, req.MaxRunes, req.MinRunes)
	if len(lines) == 0 {
		return Result{}, ErrEmptyScript
	}

	voice := strings.TrimSpace(req.Voice)
	if voice == "" {
		voice = e.voice
	}

	now := e.now().UTC()
	project := model.NewProject(projectID(req.ProjectID, title, now), title, now)
	project.RenderProfile = model.RenderProfile{Voice: voice}

	measured := make([]Line, 0, len(lines))
	var total int64
	for i, text := range lines {
		segID := fmt.Sprintf("s%d", i+1)
		ms, err := e.prober.ProbeMS(ctx, text, voice)
		if err != nil {
			return Result{}, fmt.Errorf("measure %s: %w", segID, err)
		}
		if ms < model.MinTargetMS {
			return Result{}, fmt.Errorf("measure %s: %dms is too short to budget", segID, ms)
		}
		seg := model.NewSeg(segID, text, ms)
		// Segs are left independent on purpose. Narration lines do not resolve
		// against each other, and a linear depends_on chain would make editing
		// the first sentence invalidate every seg after it — exactly the cost
		// incremental recompilation exists to avoid.
		project.Segs = append(project.Segs, seg)
		measured = append(measured, Line{SegID: segID, Text: text, ProbedMS: ms})
		total += ms
	}

	project.Seal()
	if err := project.Validate(); err != nil {
		return Result{}, err
	}

	if e.projects != nil {
		if err := e.projects.SaveProject(ctx, project); err != nil {
			return Result{}, err
		}
	}

	_ = telemetry.Report(ctx, e.reporter, "intake.imported", map[string]any{
		"project_id": project.ID, "seg_count": len(project.Segs), "total_ms": total,
	})

	return Result{Project: project, Lines: measured, TotalMS: total, SegCount: len(project.Segs)}, nil
}

// projectID derives a stable, sortable id from the title. Ids are ASCII so they
// stay usable as path segments in media/ and output/.
func projectID(explicit, title string, now time.Time) string {
	if id := strings.TrimSpace(explicit); id != "" {
		return id
	}
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteRune('-')
		}
		if b.Len() >= 24 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "project"
	}
	return fmt.Sprintf("%s-%s", slug, now.Format("20060102-150405"))
}
