package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// OutputSpec is the delivery contract for a muxed render.
type OutputSpec struct {
	Container         string
	Width             int
	Height            int
	Duration          time.Duration
	DurationTolerance time.Duration
	RequireAudio      bool
}

// OutputValidator verifies a muxed file before the render is deliverable.
type OutputValidator interface {
	Validate(ctx context.Context, path string, spec OutputSpec) error
}

// ExecOutputValidator uses ffprobe for media metadata and ffmpeg for a full
// audio/video decode. The binaries default to ffprobe and ffmpeg respectively.
type ExecOutputValidator struct {
	FFprobeBinary string
	FFmpegBinary  string
}

type probeOutput struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
}

// Validate checks the container, dimensions, duration, audio presence, and
// decodability. All checks must pass before Engine exposes an output URI.
func (v ExecOutputValidator) Validate(ctx context.Context, path string, spec OutputSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := spec.validate(); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat render output: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("render output is not a non-empty regular file")
	}

	probeBinary := strings.TrimSpace(v.FFprobeBinary)
	if probeBinary == "" {
		probeBinary = "ffprobe"
	}
	cmd := exec.CommandContext(ctx, probeBinary,
		"-v", "error", "-show_entries", "format=format_name,duration:stream=codec_type,width,height", "-of", "json", path)
	var probeStderr bytes.Buffer
	cmd.Stderr = &probeStderr
	raw, err := cmd.Output()
	if err != nil {
		return commandError(ctx, "ffprobe output", err, probeStderr.String())
	}
	var probe probeOutput
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("decode ffprobe output: %w", err)
	}
	if !containsFormat(probe.Format.FormatName, spec.Container) {
		return fmt.Errorf("container is %q, want %q", probe.Format.FormatName, spec.Container)
	}

	videoCount, audioCount := 0, 0
	for _, stream := range probe.Streams {
		switch stream.CodecType {
		case "video":
			videoCount++
			if stream.Width != spec.Width || stream.Height != spec.Height {
				return fmt.Errorf("video dimensions are %dx%d, want %dx%d", stream.Width, stream.Height, spec.Width, spec.Height)
			}
		case "audio":
			audioCount++
		}
	}
	if videoCount == 0 {
		return errors.New("render output has no video stream")
	}
	if spec.RequireAudio && audioCount == 0 {
		return errors.New("render output has no audio stream")
	}

	durationSeconds, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || durationSeconds <= 0 {
		return fmt.Errorf("invalid render duration %q", probe.Format.Duration)
	}
	actualDuration := time.Duration(durationSeconds * float64(time.Second))
	if delta := absDuration(actualDuration - spec.Duration); delta > spec.DurationTolerance {
		return fmt.Errorf("render duration is %s, want %s (+/-%s)", actualDuration, spec.Duration, spec.DurationTolerance)
	}

	ffmpegBinary := strings.TrimSpace(v.FFmpegBinary)
	if ffmpegBinary == "" {
		ffmpegBinary = "ffmpeg"
	}
	decodeArgs := []string{"-hide_banner", "-loglevel", "error", "-xerror", "-nostdin", "-i", path, "-map", "0:v:0"}
	if spec.RequireAudio {
		decodeArgs = append(decodeArgs, "-map", "0:a:0")
	} else {
		decodeArgs = append(decodeArgs, "-map", "0:a:0?")
	}
	decodeArgs = append(decodeArgs, "-f", "null", "-")
	decode := exec.CommandContext(ctx, ffmpegBinary, decodeArgs...)
	var decodeStderr bytes.Buffer
	decode.Stderr = &decodeStderr
	if err := decode.Run(); err != nil {
		return commandError(ctx, "decode render output", err, decodeStderr.String())
	}
	return nil
}

func (s OutputSpec) validate() error {
	if strings.TrimSpace(s.Container) == "" {
		return errors.New("output container is required")
	}
	if s.Width <= 0 || s.Height <= 0 {
		return errors.New("output dimensions must be positive")
	}
	if s.Duration <= 0 {
		return errors.New("output duration must be positive")
	}
	if s.DurationTolerance < 0 {
		return errors.New("output duration tolerance must not be negative")
	}
	return nil
}

func containsFormat(formatNames, want string) bool {
	for _, name := range strings.Split(formatNames, ",") {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

func commandError(ctx context.Context, action string, err error, stderr string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s interrupted: %w", action, ctxErr)
	}
	message := strings.TrimSpace(stderr)
	if len(message) > 8*1024 {
		message = message[len(message)-8*1024:]
	}
	if message == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, message)
}

func defaultFFprobeBinary(ffmpegBinary string) string {
	ffmpegBinary = strings.TrimSpace(ffmpegBinary)
	if ffmpegBinary == "" || (!filepath.IsAbs(ffmpegBinary) && !strings.ContainsRune(ffmpegBinary, filepath.Separator)) {
		return "ffprobe"
	}
	return filepath.Join(filepath.Dir(ffmpegBinary), "ffprobe")
}

// StubOutputValidator is an explicit test double for placeholder media.
type StubOutputValidator struct{}

func (StubOutputValidator) Validate(context.Context, string, OutputSpec) error { return nil }
