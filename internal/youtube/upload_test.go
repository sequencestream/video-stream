package youtube_test

import (
	"encoding/json"
	"testing"

	"github.com/sequencestream/video-stream/internal/youtube"
)

func TestUploadRequestSyntheticAlwaysTrue(t *testing.T) {
	req := youtube.BuildUploadRequest("/out/v.mp4", "title")
	if !req.Synthetic {
		t.Fatal("synthetic must be true")
	}
	req.Synthetic = false
	b, err := youtube.EncodeUploadRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded youtube.UploadRequest
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Synthetic {
		t.Fatalf("encoded body lost synthetic=true: %s", b)
	}
}
