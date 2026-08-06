package youtube_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/youtube"
)

type fakeCreds map[string]string

func (f fakeCreds) Get(_ context.Context, key string) (string, error) {
	v, ok := f[key]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}

func TestPublishWithStubToken(t *testing.T) {
	dir := t.TempDir()
	vpath := filepath.Join(dir, "v.mp4")
	if err := os.WriteFile(vpath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	eng := youtube.New(youtube.Options{
		Store: db, Credentials: fakeCreds{"platform/youtube": "stub:abc123"},
		Uploader: youtube.StubUploader{}, OutputDir: dir,
	})
	res, err := eng.Publish(context.Background(), youtube.PublishRequest{
		ProjectID: "p1", VideoPath: vpath, Title: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.VideoID == "" {
		t.Fatal("missing video_id")
	}
}

func TestQuotaExceededReadableError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"errors":[{"reason":"quotaExceeded","message":"Daily Limit Exceeded"}]}}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	vpath := filepath.Join(dir, "v.mp4")
	_ = os.WriteFile(vpath, []byte("x"), 0o644)

	up := youtube.NewAPIUploader()
	up.BaseURL = srv.URL
	up.Client = srv.Client()

	_, err := up.Upload(context.Background(), "real-token", youtube.BuildUploadRequest(vpath, "t"))
	if !errors.Is(err, youtube.ErrQuotaExceeded) {
		t.Fatalf("err = %v want ErrQuotaExceeded", err)
	}
}

func TestUploadRequestSyntheticAlwaysTrue(t *testing.T) {
	req := youtube.BuildUploadRequest("/out/v.mp4", "title")
	req.Synthetic = false
	b, err := youtube.EncodeUploadRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(b), `"synthetic":true`) && !contains(string(b), `"synthetic": true`) {
		t.Fatalf("synthetic not true: %s", b)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
