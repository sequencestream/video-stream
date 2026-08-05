package hybrid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

// Store persists hybrid plans.
type Store interface {
	PutHybridPlan(ctx context.Context, projectID string, plans []store.HybridShotRecord) error
	HybridPlans(ctx context.Context, projectID string) ([]store.HybridShotRecord, error)
	AttachHybridStock(ctx context.Context, projectID, segID, stockJSON string) error
}

// Options configures the Engine.
type Options struct {
	Store    Store
	Config   Config
	Stock    []StockSource
	Reporter telemetry.Reporter
	Logger   *slog.Logger
}

// Engine plans and persists hybrid visual routes.
type Engine struct {
	store    Store
	config   Config
	stock    []StockSource
	reporter telemetry.Reporter
	logger   *slog.Logger
}

var ErrNoStore = errors.New("hybrid has no store configured")

// New builds an Engine.
func New(opts Options) *Engine {
	e := &Engine{
		store: opts.Store, config: opts.Config, stock: opts.Stock,
		reporter: opts.Reporter, logger: opts.Logger,
	}
	if e.config.MaxAIShots == 0 {
		e.config = DefaultConfig()
	}
	if e.reporter == nil {
		e.reporter = telemetry.Nop()
	}
	if e.logger == nil {
		e.logger = slog.New(slog.DiscardHandler)
	}
	return e
}

// PlanProject computes and persists shot routes.
func (e *Engine) PlanProject(ctx context.Context, project model.Project) ([]ShotPlan, error) {
	if e.store == nil {
		return nil, ErrNoStore
	}
	plans, err := Plan(project.Segs, e.config)
	if err != nil {
		return nil, err
	}
	records := make([]store.HybridShotRecord, 0, len(plans))
	for _, p := range plans {
		rec := store.HybridShotRecord{
			ProjectID: project.ID, SegID: p.SegID,
			Route: string(p.Route), Reason: p.Reason, StockQuery: p.StockQuery,
		}
		if p.KenBurns != nil {
			b, _ := json.Marshal(p.KenBurns)
			rec.KenBurnsJSON = string(b)
		}
		records = append(records, rec)
	}
	if err := e.store.PutHybridPlan(ctx, project.ID, records); err != nil {
		return nil, err
	}
	_ = telemetry.Report(ctx, e.reporter, "hybrid.planned", map[string]any{
		"project_id": project.ID, "ai_routes": CountAIRoutes(plans),
	})
	return plans, nil
}

// FetchStockForSeg resolves stock for a query.
func (e *Engine) FetchStockForSeg(ctx context.Context, query string) (StockAsset, error) {
	sources := e.stock
	if len(sources) == 0 {
		sources = []StockSource{FixtureStockSource{}}
	}
	return FetchStock(ctx, sources, query)
}

// Plans returns stored plans.
func (e *Engine) Plans(ctx context.Context, projectID string) ([]ShotPlan, error) {
	if e.store == nil {
		return nil, ErrNoStore
	}
	records, err := e.store.HybridPlans(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return plansFromRecords(records)
}

func plansFromRecords(records []store.HybridShotRecord) ([]ShotPlan, error) {
	out := make([]ShotPlan, 0, len(records))
	for _, r := range records {
		p := ShotPlan{SegID: r.SegID, Route: Route(r.Route), Reason: r.Reason, StockQuery: r.StockQuery}
		if r.KenBurnsJSON != "" {
			var kb KenBurnsParams
			if err := json.Unmarshal([]byte(r.KenBurnsJSON), &kb); err != nil {
				return nil, fmt.Errorf("decode ken burns for %s: %w", r.SegID, err)
			}
			p.KenBurns = &kb
		}
		out = append(out, p)
	}
	return out, nil
}
