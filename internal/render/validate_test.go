package render_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/render"
)

func TestExecOutputValidatorAcceptsDeliveryContract(t *testing.T) {
	ffmpeg, ffprobe := mediaBinaries(t)
	path := filepath.Join(t.TempDir(), "valid.mp4")
	makeValidationFixture(t, ffmpeg, path, true)

	err := (render.ExecOutputValidator{FFmpegBinary: ffmpeg, FFprobeBinary: ffprobe}).Validate(t.Context(), path, render.OutputSpec{
		Container: "mp4", Width: 320, Height: 180,
		Duration: time.Second, DurationTolerance: 100 * time.Millisecond, RequireAudio: true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecOutputValidatorRejectsContractMismatches(t *testing.T) {
	ffmpeg, ffprobe := mediaBinaries(t)
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.mp4")
	silent := filepath.Join(dir, "silent.mp4")
	mkv := filepath.Join(dir, "wrong-container.mkv")
	makeValidationFixture(t, ffmpeg, valid, true)
	makeValidationFixture(t, ffmpeg, silent, false)
	makeValidationFixture(t, ffmpeg, mkv, true)
	validator := render.ExecOutputValidator{FFmpegBinary: ffmpeg, FFprobeBinary: ffprobe}

	tests := []struct {
		name string
		path string
		spec render.OutputSpec
		want string
	}{
		{name: "container", path: mkv, spec: validationSpec(), want: "container"},
		{name: "dimensions", path: valid, spec: withDimensions(validationSpec(), 640, 360), want: "dimensions"},
		{name: "duration", path: valid, spec: withDuration(validationSpec(), 2*time.Second), want: "duration"},
		{name: "audio", path: silent, spec: validationSpec(), want: "no audio stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(t.Context(), tt.path, tt.spec)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestExecOutputValidatorRejectsDecodeFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell script")
	}
	ffmpeg, ffprobe := mediaBinaries(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "valid.mp4")
	makeValidationFixture(t, ffmpeg, path, true)
	failingDecoder := filepath.Join(dir, "ffmpeg-fail")
	if err := os.WriteFile(failingDecoder, []byte("#!/bin/sh\necho decode-failed >&2\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := (render.ExecOutputValidator{FFmpegBinary: failingDecoder, FFprobeBinary: ffprobe}).Validate(t.Context(), path, validationSpec())
	if err == nil || !strings.Contains(err.Error(), "decode-failed") {
		t.Fatalf("got %v, want decode failure", err)
	}
}

func TestExecOutputValidatorHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (render.ExecOutputValidator{}).Validate(ctx, "unused.mp4", validationSpec())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func validationSpec() render.OutputSpec {
	return render.OutputSpec{
		Container: "mp4", Width: 320, Height: 180,
		Duration: time.Second, DurationTolerance: 100 * time.Millisecond, RequireAudio: true,
	}
}

func withDimensions(spec render.OutputSpec, width, height int) render.OutputSpec {
	spec.Width, spec.Height = width, height
	return spec
}

func withDuration(spec render.OutputSpec, duration time.Duration) render.OutputSpec {
	spec.Duration = duration
	return spec
}

func mediaBinaries(t *testing.T) (string, string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	return ffmpeg, ffprobe
}

func makeValidationFixture(t *testing.T, ffmpeg, path string, audio bool) {
	t.Helper()
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x180:d=1:r=30"}
	if audio {
		args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-shortest", "-c:a", "aac")
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-y", path)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("create validation fixture: %v: %s", err, output)
	}
}
