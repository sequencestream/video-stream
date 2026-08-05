package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/config"
	"github.com/sequencestream/video-stream/internal/credential"
	"github.com/sequencestream/video-stream/internal/provider"
	"github.com/sequencestream/video-stream/internal/redact"
)

// providerSecret avoids the "sk-" shape the repository's secret scanner flags.
const providerSecret = "provider-test-credential-a417"

// fakeBackend is a minimal OpenAI-compatible endpoint. It records the
// Authorization header so the test can prove the credential actually reached
// the wire, which is the whole point of the credential store.
type fakeBackend struct {
	*httptest.Server
	authHeader string
	body       map[string]any
}

func newFakeBackend(t *testing.T, status int, response any) *fakeBackend {
	t.Helper()

	backend := &fakeBackend{}
	backend.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend.authHeader = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&backend.body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(backend.Close)

	return backend
}

func newRegistry(t *testing.T, baseURL string, seed map[string]string) *provider.Registry {
	t.Helper()

	return provider.New(provider.Options{
		Providers: []config.Provider{
			{Name: "openai", BaseURL: baseURL, Model: "gpt-4o-mini"},
		},
		Credentials: credential.NewMemoryStore(seed),
	})
}

func successResponse() map[string]any {
	return map[string]any{
		"id":     "chatcmpl-test",
		"object": "chat.completion",
		"model":  "gpt-4o-mini",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": "hello from the fake backend"},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
	}
}

func TestCompleteSendsTheStoredCredential(t *testing.T) {
	backend := newFakeBackend(t, http.StatusOK, successResponse())
	registry := newRegistry(t, backend.URL, map[string]string{
		credential.ProviderKey("openai"): providerSecret,
	})

	got, err := registry.Complete(context.Background(), provider.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	// The credential store is only worth having if the key it holds is the one
	// that reaches the vendor.
	if want := "Bearer " + providerSecret; backend.authHeader != want {
		t.Fatalf("Authorization header is %q, want %q", backend.authHeader, want)
	}
	if got.Text != "hello from the fake backend" {
		t.Errorf("text is %q", got.Text)
	}
	if got.FinishReason != "stop" {
		t.Errorf("finish reason is %q", got.FinishReason)
	}
	if got.TotalTokens != 18 {
		t.Errorf("total tokens is %d, want 18", got.TotalTokens)
	}
	if got.Provider != "openai" {
		t.Errorf("provider is %q", got.Provider)
	}
}

// A missing credential must be actionable: the user needs to be told to store
// one, not left guessing at a network problem.
func TestCompleteWithoutCredentialIsIdentifiable(t *testing.T) {
	backend := newFakeBackend(t, http.StatusOK, successResponse())
	registry := newRegistry(t, backend.URL, nil)

	_, err := registry.Complete(context.Background(), provider.Request{Prompt: "hi"})
	if !errors.Is(err, provider.ErrNoCredential) {
		t.Fatalf("got %v, want ErrNoCredential", err)
	}
	if !strings.Contains(err.Error(), "vs credential set openai") {
		t.Errorf("the error should tell the user how to fix it, got %q", err)
	}
}

// An upstream failure must never carry the key back to the caller, because the
// error goes straight into a task receipt and the logs.
func TestCompleteErrorsDoNotLeakTheCredential(t *testing.T) {
	backend := newFakeBackend(t, http.StatusUnauthorized, map[string]any{
		"error": map[string]any{
			// Vendors do sometimes echo the presented key back in an auth
			// error, so the redaction path has to handle exactly this.
			"message": "Incorrect API key provided: " + providerSecret,
			"type":    "invalid_request_error",
		},
	})
	registry := newRegistry(t, backend.URL, map[string]string{
		credential.ProviderKey("openai"): providerSecret,
	})

	_, err := registry.Complete(context.Background(), provider.Request{Prompt: "hi"})
	if err == nil {
		t.Fatal("expected the upstream 401 to surface as an error")
	}
	if strings.Contains(err.Error(), providerSecret) {
		t.Fatalf("the credential leaked into the error: %q", err)
	}
	if !strings.Contains(err.Error(), redact.Placeholder) {
		t.Errorf("expected the error to show a redaction placeholder, got %q", err)
	}
}

func TestCompleteRejectsUnknownProvider(t *testing.T) {
	backend := newFakeBackend(t, http.StatusOK, successResponse())
	registry := newRegistry(t, backend.URL, nil)

	_, err := registry.Complete(context.Background(), provider.Request{Provider: "nonexistent", Prompt: "hi"})
	if !errors.Is(err, provider.ErrUnknownProvider) {
		t.Fatalf("got %v, want ErrUnknownProvider", err)
	}
}

func TestCompleteAppliesSystemPromptAndTokenCap(t *testing.T) {
	backend := newFakeBackend(t, http.StatusOK, successResponse())
	registry := newRegistry(t, backend.URL, map[string]string{
		credential.ProviderKey("openai"): providerSecret,
	})

	_, err := registry.Complete(context.Background(), provider.Request{
		System:    "be terse",
		Prompt:    "hi",
		MaxTokens: 256,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	messages, ok := backend.body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("expected a system and a user message, got %v", backend.body["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("first message role is %v, want system", first["role"])
	}

	// The deprecated max_tokens is rejected by reasoning models, so the
	// request must use max_completion_tokens.
	if _, unwanted := backend.body["max_tokens"]; unwanted {
		t.Error("request used the deprecated max_tokens field")
	}
	if backend.body["max_completion_tokens"] != float64(256) {
		t.Errorf("max_completion_tokens is %v, want 256", backend.body["max_completion_tokens"])
	}
}

// An empty provider name uses the first configured provider, which is what lets
// a caller omit it entirely.
func TestCompleteDefaultsToTheFirstProvider(t *testing.T) {
	backend := newFakeBackend(t, http.StatusOK, successResponse())
	registry := provider.New(provider.Options{
		Providers: []config.Provider{
			{Name: "primary", BaseURL: backend.URL, Model: "gpt-4o-mini"},
			{Name: "secondary", BaseURL: backend.URL, Model: "gpt-4o"},
		},
		Credentials: credential.NewMemoryStore(map[string]string{
			credential.ProviderKey("primary"): providerSecret,
		}),
	})

	got, err := registry.Complete(context.Background(), provider.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got.Provider != "primary" {
		t.Fatalf("got provider %q, want primary", got.Provider)
	}
}

func TestCompleteWithNoProvidersConfigured(t *testing.T) {
	registry := provider.New(provider.Options{Credentials: credential.NewMemoryStore(nil)})

	if _, err := registry.Complete(context.Background(), provider.Request{Prompt: "hi"}); err == nil {
		t.Fatal("expected an error when no providers are configured")
	}
}
