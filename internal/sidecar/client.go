package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotImplemented reports that the sidecar acknowledged the call but the
// capability is still a placeholder. Callers must treat this as "not built yet",
// distinct from a transport or server failure.
var ErrNotImplemented = errors.New("sidecar capability not implemented")

// APIError is a structured non-2xx response from the sidecar.
type APIError struct {
	StatusCode int    `json:"-"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Capability string `json:"capability,omitempty"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("sidecar %d %s: %s", e.StatusCode, e.Code, e.Message)
}

// Unwrap lets errors.Is(err, ErrNotImplemented) succeed for 501 responses.
func (e *APIError) Unwrap() error {
	if e.StatusCode == http.StatusNotImplemented {
		return ErrNotImplemented
	}
	return nil
}

// Client calls the sidecar over loopback HTTP.
type Client struct {
	baseURL string
	httpc   *http.Client
}

// New returns a sidecar client. A zero timeout falls back to five seconds so a
// hung sidecar cannot stall the main service indefinitely.
func New(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	// The sidecar is a loopback address or a sibling container. Sending the
	// call through the operator's HTTP(S)_PROXY would turn a healthy sidecar
	// into a reported outage, so the transport opts out of proxy discovery.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil

	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpc:   &http.Client{Timeout: timeout, Transport: transport},
	}
}

// BaseURL returns the configured sidecar address.
func (c *Client) BaseURL() string { return c.baseURL }

// Health probes the sidecar's own health endpoint.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var out Health
	err := c.do(ctx, http.MethodGet, "/health", nil, &out)
	return out, err
}

// Transcribe requests word-level transcription. Returns ErrNotImplemented while
// the capability is a placeholder.
func (c *Client) Transcribe(ctx context.Context, req TranscribeRequest) (TranscribeResponse, error) {
	var out TranscribeResponse
	err := c.do(ctx, http.MethodPost, "/v1/audio/transcribe", req, &out)
	return out, err
}

// CreateDraft requests an editor draft project.
func (c *Client) CreateDraft(ctx context.Context, req DraftRequest) (DraftResponse, error) {
	var out DraftResponse
	err := c.do(ctx, http.MethodPost, "/v1/jianying/draft", req, &out)
	return out, err
}

// Automate requests a browser automation run.
func (c *Client) Automate(ctx context.Context, req AutomateRequest) (AutomateResponse, error) {
	var out AutomateResponse
	err := c.do(ctx, http.MethodPost, "/v1/browser/automate", req, &out)
	return out, err
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	endpoint, err := url.JoinPath(c.baseURL, path)
	if err != nil {
		return fmt.Errorf("build sidecar url for %s: %w", path, err)
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode sidecar request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("build sidecar request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("call sidecar %s: %w", path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read sidecar response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp.StatusCode, payload)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode sidecar response from %s: %w", path, err)
	}
	return nil
}

// decodeAPIError reads the structured error envelope, falling back to the raw
// body when the sidecar (or something in front of it) returns non-JSON.
func decodeAPIError(status int, payload []byte) error {
	var envelope struct {
		Detail APIError `json:"detail"`
	}
	if err := json.Unmarshal(payload, &envelope); err == nil && envelope.Detail.Code != "" {
		apiErr := envelope.Detail
		apiErr.StatusCode = status
		return &apiErr
	}

	var direct APIError
	if err := json.Unmarshal(payload, &direct); err == nil && direct.Code != "" {
		direct.StatusCode = status
		return &direct
	}

	message := strings.TrimSpace(string(payload))
	if message == "" {
		message = http.StatusText(status)
	}
	return &APIError{StatusCode: status, Code: "unexpected_response", Message: message}
}
