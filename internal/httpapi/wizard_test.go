package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/compliance"
	"github.com/sequencestream/video-stream/internal/config"
	"github.com/sequencestream/video-stream/internal/hybrid"
	"github.com/sequencestream/video-stream/internal/ideation"
	"github.com/sequencestream/video-stream/internal/radar"
	"github.com/sequencestream/video-stream/internal/recompile"
	"github.com/sequencestream/video-stream/internal/render"
	"github.com/sequencestream/video-stream/internal/scriptagents"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/wizard"
)

func wireWizard(t *testing.T, deps *Deps) {
	t.Helper()
	s, ok := deps.Store.(*store.SQLiteStore)
	if !ok {
		t.Fatal("want SQLiteStore")
	}
	cfg := config.Default().Compliance
	comp, err := compliance.New(compliance.Options{
		Store: s,
		Config: compliance.Config{
			RejectSimilarity: cfg.RejectSimilarity,
			PassSimilarity:   cfg.PassSimilarity,
			ReuseWindowDays:  cfg.ReuseWindowDays,
			MaxReuses:        cfg.MaxReuses,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deps.Wizard = wizard.New(wizard.Options{
		Store: s, Projects: s,
		Radar:      radar.New(radar.Options{Store: s}),
		Ideation:   ideation.New(ideation.Options{Store: s}),
		Script:     scriptagents.New(scriptagents.Options{Store: s, Termination: scriptagents.TerminationConfig{MaxRounds: 2}}),
		Hybrid:     hybrid.New(hybrid.Options{Store: s}),
		Compliance: comp,
		Render:     render.New(render.Options{Store: s, Artifacts: s, OutputDir: t.TempDir(), FFmpeg: render.StubFFmpeg{}}),
		Recompile:  recompile.New(recompile.Options{Cache: s, Runs: s}),
	})
}

func TestWizardCreateSession(t *testing.T) {
	deps := newDeps(t)
	wireWizard(t, &deps)
	handler := NewServer(deps).Handler()

	body := `{"operation_id":"00000000-0000-4000-8000-000000000001","topic":"test topic","category":"tech","accounts":[{"platform":"youtube","handle":"@a"}]}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/wizard/sessions", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	var sess wizard.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &sess); err != nil {
		t.Fatal(err)
	}
	if sess.CurrentStep != wizard.StepTopics || len(sess.State.TopicCards) < 3 {
		t.Fatalf("got step=%d topics=%d", sess.CurrentStep, len(sess.State.TopicCards))
	}
}

func TestWizardAdvanceReturnsStaleSession(t *testing.T) {
	deps := newDeps(t)
	wireWizard(t, &deps)
	handler := NewServer(deps).Handler()
	create := httptest.NewRecorder()
	handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/wizard/sessions", strings.NewReader(
		`{"operation_id":"00000000-0000-4000-8000-000000000101","topic":"t","category":"c","accounts":[]}`)))
	var sess wizard.Session
	if err := json.Unmarshal(create.Body.Bytes(), &sess); err != nil {
		t.Fatal(err)
	}
	firstBody := fmt.Sprintf(`{"operation_id":"00000000-0000-4000-8000-000000000102","expected_version":%d,"topic_card_id":%q}`,
		sess.Version, sess.State.TopicCards[0].ID)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/wizard/sessions/"+sess.ID+"/advance", strings.NewReader(firstBody)))
	if first.Code != http.StatusOK {
		t.Fatalf("first=%d %s", first.Code, first.Body.String())
	}
	staleBody := fmt.Sprintf(`{"operation_id":"00000000-0000-4000-8000-000000000103","expected_version":%d,"topic_card_id":%q}`,
		sess.Version, sess.State.TopicCards[0].ID)
	stale := httptest.NewRecorder()
	handler.ServeHTTP(stale, httptest.NewRequest(http.MethodPost, "/v1/wizard/sessions/"+sess.ID+"/advance", strings.NewReader(staleBody)))
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"code":"stale_session"`) || !strings.Contains(stale.Body.String(), `"session"`) {
		t.Fatalf("stale=%d %s", stale.Code, stale.Body.String())
	}
}
