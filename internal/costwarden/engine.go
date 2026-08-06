package costwarden

import (
	"context"
	"time"

	"github.com/sequencestream/video-stream/internal/hybrid"
	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

// PlanRequest asks CostWarden to fit a project under budget.
type PlanRequest struct {
	Project          model.Project
	BudgetMicros     int64
	ScriptCostMicros int64
}

// PlanResult is the outcome of budget planning.
type PlanResult struct {
	ProjectID        string                  `json:"project_id"`
	BudgetMicros     int64                   `json:"budget_micros"`
	EstimatedMicros  int64                   `json:"estimated_micros"`
	DegradationLevel int                     `json:"degradation_level"`
	State            PlanState               `json:"state"`
	Breakdown        EstimateBreakdown       `json:"breakdown"`
	ShotPlans        []hybrid.ShotPlan       `json:"shot_plans"`
	Decisions        []model.DegradationDecision `json:"decisions"`
	CostPlan         model.CostPlan          `json:"cost_plan"`
}

// Options configures the Engine.
type Options struct {
	Catalog  *Catalog
	Projects store.ProjectStore
	Reporter telemetry.Reporter
}

// Engine runs script-stage cost planning.
type Engine struct {
	catalog  *Catalog
	projects store.ProjectStore
	reporter telemetry.Reporter
}

// New builds an Engine.
func New(opts Options) *Engine {
	cat := opts.Catalog
	if cat == nil {
		cat = NewCatalog()
	}
	r := opts.Reporter
	if r == nil {
		r = telemetry.Nop()
	}
	return &Engine{catalog: cat, projects: opts.Projects, reporter: r}
}

// Estimate returns a cost breakdown without applying degradation.
func (e *Engine) Estimate(ctx context.Context, req PlanRequest) (PlanResult, error) {
	if len(req.Project.Segs) == 0 {
		return PlanResult{}, ErrEmptyProject
	}
	budget := req.BudgetMicros
	if budget <= 0 {
		budget = DefaultBudgetMicrosUSD
	}
	state := DefaultPlanState(e.catalog)
	breakdown, plans, err := Estimate(EstimateInput{
		Project: req.Project, State: state, ScriptCostMicros: req.ScriptCostMicros,
	}, e.catalog)
	if err != nil {
		return PlanResult{}, err
	}
	return PlanResult{
		ProjectID: req.Project.ID, BudgetMicros: budget,
		EstimatedMicros: breakdown.TotalMicros, DegradationLevel: int(LevelOriginal),
		State: state, Breakdown: breakdown, ShotPlans: plans,
		CostPlan: model.CostPlan{
			EstimatedMicros: breakdown.TotalMicros, BudgetMicros: budget,
			DegradationLevel: int(LevelOriginal), PlannedAt: time.Now().UTC(),
		},
	}, nil
}

// Plan applies the degradation ladder until the estimate fits the budget.
func (e *Engine) Plan(ctx context.Context, req PlanRequest) (PlanResult, error) {
	res, err := e.Estimate(ctx, req)
	if err != nil {
		return PlanResult{}, err
	}
	if res.EstimatedMicros <= res.BudgetMicros {
		return e.persist(ctx, req.Project, res)
	}

	state := res.State
	var decisions []model.DegradationDecision
	for _, step := range LadderActions() {
		if res.EstimatedMicros <= res.BudgetMicros {
			break
		}
		before := res.EstimatedMicros
		if !ApplyLevel(&state, step.Level, e.catalog) {
			continue
		}
		breakdown, plans, err := Estimate(EstimateInput{
			Project: req.Project, State: state, ScriptCostMicros: req.ScriptCostMicros,
		}, e.catalog)
		if err != nil {
			return PlanResult{}, err
		}
		after := breakdown.TotalMicros
		decisions = append(decisions, model.DegradationDecision{
			Level: int(step.Level), Action: step.Action, Reason: step.Reason,
			SavedMicros: before - after, FromCostMicros: before, ToCostMicros: after,
		})
		res = PlanResult{
			ProjectID: req.Project.ID, BudgetMicros: res.BudgetMicros,
			EstimatedMicros: after, DegradationLevel: int(step.Level),
			State: state, Breakdown: breakdown, ShotPlans: plans, Decisions: decisions,
			CostPlan: model.CostPlan{
				EstimatedMicros: after, BudgetMicros: res.BudgetMicros,
				DegradationLevel: int(step.Level), Decisions: decisions,
				PlannedAt: time.Now().UTC(),
			},
		}
	}
	if res.EstimatedMicros > res.BudgetMicros {
		return PlanResult{}, ErrBudgetExceeded
	}
	return e.persist(ctx, req.Project, res)
}

// ReconcileActual compares a stored estimate with measured render spend.
func (e *Engine) ReconcileActual(plan model.CostPlan, actualMicros int64) model.CostPlan {
	plan.ActualMicros = actualMicros
	ok := WithinTolerance(plan.EstimatedMicros, actualMicros)
	plan.WithinTolerance = &ok
	return plan
}

func (e *Engine) persist(ctx context.Context, project model.Project, res PlanResult) (PlanResult, error) {
	project.CostPlan = &res.CostPlan
	project.UpdatedAt = time.Now().UTC()
	if e.projects != nil {
		if err := e.projects.SaveProject(ctx, project); err != nil {
			return PlanResult{}, err
		}
	}
	_ = telemetry.Report(ctx, e.reporter, "costwarden.planned", map[string]any{
		"project_id": project.ID, "estimated_micros": res.EstimatedMicros,
		"degradation_level": res.DegradationLevel,
	})
	return res, nil
}
