package render_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/render"
)

func TestFFmpegVideoGeneratorRendersLocalMediaAndMotionGraphics(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}

	dir := t.TempDir()
	mediaDir := filepath.Join(dir, "media")
	projectMedia := filepath.Join(mediaDir, "project")
	if err := os.MkdirAll(projectMedia, 0o755); err != nil {
		t.Fatal(err)
	}
	makeMediaFixture(t, ffmpeg, "-f", "lavfi", "-i", "color=c=orange:s=640x480", "-frames:v", "1", filepath.Join(projectMedia, "still.png"))
	makeMediaFixture(t, ffmpeg, "-f", "lavfi", "-i", "color=c=blue:s=480x640:d=0.15:r=30", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", filepath.Join(projectMedia, "clip.mp4"))

	generator := render.FFmpegVideoGenerator{
		Binary: ffmpeg, OutputDir: filepath.Join(dir, "output"), MediaDir: mediaDir,
	}
	tests := []struct {
		name string
		seg  string
		key  string
	}{
		{name: "image uses Ken Burns", seg: "still", key: "rk2:still"},
		{name: "video is looped and cropped", seg: "clip", key: "rk2:clip"},
		{name: "missing media uses motion graphics", seg: "motion", key: "rk2:motion"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			generated, err := generator.Generate(ctx, render.VideoGenInput{
				Resolution: render.Resolution720p, ProjectID: "project", SegID: tt.seg,
				Text: "A useful visual", DurationMS: 450, RenderCacheKey: tt.key, Seed: "seed",
			})
			if err != nil {
				t.Fatal(err)
			}
			if generated.DurationMS < 400 || generated.DurationMS > 500 {
				t.Fatalf("measured duration=%dms, want about 450ms", generated.DurationMS)
			}
			probeVisual(t, ffprobe, generated.URI, 1280, 720, 0.45)
		})
	}
}

func TestFFmpegVideoGeneratorAcceptsExplicitFileURI(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "source image.png")
	makeMediaFixture(t, ffmpeg, "-f", "lavfi", "-i", "color=c=green:s=320x180", "-frames:v", "1", imagePath)
	generator := render.FFmpegVideoGenerator{Binary: ffmpeg, OutputDir: filepath.Join(dir, "output")}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	generated, err := generator.Generate(ctx, render.VideoGenInput{
		Resolution: render.Resolution720p, ProjectID: "project", SegID: "seg",
		Text: "text", DurationMS: 200, RenderCacheKey: "rk2:file-uri",
		RefURI: "file://" + filepath.ToSlash(imagePath),
	})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(generated.URI); err != nil || info.Size() == 0 {
		t.Fatalf("generated visual is missing or empty: info=%v err=%v", info, err)
	}
}

func TestFFmpegVideoGeneratorStillImageHoldsTheFrame(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "poster.png")
	// testsrc is asymmetric, so a Ken Burns pan is visible as a frame difference.
	makeMediaFixture(t, ffmpeg, "-f", "lavfi", "-i", "testsrc=s=640x480", "-frames:v", "1", imagePath)
	generator := render.FFmpegVideoGenerator{Binary: ffmpeg, OutputDir: filepath.Join(dir, "output")}

	// The comparison is a mean absolute difference rather than an equality
	// check: lossy H.264 reconstruction makes even an identical source frame
	// differ by a few levels, while a pan moves whole edges across the frame.
	const maxStillDrift = 2.0

	tests := []struct {
		name  string
		still bool
		seg   string
	}{
		{name: "still image holds the frame", still: true, seg: "still"},
		{name: "default applies Ken Burns", still: false, seg: "panned"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			generated, err := generator.Generate(ctx, render.VideoGenInput{
				Resolution: render.Resolution720p, ProjectID: "project", SegID: tt.seg,
				Text: "A useful visual", DurationMS: 600, RenderCacheKey: "rk2:" + tt.seg,
				RefURI: "file://" + filepath.ToSlash(imagePath), StillImage: tt.still,
			})
			if err != nil {
				t.Fatal(err)
			}
			first := rawFrame(t, ffmpeg, generated.URI)
			last := rawFrame(t, ffmpeg, generated.URI, "-sseof", "-0.05")
			drift := meanAbsDiff(t, first, last)
			if tt.still && drift > maxStillDrift {
				t.Fatalf("still visual drifted by %.2f levels, want <= %.2f", drift, maxStillDrift)
			}
			if !tt.still && drift <= maxStillDrift {
				t.Fatalf("Ken Burns visual drifted by only %.2f levels, want > %.2f", drift, maxStillDrift)
			}
		})
	}
}

// rawFrame decodes one frame as rgb24 so two frames can be compared per pixel.
func rawFrame(t *testing.T, ffmpeg, path string, seek ...string) []byte {
	t.Helper()
	args := append([]string{"-hide_banner", "-loglevel", "error"}, seek...)
	args = append(args, "-i", path, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
	out, err := exec.Command(ffmpeg, args...).Output()
	if err != nil {
		t.Fatalf("decode frame from %s: %v", path, err)
	}
	if len(out) == 0 {
		t.Fatalf("decoded an empty frame from %s", path)
	}
	return out
}

func meanAbsDiff(t *testing.T, a, b []byte) float64 {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("frame sizes differ: %d vs %d", len(a), len(b))
	}
	var total float64
	for i := range a {
		total += math.Abs(float64(a[i]) - float64(b[i]))
	}
	return total / float64(len(a))
}

func makeMediaFixture(t *testing.T, ffmpeg string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args = append([]string{"-hide_banner", "-loglevel", "error"}, args...)
	args = append(args, "-y")
	if output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("create media fixture: %v: %s", err, output)
	}
}

func probeVisual(t *testing.T, ffprobe, path string, width, height int, duration float64) {
	t.Helper()
	out, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name,width,height:format=duration", "-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Streams []struct {
			Codec  string `json:"codec_name"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatal(err)
	}
	if len(probe.Streams) != 1 || probe.Streams[0].Codec != "h264" || probe.Streams[0].Width != width || probe.Streams[0].Height != height {
		t.Fatalf("unexpected visual streams: %s", out)
	}
	gotDuration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil {
		t.Fatal(err)
	}
	if delta := gotDuration - duration; delta < -0.06 || delta > 0.06 {
		t.Fatal(fmt.Sprintf("duration=%0.3f want about %0.3f", gotDuration, duration))
	}
}
