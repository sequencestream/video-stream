package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FFmpeg muxes staged outputs into the final MP4.
type FFmpeg interface {
	Mux(ctx context.Context, outputPath string, stageFiles []string) error
}

// ExecFFmpeg invokes the local ffmpeg binary. Binary defaults to "ffmpeg" and
// may be an absolute path when the daemon runs with a restricted PATH.
//
// Visual inputs are concatenated in the order supplied. The last audio input
// wins because the render pipeline appends normalized and mixed tracks after
// their source tracks. A WebVTT or SRT input is embedded as a mov_text stream.
type ExecFFmpeg struct {
	Binary string
}

// Mux validates the staged artifacts, invokes ffmpeg, and atomically publishes
// the completed MP4. A failed or cancelled process never leaves a partial file
// at outputPath.
func (f ExecFFmpeg) Mux(ctx context.Context, outputPath string, stageFiles []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	inputs, err := classifyFFmpegInputs(stageFiles)
	if err != nil {
		return err
	}
	if len(inputs.videos) == 0 {
		return errors.New("ffmpeg mux requires at least one video input")
	}
	if strings.TrimSpace(outputPath) == "" {
		return errors.New("ffmpeg output path is required")
	}

	outDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create ffmpeg output directory: %w", err)
	}
	tmp, err := os.CreateTemp(outDir, ".ffmpeg-*.mp4")
	if err != nil {
		return fmt.Errorf("create temporary ffmpeg output: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temporary ffmpeg output: %w", err)
	}
	defer os.Remove(tmpPath)

	args := buildFFmpegArgs(inputs, tmpPath)
	binary := strings.TrimSpace(f.Binary)
	if binary == "" {
		binary = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("ffmpeg interrupted: %w", ctxErr)
		}
		message := strings.TrimSpace(stderr.String())
		if len(message) > 8*1024 {
			message = message[len(message)-8*1024:]
		}
		if message == "" {
			return fmt.Errorf("run ffmpeg: %w", err)
		}
		return fmt.Errorf("run ffmpeg: %w: %s", err, message)
	}

	info, err := os.Stat(tmpPath)
	if err != nil {
		return fmt.Errorf("stat ffmpeg output: %w", err)
	}
	if info.Size() == 0 {
		return errors.New("ffmpeg produced an empty output")
	}
	if err := os.Rename(tmpPath, outputPath); err != nil {
		return fmt.Errorf("publish ffmpeg output: %w", err)
	}
	return nil
}

type ffmpegInputs struct {
	videos   []string
	audio    string
	subtitle string
}

func classifyFFmpegInputs(stageFiles []string) (ffmpegInputs, error) {
	var result ffmpegInputs
	seen := make(map[string]struct{}, len(stageFiles))
	for _, path := range stageFiles {
		path = strings.TrimSpace(path)
		if path == "" {
			return ffmpegInputs{}, errors.New("ffmpeg input path is empty")
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		info, err := os.Stat(path)
		if err != nil {
			return ffmpegInputs{}, fmt.Errorf("stat ffmpeg input %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return ffmpegInputs{}, fmt.Errorf("ffmpeg input %s is not a regular file", path)
		}

		switch strings.ToLower(filepath.Ext(path)) {
		case ".mp4", ".mov", ".mkv", ".webm", ".m4v", ".avi":
			result.videos = append(result.videos, path)
		case ".wav", ".mp3", ".m4a", ".aac", ".flac", ".ogg", ".opus":
			result.audio = path
		case ".vtt", ".srt":
			result.subtitle = path
		default:
			return ffmpegInputs{}, fmt.Errorf("unsupported ffmpeg input %s", path)
		}
	}
	return result, nil
}

func buildFFmpegArgs(inputs ffmpegInputs, outputPath string) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}
	for _, path := range inputs.videos {
		args = append(args, "-i", path)
	}
	audioIndex := -1
	if inputs.audio != "" {
		audioIndex = len(inputs.videos)
		args = append(args, "-i", inputs.audio)
	}
	subtitleIndex := -1
	if inputs.subtitle != "" {
		subtitleIndex = len(inputs.videos)
		if audioIndex >= 0 {
			subtitleIndex++
		}
		args = append(args, "-i", inputs.subtitle)
	}

	if len(inputs.videos) == 1 {
		args = append(args, "-map", "0:v:0")
	} else {
		var filter strings.Builder
		for i := range inputs.videos {
			fmt.Fprintf(&filter, "[%d:v:0]", i)
		}
		fmt.Fprintf(&filter, "concat=n=%d:v=1:a=0[vout]", len(inputs.videos))
		args = append(args, "-filter_complex", filter.String(), "-map", "[vout]")
	}
	if audioIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("%d:a:0", audioIndex), "-c:a", "aac", "-b:a", "192k")
	}
	if subtitleIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("%d:0", subtitleIndex), "-c:s", "mov_text", "-metadata:s:s:0", "language=und")
	}
	args = append(args,
		"-c:v", "libx264", "-preset", "medium", "-crf", "18",
		"-pix_fmt", "yuv420p", "-movflags", "+faststart",
	)
	if audioIndex >= 0 {
		args = append(args, "-shortest")
	}
	return append(args, outputPath)
}

// StubFFmpeg writes a placeholder MP4 marker file. Tests that exercise the
// pipeline without media fixtures must opt into this implementation explicitly.
type StubFFmpeg struct{}

func (StubFFmpeg) Mux(_ context.Context, outputPath string, stageFiles []string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("stub-mp4 stages=%d", len(stageFiles))
	return os.WriteFile(outputPath, []byte(body), 0o644)
}
