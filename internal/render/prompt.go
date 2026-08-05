package render

import (
	"context"

	"github.com/sequencestream/video-stream/internal/model"
)

// PromptGenerator uses LLM to expand visual prompts during the first (720p) pass.
// The 1080p pass must not call this interface.
type PromptGenerator interface {
	Enrich(ctx context.Context, project model.Project, base []SharedVisual) ([]SharedVisual, error)
}

// NopPromptGenerator returns the base context unchanged.
type NopPromptGenerator struct{}

func (NopPromptGenerator) Enrich(_ context.Context, _ model.Project, base []SharedVisual) ([]SharedVisual, error) {
	out := make([]SharedVisual, len(base))
	copy(out, base)
	return out, nil
}

// CountingPromptGenerator wraps a generator for telemetry tests.
type CountingPromptGenerator struct {
	Inner PromptGenerator
	Calls int
}

func (c *CountingPromptGenerator) Enrich(ctx context.Context, project model.Project, base []SharedVisual) ([]SharedVisual, error) {
	c.Calls++
	if c.Inner == nil {
		return NopPromptGenerator{}.Enrich(ctx, project, base)
	}
	return c.Inner.Enrich(ctx, project, base)
}
