package store

import (
	"context"
	"time"
)

// YouTubeUploadRecord tracks one publish attempt.
type YouTubeUploadRecord struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	SessionID string    `json:"session_id,omitempty"`
	VideoID   string    `json:"video_id,omitempty"`
	VideoPath string    `json:"video_path"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// YouTubeStore persists upload records.
type YouTubeStore interface {
	PutYouTubeUpload(ctx context.Context, rec YouTubeUploadRecord) error
	GetYouTubeUpload(ctx context.Context, id string) (YouTubeUploadRecord, error)
}
