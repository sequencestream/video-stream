package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/render"
	"github.com/sequencestream/video-stream/internal/store"
)

func wireRender(t *testing.T, deps *Deps) {
	t.Helper()
	s, ok := deps.Store.(*store.SQLiteStore)
	if !ok {
		t.Fatal("want SQLiteStore")
	}
	out := t.TempDir()
	deps.Render = render.New(render.Options{Store: s, Artifacts: s, OutputDir: out, FFmpeg: render.StubFFmpeg{}, Video: render.StubVideoGenerator{OutputDir: out}})
}

func TestRenderRunWithoutEngineReturns503(t *testing.T) {
	handler := NewServer(newDeps(t)).Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/render/run", strings.NewReader(`{"project":{"id":"p1","segs":[{"seg_id":"s1","text":"hi","duration_budget_ms":{"min_ms":1800,"max_ms":2200},"emotion_tag":"neutral","breath":"none"}]}}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestRenderRun720p(t *testing.T) {
	deps := newDeps(t)
	wireRender(t, &deps)
	handler := NewServer(deps).Handler()

	p := model.NewProject("proj-r", "t", time.Now().UTC())
	p.Segs = []model.Seg{model.NewSeg("s1", "hello", 3000)}
	p.Seal()
	body := `{"project":` + mustJSON(t, p) + `,"resolution":"720p"}`

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/render/run", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("run = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"output_uri"`) {
		t.Fatalf("missing output: %s", rec.Body)
	}
}
