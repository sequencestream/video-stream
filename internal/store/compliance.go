package store

import (
	"context"
	"time"
)

// ComplianceFingerprintRecord is one stored structure fingerprint for an account.
type ComplianceFingerprintRecord struct {
	ID              string    `json:"id"`
	AccountID       string    `json:"account_id"`
	StructureCardID string    `json:"structure_card_id"`
	ProjectID       string    `json:"project_id,omitempty"`
	Fingerprint     []float64 `json:"fingerprint"`
	CreatedAt       time.Time `json:"created_at"`
}

// ComplianceStore persists compliance audit data.
type ComplianceStore interface {
	PriorFingerprints(ctx context.Context, accountID string, limit int) ([][]float64, error)
	ReuseCount(ctx context.Context, accountID, structureCardID string, since time.Time) (int, error)
	RecordPass(ctx context.Context, r ComplianceFingerprintRecord) error
}
