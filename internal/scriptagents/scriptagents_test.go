package scriptagents

import (
	"context"
	"testing"

	"github.com/sequencestream/video-stream/internal/store"
)

func TestAudienceReportRejectsQualityJudgement(t *testing.T) {
	report := AudienceReport{DropOffs: []DropOff{{Second: 3, Reason: "hook is too boring"}}}
	if err := report.Validate(); err == nil {
		t.Fatal("expected judgement error")
	}
}

func TestAudienceReportAcceptsSecondAndReasonOnly(t *testing.T) {
	report := AudienceReport{DropOffs: []DropOff{{Second: 3, Reason: "promise unclear before label appears"}}}
	if err := report.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestCriticRejectsRewriteContent(t *testing.T) {
	fb := RejectingCritic()
	if err := fb.Validate(); err == nil {
		t.Fatal("expected rewrite rejection")
	}
}

func TestCriticAcceptsDiagnoseOnlyFeedback(t *testing.T) {
	fb := CriticFeedback{Issues: []CriticIssue{{
		SegID: "hook", Problem: "viewers leave around 3s", Evidence: "promise unclear before payoff label",
	}}}
	if err := fb.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestTerminationMaxRounds(t *testing.T) {
	cfg := DefaultTermination()
	history := []RoundMetrics{
		{Round: 1, Score: 0.9, NewIssues: 0, SkillsPass: true},
		{Round: 2, Score: 0.91, NewIssues: 0, SkillsPass: true},
		{Round: 3, Score: 0.92, NewIssues: 0, SkillsPass: true},
	}
	stop, reason := ShouldStop(cfg, history)
	if !stop || reason != "max_rounds" {
		t.Fatalf("got stop=%v reason=%q", stop, reason)
	}
}

func TestTerminationStagnantLowIssues(t *testing.T) {
	cfg := DefaultTermination()
	history := []RoundMetrics{
		{Round: 1, Score: 0.90, NewIssues: 2, SkillsPass: true},
		{Round: 2, Score: 0.905, NewIssues: 1, SkillsPass: true},
	}
	stop, reason := ShouldStop(cfg, history)
	if !stop || reason != "stagnant_low_issues" {
		t.Fatalf("got stop=%v reason=%q", stop, reason)
	}
}

func TestWriterProducesThreeHeterogeneousDrafts(t *testing.T) {
	drafts, err := RuleWriter{}.Write(context.Background(), WriteRequest{Topic: "fitness"})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(drafts) != WriterCount {
		t.Fatalf("got %d drafts", len(drafts))
	}
	seen := map[Direction]struct{}{}
	for _, d := range drafts {
		seen[d.Direction] = struct{}{}
		if err := d.Validate(); err != nil {
			t.Fatalf("draft %s: %v", d.ID, err)
		}
	}
	if len(seen) != WriterCount {
		t.Fatal("directions not heterogeneous")
	}
}

func TestPolishPreservesSpikeAndProducesValidProject(t *testing.T) {
	e := New(Options{Store: &memStore{}})
	spike := "nobody talks about this"
	result, err := e.Polish(context.Background(), PolishRequest{
		Topic: "productivity", Spike: spike, ProjectID: "proj-1",
	})
	if err != nil {
		t.Fatalf("polish: %v", err)
	}
	if result.TokensUsed <= 0 {
		t.Fatalf("tokens not recorded: %d", result.TokensUsed)
	}
	if err := result.Project.Validate(); err != nil {
		t.Fatalf("project validate: %v", err)
	}
	found := false
	for _, s := range result.Project.Segs {
		if spikeInText(s.Text, spike) {
			found = true
		}
	}
	if !found {
		t.Fatal("spike lost after polish")
	}
}

func TestPolishWithoutStoreFails(t *testing.T) {
	_, err := New(Options{}).Polish(context.Background(), PolishRequest{Topic: "x"})
	if err != ErrNoStore {
		t.Fatalf("got %v", err)
	}
}

func TestPolishWithStore(t *testing.T) {
	store := &memStore{}
	e := New(Options{Store: store})
	result, err := e.Polish(context.Background(), PolishRequest{
		Topic: "marketing", Spike: "nobody talks about this", ProjectID: "p1",
	})
	if err != nil {
		t.Fatalf("polish: %v", err)
	}
	if store.lastRunID != result.RunID {
		t.Fatalf("run not persisted")
	}
}

type memStore struct {
	lastRunID string
}

func (m *memStore) PutPolishRun(_ context.Context, r store.PolishRunRecord) error {
	m.lastRunID = r.ID
	return nil
}

func (m *memStore) PolishRun(context.Context, string) (store.PolishRunRecord, error) {
	return store.PolishRunRecord{}, nil
}
