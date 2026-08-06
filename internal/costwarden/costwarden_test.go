package costwarden

import (
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/hybrid"
	"github.com/sequencestream/video-stream/internal/model"
)

func expensiveProject() model.Project {
	p := model.NewProject("cost-proj", "expensive", time.Now().UTC())
	p.Segs = []model.Seg{
		model.NewSeg("hook", "Stop scrolling this changes everything", 3000),
		model.NewSeg("b1", "Survey shows 73 percent quit within year", 5000),
		model.NewSeg("b2", "Experts warn the trend is accelerating fast", 5000),
		model.NewSeg("b3", "Here is what the data means for you today", 5000),
	}
	p.Segs[0].EmotionTag = model.EmotionUrgent
	p.Segs[0].ContinuityGroup = "hero"
	p.Seal()
	return p
}

func TestEstimateWithinBudgetForCheapProject(t *testing.T) {
	p := model.NewProject("cheap", "t", time.Now().UTC())
	p.Segs = []model.Seg{model.NewSeg("s1", "Hello world", 2000)}
	p.Seal()
	cat := NewCatalog()
	state := DefaultPlanState(cat)
	br, _, err := Estimate(EstimateInput{Project: p, State: state}, cat)
	if err != nil {
		t.Fatal(err)
	}
	if br.TotalMicros >= DefaultBudgetMicrosUSD {
		t.Fatalf("cheap project should be under $1: %d", br.TotalMicros)
	}
}

func TestLadderTriggersInOrder(t *testing.T) {
	cat := NewCatalog()
	cat.SetAvailable("openai-video-premium", false)
	eng := New(Options{Catalog: cat})
	p := expensiveProject()
	res, err := eng.Plan(t.Context(), PlanRequest{
		Project: p, BudgetMicros: 500_000, ScriptCostMicros: 80_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.EstimatedMicros > 500_000 {
		t.Fatalf("still over budget: %d", res.EstimatedMicros)
	}
	if res.DegradationLevel < int(LevelSwitchSupplier) {
		t.Fatalf("expected degradation, level=%d", res.DegradationLevel)
	}
	if len(res.Decisions) == 0 {
		t.Fatal("expected degradation decisions")
	}
}

func TestEachLadderLevelApplies(t *testing.T) {
	cat := NewCatalog()
	state := DefaultPlanState(cat)
	for _, step := range LadderActions() {
		s := state
		if !ApplyLevel(&s, step.Level, cat) && step.Level <= LevelDowngradeResolution {
			t.Fatalf("level %d should apply from default state", step.Level)
		}
	}
}

func TestFailoverUnavailableSupplier(t *testing.T) {
	cat := NewCatalog()
	primary, ok := cat.AIVideo(TierPremium, "openai")
	if !ok {
		t.Fatal("missing primary")
	}
	cat.SetAvailable(primary.ID, false)
	next, ok := cat.FailoverSameTier(primary)
	if !ok || next.Supplier != "dashscope" {
		t.Fatalf("failover = %+v ok=%v", next, ok)
	}
}

func TestWithinTolerance(t *testing.T) {
	if !WithinTolerance(1_000_000, 900_000) {
		t.Fatal("10% diff should pass")
	}
	if WithinTolerance(1_000_000, 800_000) {
		t.Fatal("20% diff should fail")
	}
}

func TestReconcileActual(t *testing.T) {
	eng := New(Options{})
	plan := model.CostPlan{EstimatedMicros: 900_000}
	got := eng.ReconcileActual(plan, 850_000)
	if got.ActualMicros != 850_000 || got.WithinTolerance == nil || !*got.WithinTolerance {
		t.Fatalf("reconcile = %+v", got)
	}
}

func TestPlanResultUsesMotionGraphicsFloor(t *testing.T) {
	cat := NewCatalog()
	state := DefaultPlanState(cat)
	for _, step := range LadderActions() {
		ApplyLevel(&state, step.Level, cat)
	}
	if state.ForceMinRoute != hybrid.RouteMotionGraphics {
		t.Fatalf("want motion graphics floor, got %q", state.ForceMinRoute)
	}
}
