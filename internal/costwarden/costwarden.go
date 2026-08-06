// Package costwarden estimates per-video spend at script finalization and applies
// a seven-level degradation ladder before render when the budget is exceeded.
package costwarden

import "errors"

const (
	// DefaultBudgetMicrosUSD is the MVP $1.00 per-video cap.
	DefaultBudgetMicrosUSD int64 = 1_000_000
	// EstimateTolerancePercent is the allowed deviation between estimate and actual.
	EstimateTolerancePercent = 15
)

var (
	ErrNoStore         = errors.New("costwarden has no store configured")
	ErrBudgetExceeded  = errors.New("cost exceeds budget even after full degradation")
	ErrEmptyProject    = errors.New("project must have at least one seg")
)
