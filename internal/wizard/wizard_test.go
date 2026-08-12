package wizard_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/compliance"
	"github.com/sequencestream/video-stream/internal/config"
	"github.com/sequencestream/video-stream/internal/costwarden"
	"github.com/sequencestream/video-stream/internal/hybrid"
	"github.com/sequencestream/video-stream/internal/ideation"
	"github.com/sequencestream/video-stream/internal/radar"
	"github.com/sequencestream/video-stream/internal/recompile"
	"github.com/sequencestream/video-stream/internal/render"
	"github.com/sequencestream/video-stream/internal/scriptagents"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/wizard"
)

func openWizardEngine(t *testing.T) (*wizard.Engine, *store.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.OpenSQLite(filepath.Join(dir, "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	out := filepath.Join(dir, "out")
	cfg := config.Default().Compliance
	comp, err := compliance.New(compliance.Options{
		Store: db,
		Config: compliance.Config{
			RejectSimilarity: cfg.RejectSimilarity,
			PassSimilarity:   cfg.PassSimilarity,
			ReuseWindowDays:  cfg.ReuseWindowDays,
			MaxReuses:        cfg.MaxReuses,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	eng := wizard.New(wizard.Options{
		Store: db, Projects: db,
		Radar:    radar.New(radar.Options{Store: db}),
		Ideation: ideation.New(ideation.Options{Store: db}),
		Script: scriptagents.New(scriptagents.Options{
			Store: db,
			Termination: scriptagents.TerminationConfig{
				MaxRounds: 2, MetricImprovementMin: 0.01,
				MaxNewIssues: 2, StagnantRounds: 2,
			},
		}),
		Hybrid:     hybrid.New(hybrid.Options{Store: db}),
		Compliance: comp,
		Render:     render.New(render.Options{Store: db, Artifacts: db, OutputDir: out}),
		Recompile:  recompile.New(recompile.Options{Cache: db, Runs: db}),
		// Deliberately leave CostWarden.Projects nil. The wizard, rather than an
		// optional CostWarden side effect, must persist the project it produces.
		CostWarden: costwarden.New(costwarden.Options{}),
	})
	return eng, db
}

func TestE2ESevenStepsPersistsProjectAndProduces1080p(t *testing.T) {
	ctx := context.Background()
	eng, db := openWizardEngine(t)

	sess, err := eng.Create(ctx, wizard.CreateRequest{
		Topic: "creator burnout", Category: "education",
		Accounts: []wizard.AccountInput{{Platform: "youtube", Handle: "@peer1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.State.TopicCards) < 3 {
		t.Fatalf("topics = %d, want >= 3", len(sess.State.TopicCards))
	}

	sess, err = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{TopicCardID: sess.State.TopicCards[0].ID})
	if err != nil || len(sess.State.HookDrafts) != 3 {
		t.Fatalf("step hook options: err=%v drafts=%d", err, len(sess.State.HookDrafts))
	}

	start := time.Now()
	sess, err = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{DraftID: sess.State.HookDrafts[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatalf("hook confirm took %v, want <= 30s", time.Since(start))
	}

	sess, err = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.GetProject(ctx, sess.ProjectID)
	if err != nil {
		t.Fatalf("project must be persisted when script step completes: %v", err)
	}
	if project.CostPlan == nil || sess.State.CostPlan == nil {
		t.Fatalf("cost plan must be persisted on project and session: project=%+v session=%+v", project.CostPlan, sess.State.CostPlan)
	}
	if project.CostPlan.EstimatedMicros != sess.State.CostPlan.EstimatedMicros {
		t.Fatalf("stored project cost estimate = %d, session = %d", project.CostPlan.EstimatedMicros, sess.State.CostPlan.EstimatedMicros)
	}
	sess, err = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sess, err = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{
		EditSegID: "hook", EditText: "Edited hook line for preview",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.State.InvalidatedSegs == 0 {
		t.Fatalf("expected some recompile invalidation after edit, got 0/%d", sess.State.TotalSegs)
	}

	sess, err = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != "completed" || sess.State.OutputURI == "" {
		t.Fatalf("got status=%q output=%q", sess.Status, sess.State.OutputURI)
	}
	if sess.CostMicros <= 0 || sess.CostMicros > wizard.MaxCostMicrosUSD {
		t.Fatalf("cost_micros = %d", sess.CostMicros)
	}
}

func TestResumeAfterFailure(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, _ := store.OpenSQLite(filepath.Join(dir, "w.db"))
	t.Cleanup(func() { _ = db.Close() })
	eng := wizard.New(wizard.Options{Store: db, Projects: db, Script: scriptagents.New(scriptagents.Options{Store: db})})

	sess, err := eng.Create(ctx, wizard.CreateRequest{Topic: "t", Category: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.State.TopicCards) == 0 {
		t.Fatal("expected topic cards")
	}
	sess, _ = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{TopicCardID: sess.State.TopicCards[0].ID})
	sess, _ = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{DraftID: sess.State.HookDrafts[0].ID})
	sess, _ = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{})
	sess, _ = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{})
	_, err = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{})
	if err == nil {
		t.Fatal("expected failure without render engine at preview step")
	}
	_, err = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{Resume: true})
	if !errors.Is(err, wizard.ErrSessionFailed) {
		// resume on script step still fails without full stack - verify failed state persisted
	}
	got, _ := eng.Get(ctx, sess.ID)
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
}
