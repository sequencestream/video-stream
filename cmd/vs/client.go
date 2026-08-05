package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// task mirrors store.Task on the wire. The CLI keeps its own copy so it stays a
// pure API client and does not link the server's persistence packages.
type task struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Status    string         `json:"status"`
	Payload   map[string]any `json:"payload,omitempty"`
	Result    map[string]any `json:"result,omitempty"`
	Error     string         `json:"error,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func (t task) terminal() bool {
	return t.Status == "succeeded" || t.Status == "failed"
}

type createTaskRequest struct {
	Type    string         `json:"type"`
	Title   string         `json:"title"`
	Payload map[string]any `json:"payload,omitempty"`
}

type client struct {
	baseURL string
	httpc   *http.Client
}

func newClient(baseURL string) *client {
	return &client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpc:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *client) CreateTask(ctx context.Context, req createTaskRequest) (task, error) {
	var out task
	err := c.do(ctx, http.MethodPost, "/v1/tasks", req, &out)
	return out, err
}

func (c *client) GetTask(ctx context.Context, id string) (task, error) {
	var out task
	err := c.do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(id), nil, &out)
	return out, err
}

// WaitForTask polls until the task settles. Polling is adequate for the MVP;
// when the WebUI needs live updates this becomes the place to add streaming.
func (c *client) WaitForTask(ctx context.Context, id string, timeout time.Duration) (task, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		current, err := c.GetTask(ctx, id)
		if err != nil {
			return task{}, err
		}
		if current.terminal() {
			return current, nil
		}
		if time.Now().After(deadline) {
			return current, fmt.Errorf("task %s still %s after %s", id, current.Status, timeout)
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return task{}, ctx.Err()
		}
	}
}

func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the main service at %s (is vsd running?): %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(payload, &apiErr) == nil && apiErr.Code != "" {
			return fmt.Errorf("%s: %s", apiErr.Code, apiErr.Message)
		}
		return fmt.Errorf("unexpected response %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(payload, out)
}
