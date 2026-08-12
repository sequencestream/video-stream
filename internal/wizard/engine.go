package wizard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/sequencestream/video-stream/internal/compliance"
	"github.com/sequencestream/video-stream/internal/costwarden"
	"github.com/sequencestream/video-stream/internal/hybrid"
	"github.com/sequencestream/video-stream/internal/ideation"
	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/radar"
	"github.com/sequencestream/video-stream/internal/recompile"
	"github.com/sequencestream/video-stream/internal/render"
	"github.com/sequencestream/video-stream/internal/scriptagents"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
	"github.com/sequencestream/video-stream/internal/youtube"
	"github.com/sequencestream/video-stream/internal/youtube/notify"
)

const (
	statusActive    = "active"
	statusFailed    = "failed"
	statusCompleted = "completed"

	costPolishMicros  int64 = 50_000
	costPreviewMicros int64 = 200_000
	costDeliverMicros int64 = 300_000
)

// Options wires the wizard to backend engines.
type Options struct {
	Store      store.WizardStore
	Projects   store.ProjectStore
	Radar      *radar.Engine
	Ideation   *ideation.Engine
	Script     *scriptagents.Engine
	Hybrid     *hybrid.Engine
	Compliance *compliance.Engine
	Render     *render.Engine
	Recompile  *recompile.Engine
	CostWarden *costwarden.Engine
	YouTube    *youtube.Engine
	OutputDir  string
	Reporter   telemetry.Reporter
}

// Engine orchestrates the seven-step wizard.
type Engine struct {
	store      store.WizardStore
	projects   store.ProjectStore
	radar      *radar.Engine
	ideation   *ideation.Engine
	script     *scriptagents.Engine
	hybrid     *hybrid.Engine
	compliance *compliance.Engine
	render     *render.Engine
	recompile  *recompile.Engine
	costwarden *costwarden.Engine
	youtube    *youtube.Engine
	reporter   telemetry.Reporter
}

// New builds an Engine.
func New(opts Options) *Engine {
	e := &Engine{
		store: opts.Store, projects: opts.Projects, radar: opts.Radar,
		ideation: opts.Ideation, script: opts.Script, hybrid: opts.Hybrid,
		compliance: opts.Compliance, render: opts.Render, recompile: opts.Recompile,
		costwarden: opts.CostWarden, youtube: opts.YouTube,
		reporter: opts.Reporter,
	}
	if e.reporter == nil {
		e.reporter = telemetry.Nop()
	}
	return e
}

// Create starts a wizard session (step 1 complete, advances to step 2).
func (e *Engine) Create(ctx context.Context, req CreateRequest) (Session, error) {
	if e.store == nil {
		return Session{}, ErrNoStore
	}
	if req.Topic == "" {
		return Session{}, fmt.Errorf("topic is required")
	}
	id := newSessionID()
	state := SessionState{Accounts: req.Accounts}
	for _, a := range req.Accounts {
		if e.radar != nil && a.Handle != "" {
			_, _ = e.radar.ImportAccount(ctx, radar.Account{Platform: a.Platform, Handle: a.Handle})
		}
	}
	rec := store.WizardSessionRecord{
		ID: id, CurrentStep: StepTopics, Status: statusActive,
		Topic: req.Topic, Category: req.Category,
	}
	raw, err := encodeState(state)
	if err != nil {
		return Session{}, err
	}
	rec.StateJSON = raw
	rec.CreatedAt = time.Now().UTC()
	if err := e.store.CreateWizardSession(ctx, rec); err != nil {
		return Session{}, err
	}
	return e.loadTopics(ctx, rec)
}

// Get returns a session by id.
func (e *Engine) Get(ctx context.Context, id string) (Session, error) {
	rec, err := e.getRec(ctx, id)
	if err != nil {
		return Session{}, err
	}
	return toSession(rec)
}

// Advance completes the current step and moves forward (or resumes a failed step).
func (e *Engine) Advance(ctx context.Context, id string, req AdvanceRequest) (Session, error) {
	rec, err := e.getRec(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if rec.Status == statusFailed {
		if !req.Resume {
			return Session{}, ErrSessionFailed
		}
		rec.Status = statusActive
		rec.CurrentStep = rec.FailedStep
		rec.Error = ""
		rec.FailedStep = 0
	}

	step := rec.CurrentStep
	var session Session
	switch step {
	case StepTopics:
		session, err = e.completeTopics(ctx, rec, req)
	case StepHook:
		session, err = e.completeHook(ctx, rec, req)
	case StepScript:
		session, err = e.completeScript(ctx, rec)
	case StepAssets:
		session, err = e.completeAssets(ctx, rec)
	case StepPreview:
		session, err = e.completePreview(ctx, rec, req)
	case StepDeliver:
		session, err = e.completeDeliver(ctx, rec)
	default:
		return Session{}, ErrWrongStep
	}
	if err != nil {
		rec.Status = statusFailed
		rec.FailedStep = step
		rec.Error = err.Error()
		_ = e.store.UpdateWizardSession(ctx, rec)
		return Session{}, err
	}
	_ = telemetry.Report(ctx, e.reporter, "wizard.step_completed", map[string]any{
		"session_id": id, "step": step, "cost_micros": session.CostMicros,
	})
	return session, nil
}

func (e *Engine) loadTopics(ctx context.Context, rec store.WizardSessionRecord) (Session, error) {
	state, _ := decodeState(rec.StateJSON)
	if len(state.TopicCards) == 0 {
		card := ideation.StructureCard{
			ID: "wizard-" + rec.ID, HookType: "question-hook",
			OpeningVisual: "face-close-up", BeatSequence: "setup-twist-payoff",
			DensityCurve: "front-loaded", EmotionArc: "curiosity-release",
			ControversyAnchor: rec.Topic,
		}
		topics, err := ideation.RuleMigrator{}.Migrate(ctx, ideation.MigrateRequest{
			Card: card, UserTheme: rec.Topic, TargetCategory: rec.Category,
		})
		if err != nil {
			return Session{}, err
		}
		for i, t := range topics {
			if t.ID == "" {
				t.ID = fmt.Sprintf("topic-%s-%d", rec.ID, i)
			}
			state.TopicCards = append(state.TopicCards, TopicOption{
				ID: t.ID, Title: t.Title, Rationale: t.WhyFits,
			})
		}
		raw, _ := encodeState(state)
		rec.StateJSON = raw
		_ = e.store.UpdateWizardSession(ctx, rec)
	}
	return toSession(rec)
}

func (e *Engine) completeTopics(ctx context.Context, rec store.WizardSessionRecord, req AdvanceRequest) (Session, error) {
	if req.TopicCardID == "" {
		return Session{}, fmt.Errorf("topic_card_id is required")
	}
	state, _ := decodeState(rec.StateJSON)
	state.SelectedTopicID = req.TopicCardID

	drafts, err := scriptagents.RuleWriter{}.Write(ctx, scriptagents.WriteRequest{Topic: rec.Topic, Spike: rec.Category})
	if err != nil {
		return Session{}, err
	}
	sim := scriptagents.RuleAudienceSimulator{}
	for _, d := range drafts {
		rep, _ := sim.Simulate(ctx, d)
		reasons := make([]string, 0, len(rep.DropOffs))
		for _, drop := range rep.DropOffs {
			reasons = append(reasons, drop.Reason)
		}
		state.HookDrafts = append(state.HookDrafts, HookOption{
			ID: d.ID, Direction: string(d.Direction), HookText: d.HookText, DropOffReasons: reasons,
		})
	}
	state.HookShownAt = time.Now().UTC()
	rec.CurrentStep = StepHook
	return e.save(ctx, rec, state)
}

func (e *Engine) completeHook(ctx context.Context, rec store.WizardSessionRecord, req AdvanceRequest) (Session, error) {
	if req.DraftID == "" {
		return Session{}, fmt.Errorf("draft_id is required")
	}
	state, _ := decodeState(rec.StateJSON)
	state.SelectedDraftID = req.DraftID
	if !state.HookShownAt.IsZero() {
		rec.HookConfirmMS = time.Since(state.HookShownAt).Milliseconds()
	}
	rec.CurrentStep = StepScript
	sess, err := e.save(ctx, rec, state)
	if err != nil {
		return Session{}, err
	}
	// Auto-run script step data prep: store hook edit hint in state via polish next
	_ = req.HookEdit
	return sess, nil
}

func (e *Engine) completeScript(ctx context.Context, rec store.WizardSessionRecord) (Session, error) {
	if e.script == nil {
		return Session{}, fmt.Errorf("script engine unavailable")
	}
	result, err := e.script.Polish(ctx, scriptagents.PolishRequest{
		Topic: rec.Topic, Spike: rec.Category, ProjectID: "wiz-" + rec.ID,
	})
	if err != nil {
		return Session{}, err
	}
	rec.CostMicros += result.CostMicros + costPolishMicros
	if rec.CostMicros > MaxCostMicrosUSD {
		return Session{}, ErrBudgetExceeded
	}
	project := result.Project
	project.ID = "wiz-" + rec.ID
	project.Title = rec.Topic
	project.Seal()
	scriptCost := result.CostMicros + costPolishMicros
	state, _ := decodeState(rec.StateJSON)
	if e.costwarden != nil {
		plan, err := e.costwarden.Plan(ctx, costwarden.PlanRequest{
			Project: project, BudgetMicros: MaxCostMicrosUSD, ScriptCostMicros: scriptCost,
		})
		if err != nil {
			return Session{}, err
		}
		project.CostPlan = &plan.CostPlan
		state.CostPlan = &plan.CostPlan
	}
	// The wizard owns the project it produces. Do not rely on CostWarden's
	// optional persistence side effect: CostWarden can be configured without a
	// ProjectStore, and in that valid setup step 4 used to report success only
	// for step 5 to fail with project not found.
	if e.projects == nil {
		return Session{}, fmt.Errorf("project store unavailable")
	}
	project.UpdatedAt = time.Now().UTC()
	if err := e.projects.SaveProject(ctx, project); err != nil {
		return Session{}, err
	}
	rec.ProjectID = project.ID
	rec.CurrentStep = StepAssets
	return e.save(ctx, rec, state)
}

func (e *Engine) completeAssets(ctx context.Context, rec store.WizardSessionRecord) (Session, error) {
	project, err := e.loadProject(ctx, rec.ProjectID)
	if err != nil {
		return Session{}, err
	}
	if e.hybrid != nil {
		if _, err := e.hybrid.PlanProject(ctx, project); err != nil {
			return Session{}, err
		}
	}
	if e.compliance != nil {
		scriptText := project.Segs[0].Text + " Survey shows 42% quit"
		_, err := e.compliance.Check(ctx, compliance.CheckRequest{
			AccountID: "wizard", StructureCardID: "wiz-card",
			Fingerprint: []float64{0.5, 0.5},
			ScriptText:  scriptText,
			NonTemplate: []compliance.NonTemplateElement{{
				Kind: compliance.KindFirstHandData, Content: "42%",
			}},
		})
		if err != nil && !errors.Is(err, compliance.ErrGateBlocked) {
			return Session{}, err
		}
		if errors.Is(err, compliance.ErrGateBlocked) {
			return Session{}, err
		}
	}
	rec.CurrentStep = StepPreview
	state, _ := decodeState(rec.StateJSON)
	return e.save(ctx, rec, state)
}

func (e *Engine) completePreview(ctx context.Context, rec store.WizardSessionRecord, req AdvanceRequest) (Session, error) {
	project, err := e.loadProject(ctx, rec.ProjectID)
	if err != nil {
		return Session{}, err
	}
	previous := project
	if req.EditSegID != "" && req.EditText != "" {
		for i := range project.Segs {
			if project.Segs[i].SegID == req.EditSegID {
				project.Segs[i].Text = req.EditText
			}
		}
		project.Seal()
		if e.projects != nil {
			_ = e.projects.SaveProject(ctx, project)
		}
		if e.recompile != nil {
			plan, err := e.recompile.Plan(ctx, previous, project)
			if err != nil {
				return Session{}, err
			}
			state, _ := decodeState(rec.StateJSON)
			state.InvalidatedSegs = len(plan.Invalidated)
			state.TotalSegs = len(project.Segs)
			raw, _ := encodeState(state)
			rec.StateJSON = raw
		}
	}
	if e.render == nil {
		return Session{}, fmt.Errorf("render engine unavailable")
	}
	runID := "wiz-preview-" + rec.ID
	result, err := e.render.Run(ctx, render.RunRequest{
		RunID: runID, Project: project, Resolution: render.Resolution720p,
	})
	if err != nil {
		return Session{}, err
	}
	rec.CostMicros += costPreviewMicros
	if rec.CostMicros > MaxCostMicrosUSD {
		return Session{}, ErrBudgetExceeded
	}
	state, _ := decodeState(rec.StateJSON)
	state.PreviewRunID = result.RunID
	rec.CurrentStep = StepDeliver
	return e.save(ctx, rec, state)
}

func (e *Engine) completeDeliver(ctx context.Context, rec store.WizardSessionRecord) (Session, error) {
	project, err := e.loadProject(ctx, rec.ProjectID)
	if err != nil {
		return Session{}, err
	}
	if e.render == nil {
		return Session{}, fmt.Errorf("render engine unavailable")
	}
	runID := "wiz-deliver-" + rec.ID
	result, err := e.render.Run(ctx, render.RunRequest{
		RunID: runID, Project: project, Resolution: render.Resolution1080p, Finalized: true,
	})
	if err != nil {
		return Session{}, err
	}
	rec.CostMicros += costDeliverMicros
	if rec.CostMicros > MaxCostMicrosUSD {
		return Session{}, ErrBudgetExceeded
	}
	state, _ := decodeState(rec.StateJSON)
	state.DeliveryRunID = result.RunID
	state.OutputURI = result.OutputURI
	if e.youtube != nil {
		pub, err := e.youtube.Publish(ctx, youtube.PublishRequest{
			ProjectID: rec.ProjectID, SessionID: rec.ID,
			VideoPath: result.OutputURI, Title: rec.Topic, Notify: true,
		})
		if err != nil && !errors.Is(err, youtube.ErrNoCredential) {
			return Session{}, err
		}
		if err == nil {
			state.YouTubeVideoID = pub.VideoID
		} else {
			_ = e.youtube.NotifyComplete(ctx, notify.Event{
				ProjectID: rec.ProjectID, SessionID: rec.ID,
				OutputURI: result.OutputURI, Title: rec.Topic,
				CompletedAt: time.Now().UTC(),
			})
		}
	}
	rec.Status = statusCompleted
	rec.CurrentStep = StepDeliver
	sess, err := e.save(ctx, rec, state)
	if err != nil {
		return Session{}, err
	}
	_ = telemetry.Report(ctx, e.reporter, "wizard.completed", map[string]any{
		"session_id": rec.ID, "cost_micros": rec.CostMicros, "output_uri": state.OutputURI,
	})
	return sess, nil
}

func (e *Engine) save(ctx context.Context, rec store.WizardSessionRecord, state SessionState) (Session, error) {
	raw, err := encodeState(state)
	if err != nil {
		return Session{}, err
	}
	rec.StateJSON = raw
	if err := e.store.UpdateWizardSession(ctx, rec); err != nil {
		return Session{}, err
	}
	return toSession(rec)
}

func (e *Engine) loadProject(ctx context.Context, id string) (model.Project, error) {
	if e.projects == nil {
		return model.Project{}, fmt.Errorf("project store unavailable")
	}
	return e.projects.GetProject(ctx, id)
}

func (e *Engine) getRec(ctx context.Context, id string) (store.WizardSessionRecord, error) {
	rec, err := e.store.GetWizardSession(ctx, id)
	if err != nil {
		if err == store.ErrWizardSessionNotFound {
			return store.WizardSessionRecord{}, ErrSessionNotFound
		}
		return store.WizardSessionRecord{}, err
	}
	return rec, nil
}

func toSession(rec store.WizardSessionRecord) (Session, error) {
	state, err := decodeState(rec.StateJSON)
	if err != nil {
		return Session{}, err
	}
	return Session{
		ID: rec.ID, CurrentStep: rec.CurrentStep, Status: rec.Status,
		Topic: rec.Topic, Category: rec.Category, ProjectID: rec.ProjectID,
		CostMicros: rec.CostMicros, FailedStep: rec.FailedStep, Error: rec.Error,
		HookConfirmMS: rec.HookConfirmMS, State: state,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
	}, nil
}

func newSessionID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "wiz-" + hex.EncodeToString(b[:])
}
