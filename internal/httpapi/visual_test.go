package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/visual"
)

func wireVisual(t *testing.T, deps *Deps) {
	t.Helper()
	s, ok := deps.Store.(*store.SQLiteStore)
	if !ok {
		t.Fatal("want SQLiteStore")
	}
	deps.Visual = visual.New(visual.Options{Store: s})
}

func TestVisualApplyReturnsFullRerunWarning(t *testing.T) {
	deps := newDeps(t)
	wireVisual(t, &deps)
	handler := NewServer(deps).Handler()

	createBody := `{"id":"p1","name":"Warm","schema_version":1,"stack":{"style_ref_uri":"file://r.jpg","palette":["#FFFFFF","#000000"],"lighting_preset":"soft","composition_rule":"center"}}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/visual/packs", strings.NewReader(createBody)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}

	applyBody := `{"project":{"id":"proj","schema_version":2,"segs":[{"seg_id":"s1","text":"hi","duration_budget_ms":{"min_ms":1800,"max_ms":2200},"emotion_tag":"neutral","breath":"none","content_hash":"ch1:x","render_cache_key":"rk2:y"}],"render_profile":{"style_anchor":"l2:other"},"timeline":{}}}`
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/visual/packs/p1/apply", strings.NewReader(applyBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("apply = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "full_rerun_warning") {
		t.Fatalf("missing warning: %s", rec.Body)
	}
}

func TestVisualImportExport(t *testing.T) {
	deps := newDeps(t)
	wireVisual(t, &deps)
	handler := NewServer(deps).Handler()

	createBody := `{"id":"exp1","name":"Export","schema_version":1,"stack":{"style_ref_uri":"file://r.jpg","palette":["#AAA","#BBB"],"lighting_preset":"soft","composition_rule":"thirds"}}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/visual/packs", strings.NewReader(createBody)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/visual/packs/exp1/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d", rec.Code)
	}
	exported := rec.Body.String()

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/visual/packs/import", strings.NewReader(exported)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body)
	}
}
