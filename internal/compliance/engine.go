package compliance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

// CheckRequest is the input to a compliance check before render.
type CheckRequest struct {
	AccountID        string               `json:"account_id"`
	StructureCardID  string               `json:"structure_card_id"`
	Fingerprint      []float64            `json:"fingerprint"`
	NonTemplate      []NonTemplateElement `json:"non_template_elements"`
	ScriptText       string               `json:"script_text"`
	ProjectID        string               `json:"project_id,omitempty"`
	RecordOnPass     bool                 `json:"record_on_pass"`
}

// Store persists fingerprints and reuse counts.
type Store interface {
	PriorFingerprints(ctx context.Context, accountID string, limit int) ([][]float64, error)
	ReuseCount(ctx context.Context, accountID, structureCardID string, since time.Time) (int, error)
	RecordPass(ctx context.Context, r store.ComplianceFingerprintRecord) error
}

// Options configures the Engine.
type Options struct {
	Store    Store
	Config   Config
	Reporter telemetry.Reporter
	Logger   *slog.Logger
}

// Engine runs all three gates in order. There is no bypass flag.
type Engine struct {
	store    Store
	config   Config
	reporter telemetry.Reporter
	logger   *slog.Logger
}

var ErrNoStore = errors.New("compliance has no store configured")

// New builds an Engine.
func New(opts Options) (*Engine, error) {
	cfg := opts.Config.Effective()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	e := &Engine{
		store:    opts.Store,
		config:   cfg,
		reporter: opts.Reporter,
		logger:   opts.Logger,
	}
	if e.reporter == nil {
		e.reporter = telemetry.Nop()
	}
	if e.logger == nil {
		e.logger = slog.New(slog.DiscardHandler)
	}
	return e, nil
}

// Check runs all three gates. All must pass.
func (e *Engine) Check(ctx context.Context, req CheckRequest) (CheckResult, error) {
	if e.store == nil {
		return CheckResult{}, ErrNoStore
	}
	if strings.TrimSpace(req.AccountID) == "" {
		return CheckResult{}, errors.New("account_id is required")
	}
	if strings.TrimSpace(req.StructureCardID) == "" {
		return CheckResult{}, errors.New("structure_card_id is required")
	}

	since := time.Now().UTC().AddDate(0, 0, -e.config.ReuseWindowDays)
	prior, err := e.store.PriorFingerprints(ctx, req.AccountID, 100)
	if err != nil {
		return CheckResult{}, err
	}
	reuseCount, err := e.store.ReuseCount(ctx, req.AccountID, req.StructureCardID, since)
	if err != nil {
		return CheckResult{}, err
	}

	gates := []GateResult{
		CheckFingerprintGate(e.config, req.Fingerprint, prior),
		CheckReuseGate(e.config, reuseCount),
		CheckNonTemplateGate(req.NonTemplate, req.ScriptText),
	}

	passed := true
	for _, g := range gates {
		if !g.Passed {
			passed = false
		}
	}
	result := CheckResult{Passed: passed, Gates: gates}

	if passed && req.RecordOnPass {
		if err := e.store.RecordPass(ctx, store.ComplianceFingerprintRecord{
			AccountID: req.AccountID, StructureCardID: req.StructureCardID,
			ProjectID: req.ProjectID, Fingerprint: req.Fingerprint,
		}); err != nil {
			return CheckResult{}, err
		}
	}

	if !passed {
		_ = telemetry.Report(ctx, e.reporter, "compliance.blocked", map[string]any{
			"account_id": req.AccountID, "gates_failed": countFailed(gates),
		})
		return result, fmt.Errorf("%w: %s", ErrGateBlocked, firstFailure(gates))
	}

	_ = telemetry.Report(ctx, e.reporter, "compliance.passed", map[string]any{"account_id": req.AccountID})
	return result, nil
}

func countFailed(gates []GateResult) int {
	n := 0
	for _, g := range gates {
		if !g.Passed {
			n++
		}
	}
	return n
}

func firstFailure(gates []GateResult) string {
	for _, g := range gates {
		if !g.Passed {
			return g.Gate + ": " + g.Reason
		}
	}
	return ""
}
