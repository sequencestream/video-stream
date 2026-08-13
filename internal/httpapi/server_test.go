package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/config"
	"github.com/sequencestream/video-stream/internal/credential"
	"github.com/sequencestream/video-stream/internal/queue"
	"github.com/sequencestream/video-stream/internal/recompile"
	"github.com/sequencestream/video-stream/internal/sidecar"
	"github.com/sequencestream/video-stream/internal/store"
)

const providerSecret = "sk-meta-test-9876543210-zyxwvut"

// TestMetaReportsCredentialPresenceWithoutTheKey is the acceptance check for
// the metadata endpoint: it is the one place a credential could plausibly be
// serialised out, so it must report only presence and origin.
func TestMetaReportsCredentialPresenceWithoutTheKey(t *testing.T) {
	t.Setenv("VS_CREDENTIAL_PROVIDER_OPENAI", providerSecret)

	body, raw := getMeta(t, newDeps(t))

	if strings.Contains(raw, providerSecret) {
		t.Fatalf("the meta response leaked the key: %s", raw)
	}

	openai := findProvider(t, body, "openai")
	if !openai.HasCredential {
		t.Error("openai should report a credential")
	}
	if openai.CredentialFrom != "env" {
		t.Errorf("credential_from = %q, want env", openai.CredentialFrom)
	}
	if openai.Protocol != config.ProtocolOpenAI {
		t.Errorf("protocol = %q, want %q", openai.Protocol, config.ProtocolOpenAI)
	}

	// The second provider has no key anywhere; absence must be visible rather
	// than indistinguishable from an unreported field.
	other := findProvider(t, body, "dashscope")
	if other.HasCredential {
		t.Error("dashscope should report no credential")
	}
	if other.CredentialFrom != "" {
		t.Errorf("credential_from = %q, want empty for a missing key", other.CredentialFrom)
	}
}

func TestRootIsUnroutedWithoutAWebUI(t *testing.T) {
	handler := NewServer(newDeps(t)).Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET / = %d, want 404 when no WebUI is wired in", rec.Code)
	}
}

func newDeps(t *testing.T) Deps {
	t.Helper()

	taskStore, err := store.OpenSQLite(t.TempDir() + "/tasks.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { taskStore.Close() })

	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{Name: "openai", BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini"},
		{Name: "dashscope", BaseURL: "https://example.invalid/v1", Model: "qwen-plus"},
	}

	// An env-only chain keeps the test away from the developer's keychain.
	chain, err := credential.Open(credential.Options{Backend: credential.BackendEnv})
	if err != nil {
		t.Fatalf("open credentials: %v", err)
	}

	return Deps{
		Config: cfg,
		Store:  taskStore,
		Queue:  queue.NewInProcess(queue.Options{Store: taskStore, Registry: queue.NewRegistry()}),
		// Unreachable on purpose: /readyz must still answer, and these tests
		// are about routing rather than the sidecar.
		Sidecar:     sidecar.New("http://127.0.0.1:1", time.Second),
		Credentials: chain,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:     "test",
	}
}

func getMeta(t *testing.T, deps Deps) (metaResponse, string) {
	t.Helper()

	rec := httptest.NewRecorder()
	NewServer(deps).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/meta", nil).WithContext(context.Background()))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/meta = %d, want 200", rec.Code)
	}

	var body metaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	return body, rec.Body.String()
}

func findProvider(t *testing.T, body metaResponse, name string) providerStatus {
	t.Helper()

	for _, p := range body.Providers {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("provider %q missing from the meta response", name)
	return providerStatus{}
}

func getRecompileReport(t *testing.T, deps Deps, query string) recompileReportResponse {
	t.Helper()

	rec := httptest.NewRecorder()
	NewServer(deps).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/recompile/report"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/recompile/report%s = %d, want 200: %s", query, rec.Code, rec.Body)
	}

	var body recompileReportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	return body
}

// recordRuns wires an engine over the deps' store and seeds it with runs whose
// invalidation rate is stated up front, so each test says what number it
// expects the route to publish.
func recordRuns(t *testing.T, deps *Deps, runs []store.RecompileRun) {
	t.Helper()

	s, ok := deps.Store.(*store.SQLiteStore)
	if !ok {
		t.Fatalf("deps.Store is %T, want *store.SQLiteStore", deps.Store)
	}
	for _, run := range runs {
		if err := s.RecordRun(context.Background(), run); err != nil {
			t.Fatalf("RecordRun %s: %v", run.ID, err)
		}
	}
	deps.Recompile = recompile.New(recompile.Options{Cache: s, Runs: s, Logger: deps.Logger})
}

// A daemon built without the engine still has to answer this route, and the
// answer must not read as success. Zeroed counters with a defaulted "viable"
// verdict would say the bet is paying off when nothing has been measured.
func TestRecompileReportWithoutAnEngineClaimsNothing(t *testing.T) {
	got := getRecompileReport(t, newDeps(t), "")

	if got.Verdict != recompile.VerdictInsufficientData {
		t.Errorf("verdict = %q, want insufficient_data", got.Verdict)
	}
	if got.Runs != 0 || got.TotalSegs != 0 {
		t.Errorf("got %+v, want empty counters", got)
	}
	// The thresholds are what make the counters readable, so they ship even
	// when there is nothing to read.
	if got.ScrapThresholdPct != recompile.ScrapThresholdPercent || got.MinRunsForVerdict != recompile.MinRunsForVerdict {
		t.Errorf("got %+v, want the thresholds populated", got)
	}
}

func TestRecompileReportPublishesTheRatesAndTheVerdict(t *testing.T) {
	deps := newDeps(t)

	// Twenty runs is exactly MinRunsForVerdict, so a verdict is due: 20 segs
	// invalidated out of 200 is 10%, comfortably under the scrap threshold.
	var runs []store.RecompileRun
	for i := range 20 {
		runs = append(runs, store.RecompileRun{
			ID: "run-" + strconv.Itoa(i), ProjectID: "p1",
			PlannedAt: time.UnixMilli(int64(i + 1)),
			TotalSegs: 10, InvalidatedSegs: 1, CostSavedMicros: 1000,
			CacheHits: 9, RegeneratedSegs: 1, ElapsedMS: 50, ActualCostMicros: 25,
		})
	}
	recordRuns(t, &deps, runs)

	got := getRecompileReport(t, deps, "")

	if got.Runs != 20 || got.TotalSegs != 200 || got.InvalidatedSegs != 20 || got.ReusedSegs != 180 {
		t.Fatalf("counters = %+v", got)
	}
	if got.InvalidationRate != 0.1 || got.ReuseRate != 0.9 {
		t.Errorf("rates = %v / %v, want 0.1 / 0.9", got.InvalidationRate, got.ReuseRate)
	}
	if got.CostSavedMicros != 20_000 {
		t.Errorf("cost_saved_micros = %d, want 20000", got.CostSavedMicros)
	}
	if got.CacheHits != 180 || got.RegeneratedSegs != 20 || got.ElapsedMS != 1000 || got.ActualCostMicros != 500 {
		t.Errorf("execution metrics = %+v", got)
	}
	if got.Verdict != recompile.VerdictViable {
		t.Errorf("verdict = %q, want viable", got.Verdict)
	}
}

// The verdict has to be able to say no. If a scrap-level rate came back as
// "viable" the report would be decoration rather than a measurement.
func TestRecompileReportReportsAScrapVerdictAndTheBoundaries(t *testing.T) {
	deps := newDeps(t)

	var runs []store.RecompileRun
	for i := range 20 {
		runs = append(runs, store.RecompileRun{
			ID: "run-" + strconv.Itoa(i), ProjectID: "p1",
			PlannedAt: time.UnixMilli(int64(i + 1)),
			TotalSegs: 10, InvalidatedSegs: 10,
			FullRerun: true, Boundary: string(recompile.BoundaryStyleAnchor),
		})
	}
	recordRuns(t, &deps, runs)

	got := getRecompileReport(t, deps, "")

	if got.Verdict != recompile.VerdictScrap {
		t.Fatalf("verdict = %q, want scrap", got.Verdict)
	}
	if got.FullRerunRuns != 20 || got.FullRerunRate != 1 {
		t.Errorf("full rerun = %d / %v, want 20 / 1", got.FullRerunRuns, got.FullRerunRate)
	}
	// Knowing which boundary fired is the actionable half of a scrap verdict.
	if got.ByBoundary[recompile.BoundaryStyleAnchor] != 20 {
		t.Errorf("by_boundary = %v, want 20 style_anchor crossings", got.ByBoundary)
	}
}

func TestRecompileReportFiltersByProject(t *testing.T) {
	deps := newDeps(t)
	recordRuns(t, &deps, []store.RecompileRun{
		{ID: "run-1", ProjectID: "p1", PlannedAt: time.UnixMilli(1), TotalSegs: 10, InvalidatedSegs: 1},
		{ID: "run-2", ProjectID: "p2", PlannedAt: time.UnixMilli(2), TotalSegs: 4, InvalidatedSegs: 4},
	})

	if got := getRecompileReport(t, deps, "?project=p1"); got.Runs != 1 || got.TotalSegs != 10 {
		t.Errorf("?project=p1 gave %+v, want p1's single run", got)
	}
	if got := getRecompileReport(t, deps, ""); got.Runs != 2 || got.TotalSegs != 14 {
		t.Errorf("no filter gave %+v, want both runs", got)
	}
}
