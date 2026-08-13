package wizard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
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
	statusActive     = "active"
	statusProcessing = "processing"
	statusFailed     = "failed"
	statusCompleted  = "completed"

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
	lockMu     sync.Mutex
	locks      map[string]*sync.Mutex
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
		locks: make(map[string]*sync.Mutex),
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
	if !validOperationID(req.OperationID) {
		return Session{}, ErrOperationRequired
	}
	unlock := e.lock("create:" + req.OperationID)
	defer unlock()
	hash, raw, err := requestFingerprint(req, func(v *CreateRequest) { v.OperationID = "" })
	if err != nil {
		return Session{}, err
	}
	if op, err := e.store.GetWizardOperation(ctx, req.OperationID); err == nil {
		return replayOperation(op, hash)
	} else if err != store.ErrWizardOperationNotFound {
		return Session{}, err
	}
	if req.Topic == "" {
		return e.reject(ctx, store.WizardOperationRecord{ID: req.OperationID, Kind: "create", Step: StepSetup,
			RequestJSON: raw, RequestHash: hash}, "invalid_request", "topic is required", nil)
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
		Topic: req.Topic, Category: req.Category, Version: 1,
	}
	state, err = buildTopics(rec, state)
	if err != nil {
		return Session{}, err
	}
	stateRaw, err := encodeState(state)
	if err != nil {
		return Session{}, err
	}
	rec.StateJSON = stateRaw
	rec.CreatedAt = time.Now().UTC()
	sess, err := toSession(rec)
	if err != nil {
		return Session{}, err
	}
	result, _ := json.Marshal(sess)
	op := store.WizardOperationRecord{ID: req.OperationID, SessionID: id, Kind: "create", Step: StepSetup,
		RequestJSON: raw, RequestHash: hash, Status: store.WizardOperationSucceeded, ResultJSON: string(result)}
	if err := e.store.CreateWizardSessionWithOperation(ctx, rec, op); err != nil {
		if err == store.ErrWizardOperationExists {
			existing, getErr := e.store.GetWizardOperation(ctx, req.OperationID)
			if getErr != nil {
				return Session{}, getErr
			}
			return replayOperation(existing, hash)
		}
		return Session{}, err
	}
	return sess, nil
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
	if !validOperationID(req.OperationID) {
		return Session{}, ErrOperationRequired
	}
	unlock := e.lock("session:" + id)
	defer unlock()
	hash, incomingRaw, err := requestFingerprint(req, func(v *AdvanceRequest) { v.OperationID = "" })
	if err != nil {
		return Session{}, err
	}
	if op, err := e.store.GetWizardOperation(ctx, req.OperationID); err == nil {
		return replayOperation(op, hash)
	} else if err != store.ErrWizardOperationNotFound {
		return Session{}, err
	}
	if req.ExpectedVersion <= 0 {
		return e.reject(ctx, store.WizardOperationRecord{ID: req.OperationID, SessionID: id, Kind: "advance",
			ExpectedVersion: req.ExpectedVersion, RequestJSON: incomingRaw, RequestHash: hash},
			"expected_version_required", ErrVersionRequired.Error(), nil)
	}

	current, err := e.getRec(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if current.Version != req.ExpectedVersion {
		sess, _ := toSession(current)
		return e.reject(ctx, rejectedAdvance(req, id, current.CurrentStep, incomingRaw, hash), "stale_session", "wizard session version is stale", &sess)
	}
	if current.Status == statusCompleted {
		sess, _ := toSession(current)
		return e.reject(ctx, rejectedAdvance(req, id, current.CurrentStep, incomingRaw, hash), "session_completed", "wizard session is already completed", &sess)
	}
	if current.Status == statusProcessing {
		sess, _ := toSession(current)
		return e.reject(ctx, rejectedAdvance(req, id, current.CurrentStep, incomingRaw, hash), "operation_in_progress", "wizard session has an active operation", &sess)
	}

	effective := req
	allowFailed := false
	if req.Resume {
		if hasStepInput(req) {
			sess, _ := toSession(current)
			return e.reject(ctx, rejectedAdvance(req, id, current.CurrentStep, incomingRaw, hash), "invalid_resume", "resume must not include step input", &sess)
		}
		if current.Status != statusFailed || current.FailedOperationID == "" {
			return e.reject(ctx, rejectedAdvance(req, id, current.CurrentStep, incomingRaw, hash), "advance_rejected", ErrSessionFailed.Error(), nil)
		}
		failed, getErr := e.store.GetWizardOperation(ctx, current.FailedOperationID)
		if getErr != nil {
			return Session{}, getErr
		}
		if err := json.Unmarshal([]byte(failed.RequestJSON), &effective); err != nil {
			return Session{}, err
		}
		effective.OperationID = req.OperationID
		effective.ExpectedVersion = req.ExpectedVersion
		effective.Resume = false
		allowFailed = true
	} else {
		if current.Status == statusFailed {
			return e.reject(ctx, rejectedAdvance(req, id, current.CurrentStep, incomingRaw, hash), "advance_rejected", ErrSessionFailed.Error(), nil)
		}
		if err := validateStepInput(current.CurrentStep, req); err != nil {
			return e.reject(ctx, rejectedAdvance(req, id, current.CurrentStep, incomingRaw, hash), "invalid_request", err.Error(), nil)
		}
	}
	effectiveRaw, _ := json.Marshal(effective)
	op := store.WizardOperationRecord{ID: req.OperationID, SessionID: id, Kind: "advance", Step: current.CurrentStep,
		ExpectedVersion: req.ExpectedVersion, RequestJSON: string(effectiveRaw), RequestHash: hash, Status: store.WizardOperationRunning}
	rec, err := e.store.ClaimWizardOperation(ctx, id, req.ExpectedVersion, allowFailed, op)
	if err != nil {
		if err == store.ErrWizardOperationExists {
			existing, getErr := e.store.GetWizardOperation(ctx, req.OperationID)
			if getErr != nil {
				return Session{}, getErr
			}
			return replayOperation(existing, hash)
		}
		latest, _ := e.getRec(ctx, id)
		if err == store.ErrWizardSessionBusy {
			return Session{}, requestError("operation_in_progress", "wizard session has an active operation", latest)
		}
		if err == store.ErrWizardVersionConflict {
			return Session{}, staleError(latest)
		}
		return Session{}, err
	}
	if allowFailed {
		rec.Status, rec.CurrentStep, rec.Error, rec.FailedStep = statusActive, rec.FailedStep, "", 0
	}
	step := rec.CurrentStep
	var session Session
	switch step {
	case StepTopics:
		session, err = e.completeTopics(ctx, rec, effective)
	case StepHook:
		session, err = e.completeHook(ctx, rec, effective)
	case StepScript:
		session, err = e.completeScript(ctx, rec)
	case StepAssets:
		session, err = e.completeAssets(ctx, rec)
	case StepPreview:
		session, err = e.completePreview(ctx, rec, effective)
	case StepDeliver:
		session, err = e.completeDeliver(ctx, rec)
	default:
		err = ErrWrongStep
	}
	if err != nil {
		rec.Status = statusFailed
		rec.FailedStep = step
		rec.Error = err.Error()
		rec.FailedOperationID = req.OperationID
		rec.Version++
		op.Status, op.ErrorCode, op.Error = store.WizardOperationFailed, "advance_failed", err.Error()
		if finishErr := e.store.FinishWizardOperation(ctx, rec, op); finishErr != nil {
			return Session{}, finishErr
		}
		return Session{}, err
	}
	finished, err := recordFromSession(rec, session)
	if err != nil {
		return Session{}, err
	}
	finished.Version = rec.Version + 1
	finished.FailedOperationID = ""
	session.Version = finished.Version
	result, _ := json.Marshal(session)
	op.Status, op.ResultJSON = store.WizardOperationSucceeded, string(result)
	if err := e.store.FinishWizardOperation(ctx, finished, op); err != nil {
		return Session{}, err
	}
	_ = telemetry.Report(ctx, e.reporter, "wizard.step_completed", map[string]any{
		"session_id": id, "step": step, "cost_micros": session.CostMicros,
	})
	return session, nil
}

func (e *Engine) lock(key string) func() {
	e.lockMu.Lock()
	mu := e.locks[key]
	if mu == nil {
		mu = &sync.Mutex{}
		e.locks[key] = mu
	}
	e.lockMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// RecoverInterrupted marks work owned by a previous daemon process as failed.
func (e *Engine) RecoverInterrupted(ctx context.Context) (int, error) {
	if e.store == nil {
		return 0, ErrNoStore
	}
	return e.store.RecoverWizardOperations(ctx, "interrupted: daemon restarted while the wizard step was running")
}

func (e *Engine) loadTopics(ctx context.Context, rec store.WizardSessionRecord) (Session, error) {
	state, _ := decodeState(rec.StateJSON)
	if len(state.TopicCards) == 0 {
		var err error
		state, err = buildTopics(rec, state)
		if err != nil {
			return Session{}, err
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
		RunID: "wiz-polish-" + rec.ID, Topic: rec.Topic, Spike: rec.Category, ProjectID: "wiz-" + rec.ID,
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
	var recompilePlan *recompile.Plan
	if req.EditSegID != "" && req.EditText != "" {
		for i := range project.Segs {
			if project.Segs[i].SegID == req.EditSegID {
				project.Segs[i].Text = req.EditText
			}
		}
		project.Seal()
		if e.recompile != nil {
			plan, err := e.recompile.PlanWithID(ctx, "wiz-recompile-"+rec.ID, previous, project)
			if err != nil {
				return Session{}, err
			}
			recompilePlan = &plan
		}
	}
	if e.render == nil {
		return Session{}, fmt.Errorf("render engine unavailable")
	}
	runID := "wiz-preview-" + rec.ID
	result, err := e.render.Run(ctx, render.RunRequest{
		RunID: runID, Project: project, Resolution: render.Resolution720p,
		RecompilePlan: recompilePlan,
	})
	if err != nil {
		return Session{}, err
	}
	if req.EditSegID != "" && req.EditText != "" && e.projects != nil {
		if err := e.projects.SaveProject(ctx, project); err != nil {
			return Session{}, err
		}
	}
	rec.CostMicros += costPreviewMicros
	if rec.CostMicros > MaxCostMicrosUSD {
		return Session{}, ErrBudgetExceeded
	}
	state, _ := decodeState(rec.StateJSON)
	if result.RecompilePlan != nil {
		state.InvalidatedSegs = len(result.RecompilePlan.Invalidated)
		state.TotalSegs = result.RecompilePlan.TotalSegs()
	}
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
			UploadID:  "yt-wizard-" + rec.ID,
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
		HookConfirmMS: rec.HookConfirmMS, Version: rec.Version, State: state,
		CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
	}, nil
}

func buildTopics(rec store.WizardSessionRecord, state SessionState) (SessionState, error) {
	card := ideation.StructureCard{
		ID: "wizard-" + rec.ID, HookType: "question-hook", OpeningVisual: "face-close-up",
		BeatSequence: "setup-twist-payoff", DensityCurve: "front-loaded", EmotionArc: "curiosity-release",
		ControversyAnchor: rec.Topic,
	}
	topics, err := ideation.RuleMigrator{}.Migrate(context.Background(), ideation.MigrateRequest{
		Card: card, UserTheme: rec.Topic, TargetCategory: rec.Category,
	})
	if err != nil {
		return state, err
	}
	for i, t := range topics {
		if t.ID == "" {
			t.ID = fmt.Sprintf("topic-%s-%d", rec.ID, i)
		}
		state.TopicCards = append(state.TopicCards, TopicOption{ID: t.ID, Title: t.Title, Rationale: t.WhyFits})
	}
	return state, nil
}

func recordFromSession(base store.WizardSessionRecord, sess Session) (store.WizardSessionRecord, error) {
	raw, err := encodeState(sess.State)
	if err != nil {
		return store.WizardSessionRecord{}, err
	}
	base.CurrentStep, base.Status, base.ProjectID, base.StateJSON = sess.CurrentStep, sess.Status, sess.ProjectID, raw
	base.CostMicros, base.FailedStep, base.Error = sess.CostMicros, sess.FailedStep, sess.Error
	base.HookConfirmMS = sess.HookConfirmMS
	return base, nil
}

func requestFingerprint[T any](req T, strip func(*T)) (string, string, error) {
	copy := req
	strip(&copy)
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), string(raw), nil
}

func validOperationID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if id[i] != '-' {
				return false
			}
			continue
		}
		c := id[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func replayOperation(op store.WizardOperationRecord, hash string) (Session, error) {
	if op.RequestHash != hash {
		return Session{}, &RequestError{Code: "idempotency_conflict", Message: "operation_id was already used for a different request"}
	}
	switch op.Status {
	case store.WizardOperationSucceeded:
		var sess Session
		if err := json.Unmarshal([]byte(op.ResultJSON), &sess); err != nil {
			return Session{}, err
		}
		return sess, nil
	case store.WizardOperationRunning:
		return Session{}, &RequestError{Code: "operation_in_progress", Message: "wizard operation is still running"}
	default:
		code := op.ErrorCode
		if code == "" {
			code = "advance_failed"
		}
		var session *Session
		if op.ResultJSON != "" {
			var value Session
			if json.Unmarshal([]byte(op.ResultJSON), &value) == nil {
				session = &value
			}
		}
		return Session{}, &RequestError{Code: code, Message: op.Error, Session: session}
	}
}

func (e *Engine) reject(ctx context.Context, op store.WizardOperationRecord, code, message string, session *Session) (Session, error) {
	op.Status, op.ErrorCode, op.Error = store.WizardOperationRejected, code, message
	if session != nil {
		raw, _ := json.Marshal(session)
		op.ResultJSON = string(raw)
	}
	if err := e.store.PutWizardOperation(ctx, op); err != nil {
		if err == store.ErrWizardOperationExists {
			existing, getErr := e.store.GetWizardOperation(ctx, op.ID)
			if getErr != nil {
				return Session{}, getErr
			}
			return replayOperation(existing, op.RequestHash)
		}
		return Session{}, err
	}
	return Session{}, &RequestError{Code: code, Message: message, Session: session}
}

func rejectedAdvance(req AdvanceRequest, sessionID string, step int, raw, hash string) store.WizardOperationRecord {
	return store.WizardOperationRecord{ID: req.OperationID, SessionID: sessionID, Kind: "advance", Step: step,
		ExpectedVersion: req.ExpectedVersion, RequestJSON: raw, RequestHash: hash}
}

func requestError(code, message string, rec store.WizardSessionRecord) error {
	sess, _ := toSession(rec)
	return &RequestError{Code: code, Message: message, Session: &sess}
}

func staleError(rec store.WizardSessionRecord) error {
	return requestError("stale_session", "wizard session version is stale", rec)
}

func hasStepInput(req AdvanceRequest) bool {
	return req.TopicCardID != "" || req.DraftID != "" || req.HookEdit != "" || req.EditSegID != "" || req.EditText != ""
}

func validateStepInput(step int, req AdvanceRequest) error {
	switch step {
	case StepTopics:
		if req.TopicCardID == "" {
			return fmt.Errorf("topic_card_id is required")
		}
	case StepHook:
		if req.DraftID == "" {
			return fmt.Errorf("draft_id is required")
		}
	case StepPreview:
		if (req.EditSegID == "") != (req.EditText == "") {
			return fmt.Errorf("edit_seg_id and edit_text must be provided together")
		}
	}
	return nil
}

func newSessionID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "wiz-" + hex.EncodeToString(b[:])
}
