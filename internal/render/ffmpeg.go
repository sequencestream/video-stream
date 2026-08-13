package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sequencestream/video-stream/internal/audio"
)

// FFmpeg muxes staged outputs into the final MP4.
type FFmpeg interface {
	Mux(ctx context.Context, outputPath string, stageFiles []string, plan MuxPlan) error
}

// MuxPlan describes the common video timeline produced by the mux stage.
// ClipDurations follows the video input order; supplying it lets the muxer add
// transitions without shortening the narration timeline.
type MuxPlan struct {
	Width              int
	Height             int
	FPS                int
	ClipDurations      []time.Duration
	TransitionDuration time.Duration
	SubtitleMode       audio.SubtitleMode
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
func (f ExecFFmpeg) Mux(ctx context.Context, outputPath string, stageFiles []string, plan MuxPlan) error {
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
	if plan.SubtitleMode == "" {
		plan.SubtitleMode = audio.SubtitleSoft
	}
	if err := plan.SubtitleMode.Validate(); err != nil {
		return err
	}
	if plan.SubtitleMode == audio.SubtitleBurnIn && inputs.subtitle == "" {
		return errors.New("ffmpeg subtitle burn-in requires a WebVTT or SRT input")
	}
	if strings.TrimSpace(outputPath) == "" {
		return errors.New("ffmpeg output path is required")
	}
	if err := plan.validate(len(inputs.videos)); err != nil {
		return err
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

	args := buildFFmpegArgs(inputs, plan, tmpPath)
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

func (p MuxPlan) validate(videoCount int) error {
	if p.Width <= 0 || p.Height <= 0 {
		return errors.New("ffmpeg mux width and height must be positive")
	}
	if p.FPS <= 0 {
		return errors.New("ffmpeg mux fps must be positive")
	}
	if len(p.ClipDurations) != videoCount {
		return fmt.Errorf("ffmpeg mux has %d video inputs but %d clip durations", videoCount, len(p.ClipDurations))
	}
	for i, duration := range p.ClipDurations {
		if duration <= 0 {
			return fmt.Errorf("ffmpeg mux clip %d duration must be positive", i)
		}
	}
	if p.TransitionDuration < 0 {
		return errors.New("ffmpeg mux transition duration must not be negative")
	}
	if p.TransitionDuration > 0 {
		for i, duration := range p.ClipDurations {
			if duration <= p.TransitionDuration {
				return fmt.Errorf("ffmpeg mux clip %d duration %s must exceed transition duration %s", i, duration, p.TransitionDuration)
			}
		}
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
	for _, path := range stageFiles {
		path = strings.TrimSpace(path)
		if path == "" {
			return ffmpegInputs{}, errors.New("ffmpeg input path is empty")
		}
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

func buildFFmpegArgs(inputs ffmpegInputs, plan MuxPlan, outputPath string) []string {
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

	filter := buildVideoFilter(len(inputs.videos), plan)
	if inputs.subtitle != "" && plan.SubtitleMode == audio.SubtitleBurnIn {
		filter = strings.Replace(filter, "[vout]", "[vbase]", 1)
		filter += ";[vbase]subtitles=filename='" + escapeFilterValue(inputs.subtitle) + "'[vout]"
	}
	args = append(args, "-filter_complex", filter, "-map", "[vout]")
	if audioIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("%d:a:0", audioIndex), "-c:a", "aac", "-b:a", "192k")
	}
	if subtitleIndex >= 0 && plan.SubtitleMode != audio.SubtitleBurnIn {
		args = append(args, "-map", fmt.Sprintf("%d:0", subtitleIndex), "-c:s", "mov_text", "-metadata:s:s:0", "language=und")
	}
	args = append(args,
		"-c:v", "libx264", "-preset", "medium", "-crf", "18",
		"-pix_fmt", "yuv420p", "-r", strconv.Itoa(plan.FPS), "-movflags", "+faststart",
	)
	timelineDuration := time.Duration(0)
	for _, duration := range plan.ClipDurations {
		timelineDuration += duration
	}
	args = append(args, "-t", formatFFmpegDuration(timelineDuration))
	return append(args, outputPath)
}

func escapeFilterValue(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`, `'`, `\'`, `:`, `\:`, `,`, `\,`, `[`, `\[`, `]`, `\]`, `;`, `\;`,
	)
	return replacer.Replace(value)
}

func buildVideoFilter(videoCount int, plan MuxPlan) string {
	var filter strings.Builder
	transitionSeconds := formatFFmpegDuration(plan.TransitionDuration)
	for i := 0; i < videoCount; i++ {
		if i > 0 {
			filter.WriteByte(';')
		}
		fmt.Fprintf(&filter,
			"[%d:v:0]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,fps=%d,setsar=1,settb=AVTB,setpts=PTS-STARTPTS,trim=duration=%s",
			i, plan.Width, plan.Height, plan.Width, plan.Height, plan.FPS, formatFFmpegDuration(plan.ClipDurations[i]))
		if plan.TransitionDuration > 0 && i < videoCount-1 {
			fmt.Fprintf(&filter, ",tpad=stop_mode=clone:stop_duration=%s", transitionSeconds)
		}
		fmt.Fprintf(&filter, "[v%d]", i)
	}

	if videoCount == 1 {
		filter.WriteString(";[v0]null[vout]")
		return filter.String()
	}
	if plan.TransitionDuration == 0 {
		filter.WriteByte(';')
		for i := 0; i < videoCount; i++ {
			fmt.Fprintf(&filter, "[v%d]", i)
		}
		fmt.Fprintf(&filter, "concat=n=%d:v=1:a=0[vout]", videoCount)
		return filter.String()
	}

	offset := time.Duration(0)
	previous := "v0"
	for i := 1; i < videoCount; i++ {
		offset += plan.ClipDurations[i-1]
		out := fmt.Sprintf("vx%d", i)
		if i == videoCount-1 {
			out = "vout"
		}
		fmt.Fprintf(&filter, ";[%s][v%d]xfade=transition=fade:duration=%s:offset=%s[%s]",
			previous, i, transitionSeconds, formatFFmpegDuration(offset), out)
		previous = out
	}
	return filter.String()
}

func formatFFmpegDuration(duration time.Duration) string {
	return strconv.FormatFloat(duration.Seconds(), 'f', 6, 64)
}

// StubFFmpeg writes a placeholder MP4 marker file. Tests that exercise the
// pipeline without media fixtures must opt into this implementation explicitly.
type StubFFmpeg struct{}

func (StubFFmpeg) Mux(_ context.Context, outputPath string, stageFiles []string, _ MuxPlan) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("stub-mp4 stages=%d", len(stageFiles))
	return os.WriteFile(outputPath, []byte(body), 0o644)
}
