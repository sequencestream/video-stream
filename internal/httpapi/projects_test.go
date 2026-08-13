package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/audio"
	"github.com/sequencestream/video-stream/internal/intake"
	"github.com/sequencestream/video-stream/internal/media"
	"github.com/sequencestream/video-stream/internal/store"
)

// intakeDeps wires the project routes onto the same store the rest of the
// handlers use, with an estimating prober so no TTS process is spawned.
func intakeDeps(t *testing.T) (Deps, *store.SQLiteStore, string) {
	t.Helper()
	deps := newDeps(t)
	s, err := store.OpenSQLite(filepath.Join(t.TempDir(), "projects.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	mediaDir := filepath.Join(t.TempDir(), "media")
	deps.Projects = s
	deps.Intake = intake.New(intake.Options{Projects: s, Prober: audio.EstimateProbe{}, Voice: "zh-CN-XiaoxiaoNeural"})
	preparer := media.Preparer{MediaDir: mediaDir}
	deps.Media = &preparer
	return deps, s, mediaDir
}

func postJSON(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	return rec
}

func TestCreateProjectImportsAScriptAndPersistsIt(t *testing.T) {
	deps, s, _ := intakeDeps(t)
	handler := NewServer(deps).Handler()

	rec := postJSON(t, handler, "/v1/projects", `{"title":"好好吃饭","script":"今天带着小孩去看了一场电影。整个影片反映了战争的残酷。"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /v1/projects = %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Project struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Segs  []struct {
				SegID          string `json:"seg_id"`
				ContentHash    string `json:"content_hash"`
				RenderCacheKey string `json:"render_cache_key"`
			} `json:"segs"`
		} `json:"project"`
		Lines []struct {
			ProbedMS int64 `json:"probed_ms"`
		} `json:"lines"`
		SegCount int   `json:"seg_count"`
		TotalMS  int64 `json:"total_ms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SegCount != 2 || len(result.Project.Segs) != 2 {
		t.Fatalf("got %d segs, want 2: %s", result.SegCount, rec.Body.String())
	}
	if result.TotalMS <= 0 {
		t.Errorf("total_ms = %d, want a measured duration", result.TotalMS)
	}
	// A project that reaches the render pipeline unsealed fails at the write,
	// so the response has to carry derived fields already computed.
	for _, seg := range result.Project.Segs {
		if seg.ContentHash == "" || seg.RenderCacheKey == "" {
			t.Errorf("seg %s came back unsealed", seg.SegID)
		}
	}

	if _, err := s.GetProject(context.Background(), result.Project.ID); err != nil {
		t.Errorf("imported project was not persisted: %v", err)
	}
}

func TestCreateProjectRejectsMissingFields(t *testing.T) {
	deps, _, _ := intakeDeps(t)
	handler := NewServer(deps).Handler()

	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "no title", body: `{"script":"今天带着小孩去看了一场电影。"}`, code: "missing_fields"},
		{name: "no script", body: `{"title":"t"}`, code: "missing_fields"},
		{name: "unspeakable script", body: `{"title":"t","script":"   "}`, code: "empty_script"},
		{name: "not json", body: `{`, code: "invalid_body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postJSON(t, handler, "/v1/projects", tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.String())
			}
			var apiErr struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &apiErr); err != nil {
				t.Fatal(err)
			}
			if apiErr.Code != tt.code {
				t.Errorf("code = %q, want %q", apiErr.Code, tt.code)
			}
		})
	}
}

func TestProjectListGetAndDeleteRoundTrip(t *testing.T) {
	deps, _, _ := intakeDeps(t)
	handler := NewServer(deps).Handler()

	rec := postJSON(t, handler, "/v1/projects", `{"project_id":"demo","title":"t","script":"今天带着小孩去看了一场电影。"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/projects", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"demo"`) {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/projects/demo", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/projects/demo", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/projects/demo", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", rec.Code)
	}
}

func TestListProjectsRejectsABadLimit(t *testing.T) {
	deps, _, _ := intakeDeps(t)
	rec := httptest.NewRecorder()
	NewServer(deps).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/projects?limit=zero", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestProjectBackgroundFitsEverySeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	deps, _, mediaDir := intakeDeps(t)
	preparer := media.Preparer{MediaDir: mediaDir, FFmpegBinary: ffmpeg}
	deps.Media = &preparer
	handler := NewServer(deps).Handler()

	source := filepath.Join(t.TempDir(), "bg.jpg")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=teal:s=800x600", "-frames:v", "1", "-y", source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make source: %v: %s", err, out)
	}

	rec := postJSON(t, handler, "/v1/projects", `{"project_id":"demo","title":"t","script":"第一句话说的是这个。第二句话说的是那个。"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, handler, "/v1/projects/demo/background",
		`{"image":"`+source+`","anchor":"top","resolution":"720p"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("background = %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Files  []string `json:"files"`
		Width  int      `json:"width"`
		Height int      `json:"height"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 2 {
		t.Fatalf("got %d files, want one per seg", len(result.Files))
	}
	if result.Width != 1280 || result.Height != 720 {
		t.Errorf("fitted to %dx%d, want the 720p frame", result.Width, result.Height)
	}
	for _, path := range result.Files {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("background %s was not written: %v", path, err)
		}
	}
}

func TestProjectBackgroundRejectsAMissingImage(t *testing.T) {
	deps, _, _ := intakeDeps(t)
	handler := NewServer(deps).Handler()

	rec := postJSON(t, handler, "/v1/projects", `{"project_id":"demo","title":"t","script":"第一句话说的是这个。"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	rec = postJSON(t, handler, "/v1/projects/demo/background", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestProjectRoutesReportAnUnconfiguredEngine(t *testing.T) {
	handler := NewServer(newDeps(t)).Handler()

	rec := postJSON(t, handler, "/v1/projects", `{"title":"t","script":"第一句话说的是这个。"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("create without intake = %d, want 503", rec.Code)
	}

	// Listing stays 200 with an empty collection: "no projects" is the honest
	// answer and a 503 would look like a broken deployment.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/projects", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"projects":[]`) {
		t.Errorf("list without a store = %d: %s", rec.Code, rec.Body.String())
	}
}
