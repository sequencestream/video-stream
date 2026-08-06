package youtube

import "errors"

var (
	// ErrNoCredential means no YouTube OAuth token is stored.
	ErrNoCredential = errors.New("youtube oauth credential not found; run vs credential set youtube")
	// ErrQuotaExceeded is returned when the Data API quota is exhausted.
	ErrQuotaExceeded = errors.New("youtube data api quota exceeded")
	// ErrUploadFailed means the upload did not succeed after retries.
	ErrUploadFailed = errors.New("youtube upload failed")
	// ErrNoStore is returned when persistence is not configured.
	ErrNoStore = errors.New("youtube has no store configured")
	// ErrUploadNotFound means the upload record does not exist.
	ErrUploadNotFound = errors.New("youtube upload not found")
	// ErrVideoMissing means the rendered file is not on disk.
	ErrVideoMissing = errors.New("rendered video file not found")
)

const (
	// MaxUploadRetries is how many times a transient upload error is retried.
	MaxUploadRetries = 3
	// CredentialKey is the platform credential namespace key.
	CredentialKey = "youtube"
)
