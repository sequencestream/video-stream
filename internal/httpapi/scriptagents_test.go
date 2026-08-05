package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/scriptagents"
	"github.com/sequencestream/video-stream/internal/store"
)

func wireScriptAgents(t *testing.T, deps *Deps) {
	t.Helper()
	s, ok := deps.Store.(*store.SQLiteStore)
	if !ok {
		t.Fatalf("deps.Store is %T, want *store.SQLiteStore", deps.Store)
	}
	deps.ScriptAgents = scriptagents.New(scriptagents.Options{Store: s})
}

func TestScriptPolishWithoutEngineReturns503(t *testing.T) {
	rec := httptest.NewRecorder()
	body := `{"topic":"fitness"}`
	NewServer(newDeps(t)).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/script/polish", strings.NewReader(body)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST polish = %d", rec.Code)
	}
}

func TestScriptPolishCreatesValidProject(t *testing.T) {
	deps := newDeps(t)
	wireScriptAgents(t, &deps)
	body := `{"topic":"fitness","spike":"nobody talks about this","project_id":"proj-test"}`
	rec := httptest.NewRecorder()
	NewServer(deps).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/script/polish", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST polish = %d: %s", rec.Code, rec.Body)
	}
	var result scriptagents.PolishResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.TokensUsed <= 0 {
		t.Fatalf("tokens not recorded")
	}
	if err := result.Project.Validate(); err != nil {
		t.Fatalf("project: %v", err)
	}
}
