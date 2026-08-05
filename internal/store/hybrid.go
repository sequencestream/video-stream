package store

import (
	"context"
	"time"
)

// HybridShotRecord is one seg's hybrid visual plan.
type HybridShotRecord struct {
	ProjectID    string    `json:"project_id"`
	SegID        string    `json:"seg_id"`
	Route        string    `json:"route"`
	Reason       string    `json:"reason"`
	StockQuery   string    `json:"stock_query,omitempty"`
	KenBurnsJSON string    `json:"ken_burns_json,omitempty"`
	StockJSON    string    `json:"stock_json,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// HybridStore persists hybrid shot plans.
type HybridStore interface {
	PutHybridPlan(ctx context.Context, projectID string, plans []HybridShotRecord) error
	HybridPlans(ctx context.Context, projectID string) ([]HybridShotRecord, error)
	AttachHybridStock(ctx context.Context, projectID, segID string, stockJSON string) error
}
