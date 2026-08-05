package radar

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

	"github.com/sequencestream/video-stream/internal/credential"
	"github.com/sequencestream/video-stream/internal/redact"
)

// PlatformAPIError is a structured non-2xx response from a platform API.
type PlatformAPIError struct {
	StatusCode int    `json:"-"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

func (e *PlatformAPIError) Error() string {
	return fmt.Sprintf("platform %d %s: %s", e.StatusCode, e.Code, e.Message)
}

// Unwrap lets errors.Is(err, ErrRateLimited) succeed for 429 responses.
func (e *PlatformAPIError) Unwrap() error {
	if e.StatusCode == http.StatusTooManyRequests {
		return ErrRateLimited
	}
	return nil
}

// PlatformClient calls a platform's official HTTP API.
//
// No platform source ships with this repository; the client exists so that a
// later official API adapter can share one do() implementation rather than
// re-copy the sidecar client's error handling and token hygiene.
type PlatformClient struct {
	platform    string
	baseURL     string
	httpc       *http.Client
	credentials credential.Store
}

// PlatformClientOptions configures a PlatformClient.
type PlatformClientOptions struct {
	// Platform is the identifier that matches Account.Platform and
	// credential.PlatformKey.
	Platform string
	// BaseURL is the platform API root. Trailing slashes are stripped.
	BaseURL string
	// Credentials supplies the platform token at call time. Nil makes every
	// request fail with an error rather than proceed without auth.
	Credentials credential.Store
	// Timeout bounds one request. Zero means thirty seconds.
	Timeout time.Duration
}

// NewPlatformClient builds a client for one platform.
func NewPlatformClient(opts PlatformClientOptions) *PlatformClient {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &PlatformClient{
		platform:    strings.ToLower(strings.TrimSpace(opts.Platform)),
		baseURL:     strings.TrimRight(opts.BaseURL, "/"),
		httpc:       &http.Client{Timeout: timeout},
		credentials: opts.Credentials,
	}
}

// Do performs one authenticated request and decodes a 2xx JSON body into out.
func (c *PlatformClient) Do(ctx context.Context, method, path string, body, out any) error {
	token, err := c.token(ctx)
	if err != nil {
		return err
	}
	redact.Register(token)

	endpoint, err := url.JoinPath(c.baseURL, path)
	if err != nil {
		return redact.Error(fmt.Errorf("build platform url for %s: %w", path, err))
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return redact.Error(fmt.Errorf("encode platform request: %w", err))
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return redact.Error(fmt.Errorf("build platform request: %w", err))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return redact.Error(fmt.Errorf("call platform %s: %w", path, err))
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return redact.Error(fmt.Errorf("read platform response: %w", err))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return redact.Error(decodePlatformAPIError(resp.StatusCode, payload))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return redact.Error(fmt.Errorf("decode platform response from %s: %w", path, err))
	}
	return nil
}

func (c *PlatformClient) token(ctx context.Context) (string, error) {
	if c.credentials == nil {
		return "", errors.New("radar platform client has no credential store")
	}
	token, err := c.credentials.Get(ctx, credential.PlatformKey(c.platform))
	if errors.Is(err, credential.ErrNotFound) {
		return "", fmt.Errorf("no platform token for %q: %w", c.platform, err)
	}
	if err != nil {
		return "", fmt.Errorf("fetch platform token for %q: %w", c.platform, err)
	}
	return token, nil
}

func decodePlatformAPIError(status int, payload []byte) error {
	var direct PlatformAPIError
	if err := json.Unmarshal(payload, &direct); err == nil && direct.Code != "" {
		direct.StatusCode = status
		return &direct
	}

	message := strings.TrimSpace(string(payload))
	if message == "" {
		message = http.StatusText(status)
	}
	return &PlatformAPIError{StatusCode: status, Code: "unexpected_response", Message: message}
}
