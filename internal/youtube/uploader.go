package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Uploader performs the YouTube Data API upload.
type Uploader interface {
	Upload(ctx context.Context, oauthToken string, req UploadRequest) (UploadResult, error)
}

// APIUploader uploads via the YouTube Data API resumable endpoint (MVP: single-shot when file is small).
type APIUploader struct {
	Client  *http.Client
	BaseURL string
}

// NewAPIUploader builds an uploader with defaults.
func NewAPIUploader() *APIUploader {
	return &APIUploader{
		Client:  &http.Client{Timeout: 5 * time.Minute},
		BaseURL: "https://www.googleapis.com/upload/youtube/v3",
	}
}

// Upload sends the video with OAuth bearer token.
func (u *APIUploader) Upload(ctx context.Context, oauthToken string, req UploadRequest) (UploadResult, error) {
	if strings.TrimSpace(oauthToken) == "" {
		return UploadResult{}, ErrNoCredential
	}
	if _, err := os.Stat(req.VideoPath); err != nil {
		return UploadResult{}, fmt.Errorf("%w: %s", ErrVideoMissing, req.VideoPath)
	}

	body, err := json.Marshal(buildVideoResource(req))
	if err != nil {
		return UploadResult{}, err
	}

	// MVP: stub token prefix skips network for local/dev flows.
	if strings.HasPrefix(oauthToken, "stub:") {
		id := strings.TrimPrefix(oauthToken, "stub:")
		if id == "" {
			id = "stub-video-id"
		}
		return UploadResult{VideoID: id, URL: "https://youtu.be/" + id}, nil
	}

	initURL := u.BaseURL + "/videos?uploadType=resumable&part=snippet,status"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, bytes.NewReader(body))
	if err != nil {
		return UploadResult{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+oauthToken)
	httpReq.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp, err := u.Client.Do(httpReq)
	if err != nil {
		return UploadResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if isQuotaError(resp.StatusCode, string(msg)) {
			return UploadResult{}, fmt.Errorf("%w: %s", ErrQuotaExceeded, trimMsg(string(msg)))
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return UploadResult{}, fmt.Errorf("%w: status %d: %s", ErrUploadFailed, resp.StatusCode, trimMsg(string(msg)))
	}

	location := resp.Header.Get("Location")
	if location == "" {
		return UploadResult{}, fmt.Errorf("%w: missing resumable upload location", ErrUploadFailed)
	}

	video, err := os.ReadFile(req.VideoPath)
	if err != nil {
		return UploadResult{}, err
	}
	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, location, bytes.NewReader(video))
	if err != nil {
		return UploadResult{}, err
	}
	putReq.Header.Set("Content-Type", "application/octet-stream")
	putResp, err := u.Client.Do(putReq)
	if err != nil {
		return UploadResult{}, err
	}
	defer putResp.Body.Close()

	putBody, _ := io.ReadAll(io.LimitReader(putResp.Body, 8192))
	if putResp.StatusCode == http.StatusForbidden || putResp.StatusCode == http.StatusTooManyRequests {
		if isQuotaError(putResp.StatusCode, string(putBody)) {
			return UploadResult{}, fmt.Errorf("%w: %s", ErrQuotaExceeded, trimMsg(string(putBody)))
		}
	}
	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		return UploadResult{}, fmt.Errorf("%w: status %d: %s", ErrUploadFailed, putResp.StatusCode, trimMsg(string(putBody)))
	}

	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(putBody, &parsed); err != nil || parsed.ID == "" {
		return UploadResult{}, fmt.Errorf("%w: response missing video id", ErrUploadFailed)
	}
	return UploadResult{VideoID: parsed.ID, URL: "https://youtu.be/" + parsed.ID}, nil
}

func isQuotaError(status int, body string) bool {
	lower := strings.ToLower(body)
	return status == http.StatusTooManyRequests ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "quotaexceeded")
}

func trimMsg(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// StubUploader always succeeds for tests.
type StubUploader struct {
	VideoID string
}

func (s StubUploader) Upload(_ context.Context, oauthToken string, req UploadRequest) (UploadResult, error) {
	if oauthToken == "" {
		return UploadResult{}, ErrNoCredential
	}
	id := s.VideoID
	if id == "" {
		id = "yt-stub-" + strings.ReplaceAll(req.Title, " ", "-")
	}
	return UploadResult{VideoID: id, URL: "https://youtu.be/" + id}, nil
}
