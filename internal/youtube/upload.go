// Package youtube builds upload requests for the YouTube adapter.
//
// Synthetic disclosure is always enabled; there is no field or flag to turn it off.
package youtube

import "encoding/json"

// UploadRequest is the body sent to the YouTube upload adapter.
type UploadRequest struct {
	VideoPath string `json:"video_path"`
	Title     string `json:"title"`
	// Synthetic must always be true for AI-generated content disclosure.
	Synthetic bool `json:"synthetic"`
}

// BuildUploadRequest constructs a request with synthetic disclosure hardcoded on.
func BuildUploadRequest(videoPath, title string) UploadRequest {
	return UploadRequest{
		VideoPath: videoPath,
		Title:     title,
		Synthetic: true,
	}
}

// EncodeUploadRequest serializes the request for HTTP transport.
func EncodeUploadRequest(req UploadRequest) ([]byte, error) {
	// Force synthetic even if a caller mutated the struct.
	req.Synthetic = true
	return json.Marshal(req)
}
