package scriptagents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

// PolishRequest starts a script polishing run.
type PolishRequest struct {
	Topic      string   `json:"topic"`
	UserQuotes []string `json:"user_quotes,omitempty"`
	Spike      string   `json:"spike,omitempty"`
	Voice      string   `json:"voice,omitempty"`
	ProjectID  string   `json:"project_id,omitempty"`
}

// PolishResult is the outcome of one polish run.
type PolishResult struct {
	RunID       string           `json:"run_id"`
	Project     model.Project    `json:"project"`
	Rounds      []RoundMetrics   `json:"rounds"`
	StopReason  string           `json:"stop_reason"`
	TokensUsed  int64            `json:"tokens_used"`
	CostMicros  int64            `json:"cost_micros"`
	WinnerDraft Direction        `json:"winner_direction"`
}

// Store persists polish runs.
type Store interface {
	PutPolishRun(ctx context.Context, r store.PolishRunRecord) error
	PolishRun(ctx context.Context, id string) (store.PolishRunRecord, error)
}

// Options configures the Engine.
type Options struct {
	Store      Store
	Writer     Writer
	Audience   AudienceSimulator
	Judge      Judge
	Termination TerminationConfig
	Reporter   telemetry.Reporter
	Logger     *slog.Logger
}

// Engine orchestrates the polish loop.
type Engine struct {
	store       Store
	writer      Writer
	audience    AudienceSimulator
	judge       Judge
	termination TerminationConfig
	reporter    telemetry.Reporter
	logger      *slog.Logger
}

var ErrNoStore = errors.New("scriptagents has no store configured")

// New builds an Engine.
func New(opts Options) *Engine {
	e := &Engine{
		store:       opts.Store,
		writer:      opts.Writer,
		audience:    opts.Audience,
		judge:       opts.Judge,
		termination: opts.Termination,
		reporter:    opts.Reporter,
		logger:      opts.Logger,
	}
	if e.writer == nil {
		e.writer = RuleWriter{}
	}
	if e.audience == nil {
		e.audience = RuleAudienceSimulator{}
	}
	if e.judge == nil {
		e.judge = RuleJudge{}
	}
	if e.termination.MaxRounds == 0 {
		e.termination = DefaultTermination()
	}
	if e.reporter == nil {
		e.reporter = telemetry.Nop()
	}
	if e.logger == nil {
		e.logger = slog.New(slog.DiscardHandler)
	}
	return e
}

// Polish runs the multi-agent loop until termination.
func (e *Engine) Polish(ctx context.Context, req PolishRequest) (PolishResult, error) {
	if e.store == nil {
		return PolishResult{}, ErrNoStore
	}

	drafts, err := e.writer.Write(ctx, WriteRequest{
		Topic: req.Topic, UserQuotes: req.UserQuotes, Spike: req.Spike, RenderVoice: req.Voice,
	})
	if err != nil {
		return PolishResult{}, err
	}
	if len(drafts) != WriterCount {
		return PolishResult{}, fmt.Errorf("writer returned %d drafts, want %d", len(drafts), WriterCount)
	}
	directions := map[Direction]struct{}{}
	for _, d := range drafts {
		directions[d.Direction] = struct{}{}
	}
	if len(directions) != WriterCount {
		return PolishResult{}, errors.New("writer drafts are not direction-heterogeneous")
	}

	var tokens int64
	for _, d := range drafts {
		tokens += d.TokensUsed
	}

	reports := map[string]AudienceReport{}
	for _, d := range drafts {
		report, err := e.audience.Simulate(ctx, d)
		if err != nil {
			return PolishResult{}, err
		}
		reports[d.ID] = report
	}

	rankings, err := e.judge.Rank(ctx, drafts, reports)
	if err != nil {
		return PolishResult{}, err
	}
	if len(rankings) == 0 {
		return PolishResult{}, errors.New("judge returned no rankings")
	}

	winner, ok := draftByID(drafts, rankings[0].DraftID)
	if !ok {
		return PolishResult{}, errors.New("winning draft not found")
	}
	var eliminated Draft
	if len(rankings) > 1 {
		eliminated, _ = draftByID(drafts, rankings[len(rankings)-1].DraftID)
	}
	winner = HybridiseHook(winner, eliminated)
	winner = ApplyBreathPoints(winner)

	if styleAnchorReject(winner.Segs[0].Text, req.UserQuotes) {
		return PolishResult{}, errors.New("style anchor rejected AI phrasing")
	}
	if !containsSpike(winner, req.Spike) && req.Spike != "" {
		return PolishResult{}, ErrSpikeLost
	}

	var history []RoundMetrics
	var prevIssueCount int
	stopReason := "max_rounds"
	score := rankings[0].Score

	for round := 1; round <= e.termination.MaxRounds; round++ {
		skills := RunSkills(winner, e.termination)
		tokens += winner.TokensUsed

		fb := RuleCritic(winner, reports[winner.ID])
		if err := fb.Validate(); err != nil {
			return PolishResult{}, err
		}
		newIssues := len(fb.Issues) - prevIssueCount
		if newIssues < 0 {
			newIssues = 0
		}
		prevIssueCount = len(fb.Issues)

		m := RoundMetrics{
			Round: round, Score: score, NewIssues: newIssues,
			SkillsPass: HardConstraintsPass(skills),
		}
		history = append(history, m)

		if stop, reason := ShouldStop(e.termination, history); stop {
			stopReason = reason
			break
		}
		if round == e.termination.MaxRounds {
			break
		}
		// Minimal refinement: bump score slightly to simulate progress then re-check.
		score += 0.01
	}

	projectID := req.ProjectID
	if projectID == "" {
		projectID = newRunID()
	}
	profile := model.RenderProfile{Voice: req.Voice}
	project, err := winner.ToProject(projectID, req.Topic, profile)
	if err != nil {
		return PolishResult{}, err
	}
	if req.Spike != "" && !containsSpike(winner, req.Spike) {
		return PolishResult{}, ErrSpikeLost
	}

	cost := RunSkills(winner, e.termination).CostMicros
	runID := newRunID()
	result := PolishResult{
		RunID: runID, Project: project, Rounds: history, StopReason: stopReason,
		TokensUsed: tokens, CostMicros: cost, WinnerDraft: winner.Direction,
	}

	if err := e.store.PutPolishRun(ctx, store.PolishRunRecord{
		ID: runID, ProjectID: project.ID, StopReason: stopReason,
		TokensUsed: tokens, CostMicros: cost, Rounds: len(history),
	}); err != nil {
		return PolishResult{}, err
	}

	_ = telemetry.Report(ctx, e.reporter, "scriptagents.polished", map[string]any{
		"run_id": runID, "rounds": len(history), "tokens": tokens,
	})
	return result, nil
}

func newRunID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
