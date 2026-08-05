package store

import (
	"context"
	"time"
)

// PolishRunRecord is the evidence of one script polish run.
type PolishRunRecord struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	StopReason string    `json:"stop_reason"`
	TokensUsed int64     `json:"tokens_used"`
	CostMicros int64     `json:"cost_micros"`
	Rounds     int       `json:"rounds"`
	CreatedAt  time.Time `json:"created_at"`
}

// ScriptStore persists polish runs.
type ScriptStore interface {
	PutPolishRun(ctx context.Context, r PolishRunRecord) error
	PolishRun(ctx context.Context, id string) (PolishRunRecord, error)
}
