// Package tasks holds the task handlers registered with the queue.
//
// The MVP ships one real handler (echo, which proves the CLI -> queue -> store
// round trip) and two placeholders. The placeholders fail loudly instead of
// returning fabricated success, so a later intent can tell at a glance whether
// the real implementation has landed.
package tasks

import (
	"context"
	"errors"
	"fmt"

	"github.com/sequencestream/video-stream/internal/queue"
	"github.com/sequencestream/video-stream/internal/sidecar"
	"github.com/sequencestream/video-stream/internal/store"
)

// Task type identifiers.
const (
	TypeEcho       = "echo"
	TypeRender     = "render"
	TypeTranscribe = "transcribe"
)

// Register wires every MVP handler into the registry.
func Register(r *queue.Registry, sc *sidecar.Client) {
	r.Register(TypeEcho, Echo)
	r.Register(TypeRender, Render)
	r.Register(TypeTranscribe, Transcribe(sc))
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
