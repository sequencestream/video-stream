package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/youtube"
)

func wireYouTube(t *testing.T, deps *Deps) string {
	t.Helper()
	dir := t.TempDir()
	vpath := filepath.Join(dir, "p1", "1080p.mp4")
	if err := os.MkdirAll(filepath.Dir(vpath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vpath, []byte("mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, ok := deps.Store.(*store.SQLiteStore)
	if !ok {
		t.Fatal("want sqlite store")
	}
	deps.YouTube = youtube.New(youtube.Options{
		Store: s, Uploader: youtube.StubUploader{},
		Credentials: fakeYTToken{"platform/youtube": "token"},
		OutputDir: dir,
	})
	return vpath
}

type fakeYTToken map[string]string

func (f fakeYTToken) Get(_ context.Context, key string) (string, error) {
	return f[key], nil
}

func TestYouTubePublishReturnsVideoID(t *testing.T) {
	deps := newDeps(t)
	vpath := wireYouTube(t, &deps)
	handler := NewServer(deps).Handler()

	body := `{"project_id":"p1","title":"demo","video_path":"` + vpath + `"}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/youtube/publish", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("publish = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"video_id"`) {
		t.Fatalf("missing video_id: %s", rec.Body)
	}
}

func TestDeliveryDownloadWithoutEngineReturns503(t *testing.T) {
	handler := NewServer(newDeps(t)).Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/delivery/download?project_id=p1", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("download = %d, want 503", rec.Code)
	}
}

func TestDeliveryDownloadServesFile(t *testing.T) {
	deps := newDeps(t)
	wireYouTube(t, &deps)
	handler := NewServer(deps).Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/delivery/download?project_id=p1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
}
