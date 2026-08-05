package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/hybrid"
	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/store"
)

func wireHybrid(t *testing.T, deps *Deps) {
	t.Helper()
	s, ok := deps.Store.(*store.SQLiteStore)
	if !ok {
		t.Fatal("want SQLiteStore")
	}
	deps.Hybrid = hybrid.New(hybrid.Options{Store: s})
}

func TestHybridPlanWithoutEngineReturns503(t *testing.T) {
	handler := NewServer(newDeps(t)).Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/hybrid/plan", strings.NewReader(`{"project":{"id":"p1","segs":[{"seg_id":"s1","text":"hi","duration_budget_ms":{"min_ms":1800,"max_ms":2200},"emotion_tag":"neutral","breath":"none"}]}}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("plan = %d, want 503: %s", rec.Code, rec.Body)
	}
}

func TestHybridPlanPersistsAndLists(t *testing.T) {
	deps := newDeps(t)
	wireHybrid(t, &deps)
	handler := NewServer(deps).Handler()

	p := model.NewProject("proj-hybrid", "t", time.Now().UTC())
	p.Segs = []model.Seg{
		model.NewSeg("hook", "Stop scrolling now", 3000),
		model.NewSeg("b1", "Survey shows 73% data", 5000),
	}
	p.Segs[0].EmotionTag = model.EmotionUrgent
	p.Segs[0].ContinuityGroup = "hero"
	p.Seal()

	body := `{"project":` + mustJSON(t, p) + `}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/hybrid/plan", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("plan = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"ai_routes":1`) {
		t.Fatalf("expected one AI route: %s", rec.Body)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/hybrid/plans/proj-hybrid", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"route":"ai_video"`) {
		t.Fatalf("missing ai route in list: %s", rec.Body)
	}
}

func TestHybridPlansWithoutEngineReturnsEmpty(t *testing.T) {
	handler := NewServer(newDeps(t)).Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/hybrid/plans/none", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"plans":[]`) {
		t.Fatalf("want empty plans: %s", rec.Body)
	}
}
