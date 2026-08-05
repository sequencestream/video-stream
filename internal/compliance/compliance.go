// Package compliance enforces the three inauthentic-differentiation gates.
//
// Every render-bound script must pass all three checks. There is no skip switch:
// the gates are computed metrics, not LLM opinions, so the user can appeal with
// numbers rather than argue with a model.
package compliance

import "errors"

var (
	// ErrGateBlocked means one gate rejected the submission.
	ErrGateBlocked = errors.New("compliance gate blocked")
	// ErrBypassAttempt means code tried to disable a gate below its floor.
	ErrBypassAttempt = errors.New("compliance gate cannot be disabled or loosened below floor")
)

// Hard floors — configurable thresholds may tighten but never loosen below these.
const (
	FloorRejectSimilarity = 0.85
	FloorPassSimilarity   = 0.70
	FloorReuseWindowDays  = 30
	FloorMaxReuses        = 3
)

// NonTemplateKind identifies an originality anchor beyond template structure.
type NonTemplateKind string

const (
	KindUserQuote       NonTemplateKind = "user_quote"
	KindFirstHandData   NonTemplateKind = "first_hand_data"
	KindExclusiveSource NonTemplateKind = "exclusive_source"
)

// NonTemplateElement is one non-template anchor in a draft.
type NonTemplateElement struct {
	Kind    NonTemplateKind `json:"kind"`
	Content string          `json:"content"`
	// Evidence locates the element in the script for audit.
	Evidence string `json:"evidence,omitempty"`
}

// GateResult is one gate's outcome.
type GateResult struct {
	Gate    string `json:"gate"`
	Passed  bool   `json:"passed"`
	Reason  string `json:"reason,omitempty"`
	Advice  string `json:"advice,omitempty"`
	Metric  string `json:"metric,omitempty"`
}

// CheckResult aggregates all gates.
type CheckResult struct {
	Passed bool         `json:"passed"`
	Gates  []GateResult `json:"gates"`
}
