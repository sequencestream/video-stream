package render_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	err := (render.ExecFFmpeg{}).Mux(context.Background(), filepath.Join(dir, "out.mp4"), []string{audioPath}, render.MuxPlan{})
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

	err := (render.ExecFFmpeg{Binary: binary}).Mux(context.Background(), outputPath, []string{videoPath}, render.MuxPlan{
		Width: 320, Height: 180, FPS: 25, ClipDurations: []time.Duration{time.Second},
	})
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
	err := (render.ExecFFmpeg{}).Mux(ctx, "unused.mp4", nil, render.MuxPlan{})
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
	makeFixture("-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=blue:s=240x320:d=0.4:r=15", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-y", clip2)
	makeFixture("-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.8", "-c:a", "pcm_s16le", "-y", audioPath)
	if err := os.WriteFile(subtitlePath, []byte("WEBVTT\n\n00:00:00.000 --> 00:00:00.700\nhello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := (render.ExecFFmpeg{Binary: ffmpeg}).Mux(ctx, outputPath, []string{clip1, clip2, audioPath, subtitlePath}, render.MuxPlan{
		Width: 640, Height: 360, FPS: 30,
		ClipDurations:      []time.Duration{400 * time.Millisecond, 400 * time.Millisecond},
		TransitionDuration: 150 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_type,codec_name,width,height:format=duration", "-of", "json", outputPath)
	probeOutput, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
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
	for _, stream := range probe.Streams {
		if stream.CodecType == "video" && (stream.Width != 640 || stream.Height != 360) {
			t.Fatalf("video dimensions=%dx%d, want 640x360", stream.Width, stream.Height)
		}
	}
	duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil {
		t.Fatal(err)
	}
	if duration < 0.78 || duration > 0.85 {
		t.Fatalf("duration=%0.3f, want about 0.8s", duration)
	}

	frame, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-ss", "0.475", "-i", outputPath,
		"-frames:v", "1", "-vf", "scale=1:1", "-pix_fmt", "rgb24", "-f", "rawvideo", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) != 3 || frame[0] < 40 || frame[2] < 40 {
		t.Fatalf("transition frame RGB=%v, want a red/blue blend", frame)
	}
}
