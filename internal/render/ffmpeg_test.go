package render_test

import (
	"context"
	"encoding/json"
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

func TestExecFFmpegRequiresVideo(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "audio.wav")
	if err := os.WriteFile(audioPath, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := (render.ExecFFmpeg{}).Mux(context.Background(), filepath.Join(dir, "out.mp4"), []string{audioPath})
	if err == nil || !strings.Contains(err.Error(), "at least one video") {
		t.Fatalf("got %v, want missing video error", err)
	}
}

func TestExecFFmpegFailureDoesNotReplaceExistingOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell script")
	}
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "input.mp4")
	outputPath := filepath.Join(dir, "output.mp4")
	if err := os.WriteFile(videoPath, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "ffmpeg-fail")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho deliberate-failure >&2\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := (render.ExecFFmpeg{Binary: binary}).Mux(context.Background(), outputPath, []string{videoPath})
	if err == nil || !strings.Contains(err.Error(), "deliberate-failure") {
		t.Fatalf("got %v, want ffmpeg stderr", err)
	}
	body, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) != "existing" {
		t.Fatalf("output was replaced with %q", body)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, ".ffmpeg-*.mp4"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary outputs were not removed: %v", matches)
	}
}

func TestExecFFmpegHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (render.ExecFFmpeg{}).Mux(ctx, "unused.mp4", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestExecFFmpegMuxesVideoAudioAndSoftSubtitles(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	dir := t.TempDir()
	clip1 := filepath.Join(dir, "clip-1.mp4")
	clip2 := filepath.Join(dir, "clip-2.mp4")
	audioPath := filepath.Join(dir, "normalized.wav")
	subtitlePath := filepath.Join(dir, "subtitles.vtt")
	outputPath := filepath.Join(dir, "final.mp4")

	makeFixture := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, ffmpeg, args...)
		if output, runErr := cmd.CombinedOutput(); runErr != nil {
			t.Fatalf("create media fixture: %v: %s", runErr, output)
		}
	}
	makeFixture("-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=red:s=320x180:d=0.4:r=25", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-y", clip1)
	makeFixture("-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x180:d=0.4:r=25", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-y", clip2)
	makeFixture("-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.8", "-c:a", "pcm_s16le", "-y", audioPath)
	if err := os.WriteFile(subtitlePath, []byte("WEBVTT\n\n00:00:00.000 --> 00:00:00.700\nhello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := (render.ExecFFmpeg{Binary: ffmpeg}).Mux(ctx, outputPath, []string{clip1, clip2, audioPath, subtitlePath}); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_type,codec_name", "-of", "json", outputPath)
	probeOutput, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(probeOutput, &probe); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"video": "h264", "audio": "aac", "subtitle": "mov_text"}
	for _, stream := range probe.Streams {
		if codec, ok := want[stream.CodecType]; ok && codec == stream.CodecName {
			delete(want, stream.CodecType)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing output streams %v; ffprobe=%s", want, probeOutput)
	}
}
