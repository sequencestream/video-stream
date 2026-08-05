package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// FFmpeg muxes staged outputs into the final MP4. Production uses real ffmpeg;
// tests use StubFFmpeg for speed.
type FFmpeg interface {
	Mux(ctx context.Context, outputPath string, stageFiles []string) error
}

// StubFFmpeg writes a placeholder MP4 marker file without invoking ffmpeg.
type StubFFmpeg struct{}

func (StubFFmpeg) Mux(_ context.Context, outputPath string, stageFiles []string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("stub-mp4 stages=%d", len(stageFiles))
	return os.WriteFile(outputPath, []byte(body), 0o644)
}
