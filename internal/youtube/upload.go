// Package youtube builds upload requests for the YouTube adapter.
//
// Synthetic disclosure is always enabled; there is no field or flag to turn it off.
package youtube

import "encoding/json"

// Visibility values for YouTube uploads.
const (
	VisibilityPrivate  = "private"
	VisibilityUnlisted = "unlisted"
	VisibilityPublic   = "public"
)

// UploadRequest is the body sent to the YouTube upload adapter.
type UploadRequest struct {
	VideoPath   string   `json:"video_path"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Visibility  string   `json:"visibility,omitempty"`
	// Synthetic must always be true for AI-generated content disclosure.
	Synthetic bool `json:"synthetic"`
}

// UploadResult is returned after a successful upload.
type UploadResult struct {
	VideoID string `json:"video_id"`
	URL     string `json:"url,omitempty"`
}

// BuildUploadRequest constructs a request with synthetic disclosure hardcoded on.
func BuildUploadRequest(videoPath, title string) UploadRequest {
	return UploadRequest{
		VideoPath:  videoPath,
		Title:      title,
		Visibility: VisibilityPrivate,
		Synthetic:  true,
	}
}

// EncodeUploadRequest serializes the request for HTTP transport.
func EncodeUploadRequest(req UploadRequest) ([]byte, error) {
	req.Synthetic = true
	return json.Marshal(req)
}

// videoResource is the YouTube Data API snippet/status body.
type videoResource struct {
	Snippet snippet `json:"snippet"`
	Status  status  `json:"status"`
}

type snippet struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
}

type status struct {
	PrivacyStatus            string `json:"privacyStatus"`
	ContainsSyntheticMedia   bool   `json:"containsSyntheticMedia"`
	SelfDeclaredMadeForKids  bool   `json:"selfDeclaredMadeForKids"`
}

func buildVideoResource(req UploadRequest) videoResource {
	vis := req.Visibility
	if vis == "" {
		vis = VisibilityPrivate
	}
	return videoResource{
		Snippet: snippet{Title: req.Title, Description: req.Description, Tags: req.Tags},
		Status: status{
			PrivacyStatus: vis, ContainsSyntheticMedia: true,
		},
	}
}
