package hybrid_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/hybrid"
	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/store"
)

func TestGoldenFixturesMatchExpectedRoutes(t *testing.T) {
	for _, f := range hybrid.GoldenFixtures {
		if !hybrid.MatchFixture(f) {
			got := hybrid.DecideRoute(f.Input, hybrid.DefaultConfig(), f.AIBudget)
			t.Fatalf("%s: got route %q reason %q", f.Name, got.Route, got.Reason)
		}
	}
}

func TestDefaultPlanUsesOnlyOneAIShotFor60sProject(t *testing.T) {
	segs := []model.Seg{
		model.NewSeg("hook", "Stop scrolling now", 3000),
		model.NewSeg("b1", "Survey shows 73% data", 8000),
		model.NewSeg("b2", "Simple takeaway here", 8000),
		model.NewSeg("b3", "Another point", 8000),
		model.NewSeg("b4", "Final call to action", 8000),
	}
	segs[0].EmotionTag = model.EmotionUrgent
	segs[0].ContinuityGroup = "hero"
	plans, err := hybrid.Plan(segs, hybrid.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if hybrid.CountAIRoutes(plans) != 1 {
		t.Fatalf("got %d AI routes, want 1", hybrid.CountAIRoutes(plans))
	}
}

func TestKenBurnsReproducibleWithSameSeed(t *testing.T) {
	seed := hybrid.KenBurnsSeed("s1", "hello")
	a := hybrid.ComputeKenBurns(seed, 1920, 1080)
	b := hybrid.ComputeKenBurns(seed, 1920, 1080)
	if !a.Equal(b) {
		t.Fatalf("ken burns not reproducible: %+v vs %+v", a, b)
	}
}

func TestStockFetchRetriesThenSucceeds(t *testing.T) {
	src := &flakySource{fails: 2}
	asset, err := hybrid.FetchStock(context.Background(), []hybrid.StockSource{src}, "ocean")
	if err != nil {
		t.Fatal(err)
	}
	if asset.License == "" || asset.Attribution == "" {
		t.Fatalf("missing copyright info: %+v", asset)
	}
}

type flakySource struct {
	fails int
	calls int
}

func (f *flakySource) Name() string { return "flaky" }

func (f *flakySource) Fetch(context.Context, string) (hybrid.StockAsset, error) {
	f.calls++
	if f.calls <= f.fails {
		return hybrid.StockAsset{}, errors.New("transient")
	}
	return hybrid.StockAsset{Source: "flaky", License: "test", Attribution: "test"}, nil
}

func TestEnginePersistsPlans(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	engine := hybrid.New(hybrid.Options{Store: db})
	p := model.NewProject("p1", "t", time.Now().UTC())
	p.Segs = []model.Seg{
		model.NewSeg("hook", "Stop now", 3000),
		model.NewSeg("b1", "Data 50 percent", 5000),
	}
	p.Segs[0].ContinuityGroup = "h"
	p.Segs[0].EmotionTag = model.EmotionUrgent
	p.Seal()

	if _, err := engine.PlanProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	plans, err := engine.Plans(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 || plans[0].Reason == "" {
		t.Fatalf("got %+v", plans)
	}
}
