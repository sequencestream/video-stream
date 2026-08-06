package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/costwarden"
	"github.com/sequencestream/video-stream/internal/model"
)

func wireCostWarden(t *testing.T, deps *Deps) {
	t.Helper()
	deps.CostWarden = costwarden.New(costwarden.Options{})
}

func TestCostEstimateAtScriptStage(t *testing.T) {
	deps := newDeps(t)
	wireCostWarden(t, &deps)
	handler := NewServer(deps).Handler()

	p := model.NewProject("script-only", "t", time.Now().UTC())
	p.Segs = []model.Seg{model.NewSeg("hook", "Hello", 2000)}
	p.Seal()

	body := `{"project":` + mustJSON(t, p) + `,"budget_micros":1000000}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/cost/estimate", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("estimate = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"estimated_micros"`) {
		t.Fatalf("missing estimate: %s", rec.Body)
	}
}

func TestCostPlanWithoutEngineReturns503(t *testing.T) {
	handler := NewServer(newDeps(t)).Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/cost/plan",
		strings.NewReader(`{"project":{"id":"p1","segs":[{"seg_id":"s1","text":"hi","duration_budget_ms":{"min_ms":1800,"max_ms":2200},"emotion_tag":"neutral","breath":"none"}]}}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("plan = %d, want 503", rec.Code)
	}
}
