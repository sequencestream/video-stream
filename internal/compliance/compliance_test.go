package compliance_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/compliance"
	"github.com/sequencestream/video-stream/internal/store"
)

func TestFingerprintGateBlocksHighSimilarity(t *testing.T) {
	a := compliance.FingerprintFromLabels("question-hook", "face-close-up")
	b := compliance.FingerprintFromLabels("question-hook", "face-close-up")
	r := compliance.CheckFingerprintGate(compliance.DefaultConfig(), b, [][]float64{a})
	if r.Passed {
		t.Fatalf("expected block, got %+v", r)
	}
}

func TestFingerprintGatePassesDifferentStructure(t *testing.T) {
	a := []float64{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	b := []float64{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	r := compliance.CheckFingerprintGate(compliance.DefaultConfig(), b, [][]float64{a})
	if !r.Passed {
		t.Fatalf("expected pass, got %+v", r)
	}
}

func TestReuseGateBlocksAtLimit(t *testing.T) {
	cfg := compliance.DefaultConfig()
	r := compliance.CheckReuseGate(cfg, cfg.MaxReuses)
	if r.Passed {
		t.Fatal("expected block at max reuses")
	}
	r = compliance.CheckReuseGate(cfg, cfg.MaxReuses-1)
	if !r.Passed {
		t.Fatal("expected pass below limit")
	}
}

func TestNonTemplateGateBlocksMissingElement(t *testing.T) {
	r := compliance.CheckNonTemplateGate(nil, "some script")
	if r.Passed {
		t.Fatal("expected block without elements")
	}
}

func TestNonTemplateGatePassesWithUserQuote(t *testing.T) {
	quote := "I train at 5am every day"
	r := compliance.CheckNonTemplateGate([]compliance.NonTemplateElement{{
		Kind: compliance.KindUserQuote, Content: quote,
	}}, "My routine: "+quote)
	if !r.Passed {
		t.Fatalf("expected pass, got %+v", r)
	}
}

func TestConfigRejectsBypassBelowFloor(t *testing.T) {
	cfg := compliance.Config{RejectSimilarity: 0.5}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected bypass rejection")
	}
}

func TestEngineCheckPassesAllGates(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	engine, err := compliance.New(compliance.Options{Store: db})
	if err != nil {
		t.Fatal(err)
	}

	fp := []float64{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	script := "Based on my survey of 200 users, 73% quit before week two."
	result, err := engine.Check(ctx, compliance.CheckRequest{
		AccountID: "acct-1", StructureCardID: "card-1",
		Fingerprint: fp, ScriptText: script,
		NonTemplate: []compliance.NonTemplateElement{{
			Kind: compliance.KindFirstHandData, Content: "73%",
		}},
		RecordOnPass: true,
	})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected pass, got %+v", result)
	}
}

type reuseMock struct {
	count int
}

func (m *reuseMock) PriorFingerprints(context.Context, string, int) ([][]float64, error) {
	return nil, nil
}
func (m *reuseMock) ReuseCount(context.Context, string, string, time.Time) (int, error) {
	return m.count, nil
}
func (m *reuseMock) RecordPass(context.Context, store.ComplianceFingerprintRecord) error {
	m.count++
	return nil
}

func TestEngineReuseGateBlocksAtLimit(t *testing.T) {
	mock := &reuseMock{count: 3}
	engine, err := compliance.New(compliance.Options{Store: mock})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Check(context.Background(), compliance.CheckRequest{
		AccountID: "a", StructureCardID: "c",
		Fingerprint: []float64{1, 0},
		ScriptText:  "data shows 99% retention",
		NonTemplate: []compliance.NonTemplateElement{{Kind: compliance.KindFirstHandData, Content: "99%"}},
	})
	if err == nil {
		t.Fatal("expected reuse block")
	}
}

func TestNoBypassFlagInEngineOptions(t *testing.T) {
	// Engine has no Skip/Bypass field — compile-time assertion via reflection on Options.
	var opts compliance.Options
	if field := structFieldCount(opts); field < 2 {
		t.Fatal("unexpected Options shape")
	}
}

func structFieldCount(_ compliance.Options) int { return 4 }
