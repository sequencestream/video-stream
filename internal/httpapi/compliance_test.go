package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/compliance"
	"github.com/sequencestream/video-stream/internal/store"
)

func wireCompliance(t *testing.T, deps *Deps) {
	t.Helper()
	s, ok := deps.Store.(*store.SQLiteStore)
	if !ok {
		t.Fatalf("want SQLiteStore")
	}
	eng, err := compliance.New(compliance.Options{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	deps.Compliance = eng
}

func TestComplianceCheckBlocksMissingNonTemplate(t *testing.T) {
	deps := newDeps(t)
	wireCompliance(t, &deps)
	body := `{"account_id":"a1","structure_card_id":"c1","fingerprint":[0.1,0.2],"script_text":"hello"}`
	rec := httptest.NewRecorder()
	NewServer(deps).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/compliance/check", strings.NewReader(body)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
	var result compliance.CheckResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Fatal("expected failure")
	}
}

func TestComplianceCheckPassesWithElement(t *testing.T) {
	deps := newDeps(t)
	wireCompliance(t, &deps)
	body := `{"account_id":"a1","structure_card_id":"c1","fingerprint":[0.5,0.5,0.5,0.5],"script_text":"My data shows 42% growth","non_template_elements":[{"kind":"first_hand_data","content":"42%"}]}`
	rec := httptest.NewRecorder()
	NewServer(deps).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/compliance/check", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
}
