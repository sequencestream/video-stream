package wizard_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
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

func opID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func advance(t *testing.T, ctx context.Context, eng *wizard.Engine, sess wizard.Session, n int, req wizard.AdvanceRequest) wizard.Session {
	t.Helper()
	req.OperationID, req.ExpectedVersion = opID(n), sess.Version
	next, err := eng.Advance(ctx, sess.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

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
		OperationID: opID(1),
		Topic:       "creator burnout", Category: "education",
		Accounts: []wizard.AccountInput{{Platform: "youtube", Handle: "@peer1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.State.TopicCards) < 3 {
		t.Fatalf("topics = %d, want >= 3", len(sess.State.TopicCards))
	}

	sess = advance(t, ctx, eng, sess, 2, wizard.AdvanceRequest{TopicCardID: sess.State.TopicCards[0].ID})
	if len(sess.State.HookDrafts) != 3 {
		t.Fatalf("step hook options: drafts=%d", len(sess.State.HookDrafts))
	}

	start := time.Now()
	sess = advance(t, ctx, eng, sess, 3, wizard.AdvanceRequest{DraftID: sess.State.HookDrafts[0].ID})
	if time.Since(start) > 30*time.Second {
		t.Fatalf("hook confirm took %v, want <= 30s", time.Since(start))
	}

	sess = advance(t, ctx, eng, sess, 4, wizard.AdvanceRequest{})
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
	sess = advance(t, ctx, eng, sess, 5, wizard.AdvanceRequest{})
	sess = advance(t, ctx, eng, sess, 6, wizard.AdvanceRequest{
		EditSegID: "hook", EditText: "Edited hook line for preview",
	})
	if sess.State.InvalidatedSegs == 0 {
		t.Fatalf("expected some recompile invalidation after edit, got 0/%d", sess.State.TotalSegs)
	}

	sess = advance(t, ctx, eng, sess, 7, wizard.AdvanceRequest{})
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

	sess, err := eng.Create(ctx, wizard.CreateRequest{OperationID: opID(20), Topic: "t", Category: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.State.TopicCards) == 0 {
		t.Fatal("expected topic cards")
	}
	sess = advance(t, ctx, eng, sess, 21, wizard.AdvanceRequest{TopicCardID: sess.State.TopicCards[0].ID})
	sess = advance(t, ctx, eng, sess, 22, wizard.AdvanceRequest{DraftID: sess.State.HookDrafts[0].ID})
	sess = advance(t, ctx, eng, sess, 23, wizard.AdvanceRequest{})
	sess = advance(t, ctx, eng, sess, 24, wizard.AdvanceRequest{})
	_, err = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{OperationID: opID(25), ExpectedVersion: sess.Version})
	if err == nil {
		t.Fatal("expected failure without render engine at preview step")
	}
	failed, _ := eng.Get(ctx, sess.ID)
	if failed.Status != "failed" || failed.FailedStep != wizard.StepPreview {
		t.Fatalf("failed session = %+v", failed)
	}
	out := filepath.Join(dir, "out")
	resumedEngine := wizard.New(wizard.Options{Store: db, Projects: db,
		Render: render.New(render.Options{Store: db, Artifacts: db, OutputDir: out})})
	resumed, err := resumedEngine.Advance(ctx, sess.ID, wizard.AdvanceRequest{
		OperationID: opID(26), ExpectedVersion: failed.Version, Resume: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != "active" || resumed.CurrentStep != wizard.StepDeliver {
		t.Fatalf("resumed = %+v", resumed)
	}
}

func TestDuplicateAdvanceReturnsOriginalResult(t *testing.T) {
	ctx := context.Background()
	eng, _ := openWizardEngine(t)
	sess, err := eng.Create(ctx, wizard.CreateRequest{OperationID: opID(30), Topic: "t", Category: "c"})
	if err != nil {
		t.Fatal(err)
	}
	req := wizard.AdvanceRequest{OperationID: opID(31), ExpectedVersion: sess.Version, TopicCardID: sess.State.TopicCards[0].ID}
	first, err := eng.Advance(ctx, sess.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := eng.Advance(ctx, sess.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != second.Version || first.CurrentStep != second.CurrentStep || len(second.State.HookDrafts) != 3 {
		t.Fatalf("duplicate changed result: first=%+v second=%+v", first, second)
	}
	got, _ := eng.Get(ctx, sess.ID)
	if got.Version != first.Version {
		t.Fatalf("stored version=%d want %d", got.Version, first.Version)
	}
}

func TestConcurrentDuplicateAdvanceExecutesOnce(t *testing.T) {
	ctx := context.Background()
	eng, _ := openWizardEngine(t)
	sess, err := eng.Create(ctx, wizard.CreateRequest{OperationID: opID(40), Topic: "t", Category: "c"})
	if err != nil {
		t.Fatal(err)
	}
	req := wizard.AdvanceRequest{OperationID: opID(41), ExpectedVersion: sess.Version, TopicCardID: sess.State.TopicCards[0].ID}
	var wg sync.WaitGroup
	results := make([]wizard.Session, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], errs[i] = eng.Advance(ctx, sess.ID, req) }(i)
	}
	wg.Wait()
	successes := 0
	for i, err := range errs {
		if err == nil {
			successes++
			continue
		}
		var reqErr *wizard.RequestError
		if !errors.As(err, &reqErr) || reqErr.Code != "operation_in_progress" {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if successes == 0 {
		t.Fatal("no concurrent request completed")
	}
	got, _ := eng.Get(ctx, sess.ID)
	if got.Version != sess.Version+1 {
		t.Fatalf("version=%d", got.Version)
	}
}

func TestStaleVersionReturnsLatestSession(t *testing.T) {
	ctx := context.Background()
	eng, _ := openWizardEngine(t)
	sess, err := eng.Create(ctx, wizard.CreateRequest{OperationID: opID(50), Topic: "t", Category: "c"})
	if err != nil {
		t.Fatal(err)
	}
	next := advance(t, ctx, eng, sess, 51, wizard.AdvanceRequest{TopicCardID: sess.State.TopicCards[0].ID})
	_, err = eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{OperationID: opID(52), ExpectedVersion: sess.Version, TopicCardID: sess.State.TopicCards[0].ID})
	var reqErr *wizard.RequestError
	if !errors.As(err, &reqErr) || reqErr.Code != "stale_session" || reqErr.Session == nil || reqErr.Session.Version != next.Version {
		t.Fatalf("stale error = %#v", err)
	}
}

func TestSessionSurvivesDatabaseAndEngineRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	db, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	eng := wizard.New(wizard.Options{Store: db, Projects: db})
	sess, err := eng.Create(ctx, wizard.CreateRequest{OperationID: opID(60), Topic: "restart", Category: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	restarted := wizard.New(wizard.Options{Store: db, Projects: db})
	got, err := restarted.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != sess.Version || got.State.TopicCards[0].ID != sess.State.TopicCards[0].ID {
		t.Fatalf("restored = %+v", got)
	}
	next := advance(t, ctx, restarted, got, 61, wizard.AdvanceRequest{TopicCardID: got.State.TopicCards[0].ID})
	if next.CurrentStep != wizard.StepHook {
		t.Fatalf("step=%d", next.CurrentStep)
	}
}

func TestRecoverInterruptedOperationThenResume(t *testing.T) {
	ctx := context.Background()
	eng, db := openWizardEngine(t)
	sess, err := eng.Create(ctx, wizard.CreateRequest{OperationID: opID(70), Topic: "t", Category: "c"})
	if err != nil {
		t.Fatal(err)
	}
	original := wizard.AdvanceRequest{OperationID: opID(71), ExpectedVersion: sess.Version, TopicCardID: sess.State.TopicCards[0].ID}
	raw, _ := json.Marshal(original)
	op := store.WizardOperationRecord{ID: original.OperationID, SessionID: sess.ID, Kind: "advance", Step: sess.CurrentStep,
		ExpectedVersion: sess.Version, RequestJSON: string(raw), RequestHash: "claimed"}
	if _, err := db.ClaimWizardOperation(ctx, sess.ID, sess.Version, false, op); err != nil {
		t.Fatal(err)
	}
	if n, err := eng.RecoverInterrupted(ctx); err != nil || n != 1 {
		t.Fatalf("recover n=%d err=%v", n, err)
	}
	failed, _ := eng.Get(ctx, sess.ID)
	if failed.Status != "failed" || failed.Version != sess.Version+1 {
		t.Fatalf("failed=%+v", failed)
	}
	resumed, err := eng.Advance(ctx, sess.ID, wizard.AdvanceRequest{OperationID: opID(72), ExpectedVersion: failed.Version, Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.CurrentStep != wizard.StepHook {
		t.Fatalf("resumed step=%d", resumed.CurrentStep)
	}
}
