package store

import (
	"context"
	"time"
)

const (
	WizardOperationRunning     = "running"
	WizardOperationSucceeded   = "succeeded"
	WizardOperationRejected    = "rejected"
	WizardOperationFailed      = "failed"
	WizardOperationInterrupted = "interrupted"
)

// WizardSessionRecord persists one wizard run.
type WizardSessionRecord struct {
	ID                string    `json:"id"`
	CurrentStep       int       `json:"current_step"`
	Status            string    `json:"status"`
	Topic             string    `json:"topic"`
	Category          string    `json:"category"`
	ProjectID         string    `json:"project_id"`
	StateJSON         string    `json:"state_json"`
	CostMicros        int64     `json:"cost_micros"`
	FailedStep        int       `json:"failed_step"`
	Error             string    `json:"error"`
	HookConfirmMS     int64     `json:"hook_confirm_ms"`
	Version           int64     `json:"version"`
	ActiveOperationID string    `json:"active_operation_id,omitempty"`
	FailedOperationID string    `json:"failed_operation_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// WizardOperationRecord is the durable idempotency journal for a mutating
// wizard request. ResultJSON contains the exact successful Session response.
type WizardOperationRecord struct {
	ID              string    `json:"operation_id"`
	SessionID       string    `json:"session_id"`
	Kind            string    `json:"kind"`
	Step            int       `json:"step"`
	ExpectedVersion int64     `json:"expected_version"`
	RequestJSON     string    `json:"request_json"`
	RequestHash     string    `json:"request_hash"`
	Status          string    `json:"status"`
	ResultJSON      string    `json:"result_json,omitempty"`
	ErrorCode       string    `json:"error_code,omitempty"`
	Error           string    `json:"error,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// WizardStore persists wizard sessions.
type WizardStore interface {
	CreateWizardSession(ctx context.Context, rec WizardSessionRecord) error
	CreateWizardSessionWithOperation(ctx context.Context, rec WizardSessionRecord, op WizardOperationRecord) error
	UpdateWizardSession(ctx context.Context, rec WizardSessionRecord) error
	GetWizardSession(ctx context.Context, id string) (WizardSessionRecord, error)
	GetWizardOperation(ctx context.Context, id string) (WizardOperationRecord, error)
	PutWizardOperation(ctx context.Context, op WizardOperationRecord) error
	ClaimWizardOperation(ctx context.Context, sessionID string, expectedVersion int64, allowFailed bool, op WizardOperationRecord) (WizardSessionRecord, error)
	FinishWizardOperation(ctx context.Context, rec WizardSessionRecord, op WizardOperationRecord) error
	RecoverWizardOperations(ctx context.Context, reason string) (int, error)
}
