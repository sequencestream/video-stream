package store

import (
	"context"
	"time"
)

// WizardSessionRecord persists one wizard run.
type WizardSessionRecord struct {
	ID            string    `json:"id"`
	CurrentStep   int       `json:"current_step"`
	Status        string    `json:"status"`
	Topic         string    `json:"topic"`
	Category      string    `json:"category"`
	ProjectID     string    `json:"project_id"`
	StateJSON     string    `json:"state_json"`
	CostMicros    int64     `json:"cost_micros"`
	FailedStep    int       `json:"failed_step"`
	Error         string    `json:"error"`
	HookConfirmMS int64     `json:"hook_confirm_ms"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// WizardStore persists wizard sessions.
type WizardStore interface {
	CreateWizardSession(ctx context.Context, rec WizardSessionRecord) error
	UpdateWizardSession(ctx context.Context, rec WizardSessionRecord) error
	GetWizardSession(ctx context.Context, id string) (WizardSessionRecord, error)
}
