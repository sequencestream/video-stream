package render

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// VideoGenInput is one seg's video model call. Resolution selects the tier;
// prompt/seed/ref come from SharedVisual and must not change between tiers.
type VideoGenInput struct {
	Resolution     Resolution
	RenderCacheKey string
	Prompt         string
	Seed           string
	RefURI         string
}

// VideoGenerator produces per-seg visual clips. The 1080p pass reuses stored
// shared context and must not invoke LLM-backed prompt generation.
type VideoGenerator interface {
	Generate(ctx context.Context, in VideoGenInput) (uri string, err error)
}

// StubVideoGenerator writes deterministic stub clip paths.
type StubVideoGenerator struct {
	OutputDir string
}

func (g StubVideoGenerator) Generate(_ context.Context, in VideoGenInput) (string, error) {
	w, h := in.Resolution.Dimensions()
	name := safeFilename(in.RenderCacheKey) + "_" + strconv.Itoa(w) + "x" + strconv.Itoa(h) + ".clip"
	path := filepath.Join(g.OutputDir, name)
	if err := writeStubFile(path, in.Seed); err != nil {
		return "", err
	}
	return path, nil
}

// CountingVideoGenerator wraps another generator for test assertions.
type CountingVideoGenerator struct {
	Inner VideoGenerator
	Calls int
}

func (c *CountingVideoGenerator) Generate(ctx context.Context, in VideoGenInput) (string, error) {
	c.Calls++
	return c.Inner.Generate(ctx, in)
}

func safeFilename(key string) string {
	return strings.NewReplacer(":", "_", "/", "_").Replace(key)
}

func writeStubFile(path, seed string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("stub-clip seed="+seed), 0o644)
}
