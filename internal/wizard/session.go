package wizard

import (
	"encoding/json"
	"time"

	"github.com/sequencestream/video-stream/internal/model"
)

// AccountInput is one competitor account imported in step 1.
type AccountInput struct {
	Platform string `json:"platform"`
	Handle   string `json:"handle"`
}

// SessionState is persisted wizard progress.
type SessionState struct {
	TopicCards      []TopicOption   `json:"topic_cards,omitempty"`
	HookDrafts      []HookOption    `json:"hook_drafts,omitempty"`
	SelectedTopicID string          `json:"selected_topic_id,omitempty"`
	SelectedDraftID string          `json:"selected_draft_id,omitempty"`
	PreviewRunID    string          `json:"preview_run_id,omitempty"`
	DeliveryRunID   string          `json:"delivery_run_id,omitempty"`
	OutputURI       string          `json:"output_uri,omitempty"`
	InvalidatedSegs int             `json:"invalidated_segs,omitempty"`
	TotalSegs       int             `json:"total_segs,omitempty"`
	HookShownAt     time.Time       `json:"hook_shown_at,omitempty"`
	Accounts        []AccountInput  `json:"accounts,omitempty"`
	CostPlan        *model.CostPlan `json:"cost_plan,omitempty"`
	YouTubeVideoID  string          `json:"youtube_video_id,omitempty"`
}

// TopicOption is one selectable topic card.
type TopicOption struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Rationale string `json:"rationale"`
}

// HookOption is one Writer draft surfaced in step 3.
type HookOption struct {
	ID             string   `json:"id"`
	Direction      string   `json:"direction"`
	HookText       string   `json:"hook_text"`
	DropOffReasons []string `json:"drop_off_reasons"`
}

// Session is the wizard run visible to clients.
type Session struct {
	ID            string       `json:"id"`
	CurrentStep   int          `json:"current_step"`
	Status        string       `json:"status"`
	Topic         string       `json:"topic"`
	Category      string       `json:"category"`
	ProjectID     string       `json:"project_id,omitempty"`
	CostMicros    int64        `json:"cost_micros"`
	FailedStep    int          `json:"failed_step,omitempty"`
	Error         string       `json:"error,omitempty"`
	HookConfirmMS int64        `json:"hook_confirm_ms,omitempty"`
	Version       int64        `json:"version"`
	State         SessionState `json:"state"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

// CreateRequest starts step 1.
type CreateRequest struct {
	OperationID string         `json:"operation_id"`
	Topic       string         `json:"topic"`
	Category    string         `json:"category"`
	Accounts    []AccountInput `json:"accounts"`
}

// AdvanceRequest carries step-specific input when completing a step.
type AdvanceRequest struct {
	OperationID     string `json:"operation_id"`
	ExpectedVersion int64  `json:"expected_version"`
	TopicCardID     string `json:"topic_card_id,omitempty"`
	DraftID         string `json:"draft_id,omitempty"`
	HookEdit        string `json:"hook_edit,omitempty"`
	EditSegID       string `json:"edit_seg_id,omitempty"`
	EditText        string `json:"edit_text,omitempty"`
	Resume          bool   `json:"resume,omitempty"`
}

func encodeState(s SessionState) (string, error) {
	b, err := json.Marshal(s)
	return string(b), err
}

func decodeState(raw string) (SessionState, error) {
	if raw == "" {
		return SessionState{}, nil
	}
	var s SessionState
	err := json.Unmarshal([]byte(raw), &s)
	return s, err
}
