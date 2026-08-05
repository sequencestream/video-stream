package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/ideation"
	"github.com/sequencestream/video-stream/internal/store"
)

func wireIdeation(t *testing.T, deps *Deps) {
	t.Helper()
	s, ok := deps.Store.(*store.SQLiteStore)
	if !ok {
		t.Fatalf("deps.Store is %T, want *store.SQLiteStore", deps.Store)
	}
	deps.Ideation = ideation.New(ideation.Options{Store: s})
}

func TestIdeationCardsWithoutAnEngineReturnsAnEmptyList(t *testing.T) {
	rec := httptest.NewRecorder()
	NewServer(newDeps(t)).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ideation/cards", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/ideation/cards = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("got %s", rec.Body.String())
	}
}

func TestIdeationExtractAndMigrate(t *testing.T) {
	deps := newDeps(t)
	wireIdeation(t, &deps)
	handler := NewServer(deps).Handler()

	extractBody := `{"post_id":"p1","category":"cooking","title":"Why does sourdough crack?","description":"twist on timing","duration_seconds":45,"forbidden_terms":["sourdough"]}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/ideation/extract", strings.NewReader(extractBody)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST extract = %d: %s", rec.Code, rec.Body)
	}

	var card ideation.StructureCard
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode card: %v", err)
	}

	migrateBody := `{"structure_card_id":"` + card.ID + `","user_theme":"home workouts","target_category":"fitness"}`
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/ideation/migrate", strings.NewReader(migrateBody)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST migrate = %d: %s", rec.Code, rec.Body)
	}

	var topics listTopicCardsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &topics); err != nil {
		t.Fatalf("decode topics: %v", err)
	}
	if len(topics.Items) < ideation.MinTopics || len(topics.Items) > ideation.MaxTopics {
		t.Fatalf("got %d topics", len(topics.Items))
	}
	for _, topic := range topics.Items {
		if topic.MigrationSource != card.ID {
			t.Fatalf("missing migration source on %+v", topic)
		}
	}
}

func TestIdeationRecall(t *testing.T) {
	deps := newDeps(t)
	wireIdeation(t, &deps)
	handler := NewServer(deps).Handler()

	extractBody := `{"post_id":"p2","category":"tech","title":"5 tricks for faster builds","duration_seconds":60}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/ideation/extract", strings.NewReader(extractBody)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST extract = %d: %s", rec.Code, rec.Body)
	}
	var card ideation.StructureCard
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode: %v", err)
	}

	recallBody := `{"embedding":` + mustJSON(t, card.Embedding) + `,"limit":5}`
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/ideation/recall", strings.NewReader(recallBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST recall = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), card.ID) {
		t.Fatalf("recall did not return source card: %s", rec.Body)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
