package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/radar"
	"github.com/sequencestream/video-stream/internal/store"
)

func wireRadar(t *testing.T, deps *Deps) {
	t.Helper()
	s, ok := deps.Store.(*store.SQLiteStore)
	if !ok {
		t.Fatalf("deps.Store is %T, want *store.SQLiteStore", deps.Store)
	}
	deps.Radar = radar.New(radar.Options{Store: s, Poller: radar.NewPoller(600, 1, deps.Logger)})
}

func TestRadarAccountsWithoutAnEngineReturnsAnEmptyList(t *testing.T) {
	rec := httptest.NewRecorder()
	NewServer(newDeps(t)).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/radar/accounts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/radar/accounts = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("got %s, want an empty items array", rec.Body.String())
	}
}

func TestRadarImportAndListAccounts(t *testing.T) {
	deps := newDeps(t)
	wireRadar(t, &deps)

	body := `{"platform":"douyin","handle":"cook_daily","category":"cooking","followers":12000}`
	rec := httptest.NewRecorder()
	NewServer(deps).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/radar/accounts", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /v1/radar/accounts = %d: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	NewServer(deps).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/radar/accounts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/radar/accounts = %d", rec.Code)
	}

	var listed listRadarAccountsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].Handle != "cook_daily" {
		t.Fatalf("got %+v, want one douyin account", listed.Items)
	}
}

func TestRadarSignalsWithoutAnEngineReturnsAnEmptyList(t *testing.T) {
	rec := httptest.NewRecorder()
	NewServer(newDeps(t)).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/radar/signals", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/radar/signals = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("got %s, want an empty items array", rec.Body.String())
	}
}

func TestRadarIngestAndSignals(t *testing.T) {
	deps := newDeps(t)
	wireRadar(t, &deps)
	handler := NewServer(deps).Handler()

	importRec := httptest.NewRecorder()
	handler.ServeHTTP(importRec, httptest.NewRequest(http.MethodPost, "/v1/radar/accounts",
		strings.NewReader(`{"platform":"douyin","handle":"cook","category":"cooking","followers":2000}`)))
	if importRec.Code != http.StatusCreated {
		t.Fatalf("import = %d: %s", importRec.Code, importRec.Body)
	}

	var account store.RadarAccount
	if err := json.Unmarshal(importRec.Body.Bytes(), &account); err != nil {
		t.Fatalf("decode account: %v", err)
	}

	published := time.Now().UTC().Add(-7 * 24 * time.Hour)
	observed := published.Add(240 * time.Hour)
	readings := radarReadingsForHotFixture(account.ID, published, observed)

	payload, err := json.Marshal(ingestRadarRequest{Readings: readings})
	if err != nil {
		t.Fatalf("marshal ingest: %v", err)
	}
	ingestRec := httptest.NewRecorder()
	handler.ServeHTTP(ingestRec, httptest.NewRequest(http.MethodPost, "/v1/radar/ingest", bytes.NewReader(payload)))
	if ingestRec.Code != http.StatusOK {
		t.Fatalf("ingest = %d: %s", ingestRec.Code, ingestRec.Body)
	}

	sigRec := httptest.NewRecorder()
	handler.ServeHTTP(sigRec, httptest.NewRequest(http.MethodGet, "/v1/radar/signals?hot=true", nil).WithContext(context.Background()))
	if sigRec.Code != http.StatusOK {
		t.Fatalf("signals = %d: %s", sigRec.Code, sigRec.Body)
	}

	var signals radarSignalsResponse
	if err := json.Unmarshal(sigRec.Body.Bytes(), &signals); err != nil {
		t.Fatalf("decode signals: %v", err)
	}
	if len(signals.Items) == 0 {
		t.Fatal("expected at least one hot signal")
	}
	if signals.Items[0].PostID != "small" {
		t.Fatalf("top post_id = %q, want small", signals.Items[0].PostID)
	}
}

// radarReadingsForHotFixture mirrors the acceptance fixture in residual_test:
// ten ordinary cooking posts plus one small-account post with an anomalous save rate.
func radarReadingsForHotFixture(accountID string, published, observed time.Time) []radar.Reading {
	const (
		intercept = 2.0
		slope     = 0.8
		saveRate  = 0.05
		tauHours  = 48.0
	)
	maturity := math.Max(1-math.Exp(-observed.Sub(published).Hours()/tauHours), 0.05)

	followers := []int64{2_000, 5_000, 12_000, 30_000, 80_000, 150_000, 400_000, 900_000, 1_500_000, 2_000_000}
	jitter := []float64{0.12, -0.09, 0.05, -0.14, 0.08, -0.06, 0.11, -0.10, 0.04, -0.07}
	saveJitter := []float64{0.004, -0.003, 0.002, -0.005, 0.003, -0.002, 0.004, -0.004, 0.001, -0.003}

	readings := make([]radar.Reading, 0, 11)
	for i, f := range followers {
		matured := math.Exp(intercept+slope*math.Log1p(float64(f))+jitter[i]) - 1
		views := int64(matured * maturity)
		rate := saveRate + saveJitter[i]
		readings = append(readings, radar.Reading{
			AccountID: accountID, PostID: string(rune('a' + i)),
			PublishedAt: published, ObservedAt: observed,
			Views: views, Saves: int64(float64(views) * rate),
		})
	}

	hotViews := int64((math.Exp(intercept+slope*math.Log1p(2000)) - 1) * maturity)
	readings = append(readings, radar.Reading{
		AccountID: accountID, PostID: "small",
		PublishedAt: published, ObservedAt: observed,
		Views: hotViews, Saves: int64(float64(hotViews) * 0.30),
	})
	return readings
}

func TestAPIRoutesWinOverTheWebUIIncludesRadar(t *testing.T) {
	deps := newDeps(t)
	deps.WebUI = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("<html>webui</html>"))
	})
	handler := NewServer(deps).Handler()

	for _, path := range []string{"/v1/radar/accounts", "/v1/radar/signals"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if strings.Contains(rec.Body.String(), "webui") {
			t.Errorf("GET %s was served by the WebUI handler", path)
		}
	}
}
