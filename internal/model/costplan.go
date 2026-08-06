package model

import "time"

// DegradationDecision records one cost ladder step applied to fit budget.
type DegradationDecision struct {
	Level          int    `json:"level"`
	Action         string `json:"action"`
	Reason         string `json:"reason"`
	SavedMicros    int64  `json:"saved_micros"`
	FromCostMicros int64  `json:"from_cost_micros"`
	ToCostMicros   int64  `json:"to_cost_micros"`
}

// CostPlan is persisted on the project document after script finalization.
type CostPlan struct {
	EstimatedMicros int64                 `json:"estimated_micros"`
	ActualMicros    int64                 `json:"actual_micros,omitempty"`
	BudgetMicros    int64                 `json:"budget_micros"`
	DegradationLevel int                  `json:"degradation_level"`
	Decisions       []DegradationDecision `json:"decisions,omitempty"`
	WithinTolerance *bool                 `json:"within_tolerance,omitempty"`
	PlannedAt       time.Time             `json:"planned_at,omitempty"`
}
