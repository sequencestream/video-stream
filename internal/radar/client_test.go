package radar_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sequencestream/video-stream/internal/credential"
	"github.com/sequencestream/video-stream/internal/radar"
)

func TestPlatformClientMaps429ToErrRateLimited(t *testing.T) {
	t.Setenv("VS_CREDENTIAL_PLATFORM_DOUYIN", "secret-token-1234567890")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(radar.PlatformAPIError{Code: "rate_limited", Message: "slow down"})
	}))
	t.Cleanup(srv.Close)

	chain, err := credential.Open(credential.Options{Backend: credential.BackendEnv})
	if err != nil {
		t.Fatalf("open credentials: %v", err)
	}

	client := radar.NewPlatformClient(radar.PlatformClientOptions{
		Platform:    "douyin",
		BaseURL:     srv.URL,
		Credentials: chain,
	})

	err = client.Do(context.Background(), http.MethodGet, "/v1/posts", nil, nil)
	if err == nil {
		t.Fatal("expected an error for 429")
	}
	if !errors.Is(err, radar.ErrRateLimited) {
		t.Fatalf("429 should unwrap to ErrRateLimited: %v", err)
	}
}
