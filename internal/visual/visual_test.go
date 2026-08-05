package visual_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/recompile"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/visual"
)

func samplePack() visual.StylePack {
	return visual.StylePack{
		ID: "warm-doc", Name: "Warm Documentary", SchemaVersion: visual.SchemaVersion,
		Stack: visual.IdentityStack{
			StyleRefURI: "file://refs/warm.jpg",
			Palette:     []string{"#F5E6D3", "#2C1810", "#E07A5F"},
			LightingPreset:  "soft-window-key",
			CompositionRule: "rule-of-thirds subject-left",
			BrandElements:   []string{"logo-watermark"},
			SceneCards:      []string{"cozy-desk"},
		},
	}
}

func TestCoherencePassesForFiveShotsUnderOnePack(t *testing.T) {
	pack := samplePack()
	pack.Seal()
	report, err := visual.MeasureCoherence(pack, visual.ShotsFromPack(pack))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed {
		t.Fatalf("expected coherence pass, got %+v", report)
	}
}

func TestCoherenceFailsWhenCompositionDrifts(t *testing.T) {
	pack := samplePack()
	shots := visual.ShotsFromPack(pack)
	shots[4].CompositionRule = visual.MutateComposition(shots[4].CompositionRule)
	report, err := visual.MeasureCoherence(pack, shots)
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed {
		t.Fatal("expected coherence failure")
	}
}

func TestImportExportRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	engine := visual.New(visual.Options{Store: db})

	created, err := engine.Create(ctx, samplePack())
	if err != nil {
		t.Fatal(err)
	}
	data, err := engine.Export(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := engine.Import(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Stack.LightingPreset != created.Stack.LightingPreset {
		t.Fatalf("round trip mismatch")
	}
}

func TestApplyWarnsFullRerunOnPackSwitch(t *testing.T) {
	packA := samplePack()
	packA.ID = "a"
	packB := samplePack()
	packB.ID = "b"
	packB.Stack.LightingPreset = "hard-noir"
	packA.Seal()
	packB.Seal()

	p := model.NewProject("p1", "t", time.Now().UTC())
	p.Segs = []model.Seg{model.NewSeg("s1", "hello", 2000)}
	p.RenderProfile.StyleAnchor = packA.StyleAnchorID()
	p.Seal()

	engine := visual.New(visual.Options{})
	first := engine.Apply(context.Background(), packA, p)
	if first.FullRerunWarning != "" {
		t.Fatal("first apply should not warn")
	}
	second := engine.Apply(context.Background(), packB, first.Project)
	if !second.RequiresFullRerun || second.FullRerunWarning == "" {
		t.Fatalf("expected full rerun warning, got %+v", second)
	}
}

func TestPackSwitchTriggersRecompileFullRerun(t *testing.T) {
	previous := model.NewProject("p1", "t", time.Now().UTC())
	previous.Segs = []model.Seg{
		model.NewSeg("s1", "a", 2000), model.NewSeg("s2", "b", 2000),
		model.NewSeg("s3", "c", 2000), model.NewSeg("s4", "d", 2000),
		model.NewSeg("s5", "e", 2000),
	}
	previous.RenderProfile.StyleAnchor = "l2:warm-doc"
	previous.Seal()

	next := previous
	next.RenderProfile.StyleAnchor = "l2:noir-doc"
	next.Seal()

	cache := &memCache{artifacts: map[string]int64{}}
	for _, s := range previous.Segs {
		cache.artifacts[s.RenderCacheKey] = 1000
	}
	plan, err := visual.PlanPackSwitch(previous, next, cache)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.FullRerun || plan.Boundary != recompile.BoundaryStyleAnchor {
		t.Fatalf("got %+v, want full rerun at style_anchor", plan)
	}
}

type memCache struct {
	artifacts map[string]int64
}

func (m *memCache) Artifact(_ context.Context, key string) (store.Artifact, error) {
	d, ok := m.artifacts[key]
	if !ok {
		return store.Artifact{}, store.ErrArtifactNotFound
	}
	return store.Artifact{RenderCacheKey: key, DurationMS: d}, nil
}
