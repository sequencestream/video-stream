package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/audio"
	"github.com/sequencestream/video-stream/internal/model"
)

func wireAudio(t *testing.T, deps *Deps) {
	t.Helper()
	deps.Audio = audio.New(audio.Options{
		OutputDir: t.TempDir(),
		TTS:       audio.StubTTS{MSPerWord: 250},
	})
}

func TestAudioSynthesizeWithoutEngineReturns503(t *testing.T) {
	handler := NewServer(newDeps(t)).Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/audio/synthesize",
		strings.NewReader(`{"project":{"id":"p1","segs":[{"seg_id":"s1","text":"hi","duration_budget_ms":{"min_ms":1800,"max_ms":2200},"emotion_tag":"neutral","breath":"none"}]}}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("synthesize = %d, want 503: %s", rec.Code, rec.Body)
	}
}

func TestAudioSynthesizeProducesOutput(t *testing.T) {
	deps := newDeps(t)
	wireAudio(t, &deps)
	handler := NewServer(deps).Handler()

	p := model.NewProject("proj-audio", "t", time.Now().UTC())
	p.Segs = []model.Seg{model.NewSeg("s1", "one two three four five six seven eight nine ten eleven twelve", 3000)}
	p.Seal()

	body := `{"project":` + mustJSON(t, p) + `,"platform":"youtube","mode":"soft"}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/audio/synthesize", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("synthesize = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"audio_uri"`) {
		t.Fatalf("missing audio_uri: %s", rec.Body)
	}
}

func TestAudioSynthesizeRejectsOverBudget(t *testing.T) {
	deps := newDeps(t)
	deps.Audio = audio.New(audio.Options{
		OutputDir: t.TempDir(),
		TTS:       audio.LongStubTTS{},
	})
	handler := NewServer(deps).Handler()

	p := model.NewProject("proj-bad", "t", time.Now().UTC())
	p.Segs = []model.Seg{model.NewSeg("s1", "way too long script", 2000)}
	p.Seal()

	body := `{"project":` + mustJSON(t, p) + `}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/audio/synthesize", strings.NewReader(body)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("synthesize = %d, want 422: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "需改字数") {
		t.Fatalf("want 需改字数 in body: %s", rec.Body)
	}
}

func TestAudioPlatformsListsDefaults(t *testing.T) {
	handler := NewServer(newDeps(t)).Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/audio/platforms", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("platforms = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"youtube"`) {
		t.Fatalf("missing youtube: %s", rec.Body)
	}
}
