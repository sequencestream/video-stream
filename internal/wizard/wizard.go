package wizard

import "errors"

const (
	StepSetup   = 1
	StepTopics  = 2
	StepHook    = 3
	StepScript  = 4
	StepAssets  = 5
	StepPreview = 6
	StepDeliver = 7
	StepCount   = 7

	// MaxCostMicrosUSD is the MVP per-run budget ($1.00 expressed in micro-dollars).
	MaxCostMicrosUSD int64 = 1_000_000

	// HookConfirmBudgetMS is the product target for step 3 interaction time.
	HookConfirmBudgetMS int64 = 30_000
)

var (
	ErrNoStore           = errors.New("wizard has no store configured")
	ErrSessionNotFound   = errors.New("wizard session not found")
	ErrWrongStep         = errors.New("wizard action does not match the current step")
	ErrBudgetExceeded    = errors.New("wizard run exceeded the $1 budget cap")
	ErrSessionFailed     = errors.New("wizard session is in failed state; resume from failed step")
	ErrOperationRequired = errors.New("operation_id is required and must be a UUID")
	ErrVersionRequired   = errors.New("expected_version must be positive")
)

// RequestError carries a stable API error code and, for stale clients, the
// authoritative session snapshot they should render next.
type RequestError struct {
	Code    string
	Message string
	Session *Session
}

func (e *RequestError) Error() string { return e.Message }

// StepTitle returns the human title for a step id.
func StepTitle(step int) string {
	switch step {
	case StepSetup:
		return "主题与对标账号"
	case StepTopics:
		return "选题卡片"
	case StepHook:
		return "Hook 三选一"
	case StepScript:
		return "脚本定稿"
	case StepAssets:
		return "素材与音轨"
	case StepPreview:
		return "720p 预览"
	case StepDeliver:
		return "1080p 出片"
	default:
		return "unknown"
	}
}
