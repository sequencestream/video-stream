// Package tasks holds the task handlers registered with the queue.
//
// Placeholders fail loudly instead of returning fabricated success, so a later
// intent can tell at a glance whether the real implementation has landed.
package tasks

import (
	"context"
	"errors"
	"fmt"

	"github.com/sequencestream/video-stream/internal/provider"
	"github.com/sequencestream/video-stream/internal/queue"
	"github.com/sequencestream/video-stream/internal/sidecar"
	"github.com/sequencestream/video-stream/internal/store"
)

// Task type identifiers.
const (
	TypeEcho       = "echo"
	TypeChat       = "chat"
	TypeRender     = "render"
	TypeTranscribe = "transcribe"
)

// Deps are the collaborators handlers need.
type Deps struct {
	Sidecar   *sidecar.Client
	Providers *provider.Registry
}

// Register wires every handler into the registry.
func Register(r *queue.Registry, deps Deps) {
	r.Register(TypeEcho, Echo)
	r.Register(TypeChat, Chat(deps.Providers))
	r.Register(TypeRender, Render)
	r.Register(TypeTranscribe, Transcribe(deps.Sidecar))
}

// Echo is the fake task used to verify the pipeline end to end: it returns the
// submitted payload unchanged.
func Echo(_ context.Context, t store.Task) (map[string]any, error) {
	return map[string]any{
		"echo":    t.Payload,
		"task_id": t.ID,
		"message": fmt.Sprintf("echo task %q completed", t.Title),
	}, nil
}

// Chat calls a model provider. It is the first handler to use a real
// credential, which makes it the end-to-end proof of the credential path:
// fetch the key from the OS keychain or vault, call the vendor, and write a
// receipt that contains no secret.
//
// A missing credential fails the task with instructions rather than returning
// fabricated text, following the same honesty rule as the placeholders below.
func Chat(providers *provider.Registry) queue.Handler {
	return func(ctx context.Context, t store.Task) (map[string]any, error) {
		if providers == nil {
			return nil, errors.New("no model providers are configured")
		}

		prompt, _ := t.Payload["prompt"].(string)
		if prompt == "" {
			return nil, errors.New("chat task requires a non-empty \"prompt\" in its payload")
		}

		name, _ := t.Payload["provider"].(string)
		model, _ := t.Payload["model"].(string)
		system, _ := t.Payload["system"].(string)

		// JSON numbers decode to float64, so an int assertion would silently
		// drop the caller's cap.
		maxTokens, _ := t.Payload["max_tokens"].(float64)

		completion, err := providers.Complete(ctx, provider.Request{
			Provider:  name,
			Model:     model,
			System:    system,
			Prompt:    prompt,
			MaxTokens: int(maxTokens),
		})
		if err != nil {
			return nil, err
		}

		return map[string]any{
			"provider":          completion.Provider,
			"model":             completion.Model,
			"text":              completion.Text,
			"finish_reason":     completion.FinishReason,
			"prompt_tokens":     completion.PromptTokens,
			"completion_tokens": completion.CompletionTokens,
			"total_tokens":      completion.TotalTokens,
		}, nil
	}
}

// Render is a placeholder for the render pipeline. It fails rather than
// pretending to have produced a video.
func Render(context.Context, store.Task) (map[string]any, error) {
	return nil, errors.New("render pipeline is not implemented yet; this scaffold only reserves the task type")
}

// Transcribe forwards to the sidecar. In the MVP the sidecar answers 501, which
// this handler surfaces verbatim — that round trip is what proves the main
// service can reach the sidecar's placeholder implementation.
func Transcribe(sc *sidecar.Client) queue.Handler {
	return func(ctx context.Context, t store.Task) (map[string]any, error) {
		audioPath, _ := t.Payload["audio_path"].(string)

		resp, err := sc.Transcribe(ctx, sidecar.TranscribeRequest{AudioPath: audioPath})
		if err != nil {
			if errors.Is(err, sidecar.ErrNotImplemented) {
				return nil, fmt.Errorf("sidecar reached but transcription is a placeholder: %w", err)
			}
			return nil, err
		}
		return map[string]any{"text": resp.Text, "word_count": len(resp.Words)}, nil
	}
}
