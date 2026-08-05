package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/config"
	"github.com/sequencestream/video-stream/internal/credential"
	"github.com/sequencestream/video-stream/internal/queue"
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

// TestAPIRoutesWinOverTheWebUI guards the routing order. The UI is registered
// on "/", which in net/http is the catch-all, so a mistake here would silently
// serve HTML where the CLI expects JSON.
func TestAPIRoutesWinOverTheWebUI(t *testing.T) {
	deps := newDeps(t)
	deps.WebUI = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "<html>webui</html>")
	})
	handler := NewServer(deps).Handler()

	for _, path := range []string{"/healthz", "/readyz", "/v1/meta", "/v1/tasks"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if strings.Contains(rec.Body.String(), "webui") {
			t.Errorf("GET %s was served by the WebUI handler", path)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wizard/1/", nil))
	if !strings.Contains(rec.Body.String(), "webui") {
		t.Errorf("GET /wizard/1/ should reach the WebUI handler, got %q", rec.Body.String())
	}
}

// TestRootIsUnroutedWithoutAWebUI keeps the API-only build honest: no handler,
// no route.
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
