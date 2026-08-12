package youtube

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sequencestream/video-stream/internal/credential"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
	"github.com/sequencestream/video-stream/internal/youtube/notify"
)

// PublishRequest starts a YouTube upload.
type PublishRequest struct {
	UploadID    string
	ProjectID   string
	SessionID   string
	VideoPath   string
	Title       string
	Description string
	Tags        []string
	Visibility  string
	Notify      bool
}

// PublishResult is the persisted upload outcome.
type PublishResult struct {
	UploadID string                    `json:"upload_id"`
	VideoID  string                    `json:"video_id"`
	URL      string                    `json:"url,omitempty"`
	Record   store.YouTubeUploadRecord `json:"record"`
}

// tokenSource reads platform OAuth tokens.
type tokenSource interface {
	Get(ctx context.Context, key string) (string, error)
}

// Options configures the Engine.
type Options struct {
	Store       store.YouTubeStore
	Credentials tokenSource
	Uploader    Uploader
	Notifier    notify.Notifier
	OutputDir   string
	Reporter    telemetry.Reporter
}

// Engine publishes rendered videos and sends completion notifications.
type Engine struct {
	store       store.YouTubeStore
	credentials tokenSource
	uploader    Uploader
	notifier    notify.Notifier
	outputDir   string
	reporter    telemetry.Reporter
}

// New builds an Engine.
func New(opts Options) *Engine {
	u := opts.Uploader
	if u == nil {
		u = NewAPIUploader()
	}
	r := opts.Reporter
	if r == nil {
		r = telemetry.Nop()
	}
	return &Engine{
		store: opts.Store, credentials: opts.Credentials, uploader: u,
		notifier: opts.Notifier, outputDir: opts.OutputDir, reporter: r,
	}
}

// Publish uploads a video using user-held OAuth credentials.
func (e *Engine) Publish(ctx context.Context, req PublishRequest) (PublishResult, error) {
	if e.store == nil {
		return PublishResult{}, ErrNoStore
	}
	if req.UploadID != "" {
		if existing, err := e.store.GetYouTubeUpload(ctx, req.UploadID); err == nil && existing.Status == "completed" {
			return PublishResult{UploadID: existing.ID, VideoID: existing.VideoID,
				URL: "https://youtu.be/" + existing.VideoID, Record: existing}, nil
		}
	}
	path := req.VideoPath
	if path == "" {
		var err error
		path, err = e.DefaultVideoPath(req.ProjectID)
		if err != nil {
			return PublishResult{}, err
		}
	}
	uploadReq := BuildUploadRequest(path, req.Title)
	uploadReq.Description = req.Description
	uploadReq.Tags = req.Tags
	if req.Visibility != "" {
		uploadReq.Visibility = req.Visibility
	}

	token, err := e.loadToken(ctx)
	if err != nil {
		return PublishResult{}, err
	}

	uploadID := req.UploadID
	if uploadID == "" {
		uploadID = newUploadID()
	}
	_ = e.store.PutYouTubeUpload(ctx, store.YouTubeUploadRecord{
		ID: uploadID, ProjectID: req.ProjectID, SessionID: req.SessionID,
		VideoPath: path, Status: "running", CreatedAt: time.Now().UTC(),
	})
	var result UploadResult
	var lastErr error
	for attempt := 0; attempt < MaxUploadRetries; attempt++ {
		result, lastErr = e.uploader.Upload(ctx, token, uploadReq)
		if lastErr == nil {
			break
		}
		if errors.Is(lastErr, ErrQuotaExceeded) {
			return PublishResult{}, lastErr
		}
	}
	if lastErr != nil {
		rec := store.YouTubeUploadRecord{
			ID: uploadID, ProjectID: req.ProjectID, SessionID: req.SessionID, VideoPath: path,
			Status: "failed", Error: lastErr.Error(), CreatedAt: time.Now().UTC(),
		}
		_ = e.store.PutYouTubeUpload(ctx, rec)
		return PublishResult{}, lastErr
	}

	rec := store.YouTubeUploadRecord{
		ID: uploadID, ProjectID: req.ProjectID, SessionID: req.SessionID,
		VideoID: result.VideoID, VideoPath: path, Status: "completed",
		CreatedAt: time.Now().UTC(),
	}
	if err := e.store.PutYouTubeUpload(ctx, rec); err != nil {
		return PublishResult{}, err
	}

	if req.Notify && e.notifier != nil {
		_ = e.notifier.Notify(ctx, notify.Event{
			ProjectID: req.ProjectID, SessionID: req.SessionID,
			OutputURI: path, VideoID: result.VideoID, Title: req.Title,
			CompletedAt: time.Now().UTC(),
		})
	}

	_ = telemetry.Report(ctx, e.reporter, "youtube.published", map[string]any{
		"project_id": req.ProjectID, "video_id": result.VideoID,
	})

	return PublishResult{UploadID: rec.ID, VideoID: result.VideoID, URL: result.URL, Record: rec}, nil
}

// NotifyComplete sends completion notifications without publishing.
func (e *Engine) NotifyComplete(ctx context.Context, ev notify.Event) error {
	if e.notifier == nil {
		return nil
	}
	return e.notifier.Notify(ctx, ev)
}

// GetUpload returns a stored upload record.
func (e *Engine) GetUpload(ctx context.Context, id string) (store.YouTubeUploadRecord, error) {
	if e.store == nil {
		return store.YouTubeUploadRecord{}, ErrNoStore
	}
	rec, err := e.store.GetYouTubeUpload(ctx, id)
	if errors.Is(err, store.ErrYouTubeUploadNotFound) {
		return store.YouTubeUploadRecord{}, ErrUploadNotFound
	}
	return rec, err
}

// DefaultVideoPath resolves the 1080p delivery path for a project.
func (e *Engine) DefaultVideoPath(projectID string) (string, error) {
	if e.outputDir == "" {
		return "", fmt.Errorf("output dir not configured")
	}
	path := filepath.Join(e.outputDir, projectID, "1080p.mp4")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%w: %s", ErrVideoMissing, path)
	}
	return path, nil
}

// DownloadPath returns the on-disk path for client download (no upload).
func (e *Engine) DownloadPath(projectID string) (string, error) {
	return e.DefaultVideoPath(projectID)
}

func (e *Engine) loadToken(ctx context.Context) (string, error) {
	if e.credentials == nil {
		return "", ErrNoCredential
	}
	token, err := e.credentials.Get(ctx, credential.PlatformKey(CredentialKey))
	if errors.Is(err, credential.ErrNotFound) {
		return "", ErrNoCredential
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

func newUploadID() string {
	return fmt.Sprintf("yt-%d", time.Now().UTC().UnixNano())
}
