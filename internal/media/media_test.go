package media_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/media"
)

func lookFFmpeg(t *testing.T) string {
	t.Helper()
	binary, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	return binary
}

// makeSource writes a 4:3 image with a thin blue band along its top edge. The
// band is thin enough that a centred crop of a 16:9 frame drops it entirely, so
// its presence identifies the anchor that was applied.
func makeSource(t *testing.T, ffmpeg, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	filter := "color=c=red:s=800x600[bg];color=c=blue:s=800x60[band];[bg][band]overlay=0:0"
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=8x8", "-filter_complex", filter, "-frames:v", "1", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create source image: %v: %s", err, out)
	}
}

func probeSize(t *testing.T, path string) (int, int) {
	t.Helper()
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	out, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=width,height", "-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatal(err)
	}
	if len(probe.Streams) == 0 {
		t.Fatalf("no streams in %s", path)
	}
	return probe.Streams[0].Width, probe.Streams[0].Height
}

// topRowIsBlue reports whether the first pixel row survived the crop.
func topRowIsBlue(t *testing.T, ffmpeg, path string) bool {
	t.Helper()
	// The whole frame is decoded and only its first pixel read: a 1x1 crop is
	// rejected by the rawvideo encoder.
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-i", path, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
	if err != nil {
		t.Fatalf("sample %s: %v", path, err)
	}
	if len(out) < 3 {
		t.Fatalf("sampled %d bytes from %s", len(out), path)
	}
	return out[2] > out[0]
}

func TestPlaceBackgroundFitsTheFrameAndFilesEverySeg(t *testing.T) {
	ffmpeg := lookFFmpeg(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "poster.jpg")
	makeSource(t, ffmpeg, source)

	mediaDir := filepath.Join(dir, "media")
	preparer := media.Preparer{MediaDir: mediaDir, FFmpegBinary: ffmpeg}
	result, err := preparer.PlaceBackground(context.Background(), media.Request{
		ProjectID: "proj", SegIDs: []string{"s1", "s2", "s3"},
		Source: source, Width: 1280, Height: 720, Anchor: media.AnchorTop,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Files) != 3 {
		t.Fatalf("got %d files, want 3", len(result.Files))
	}
	for i, segID := range []string{"s1", "s2", "s3"} {
		want := filepath.Join(mediaDir, "proj", segID+".jpg")
		if result.Files[i] != want {
			t.Errorf("file %d = %s, want %s", i, result.Files[i], want)
		}
		if w, h := probeSize(t, want); w != 1280 || h != 720 {
			t.Errorf("%s is %dx%d, want 1280x720", want, w, h)
		}
	}
	// The intermediate must not be left behind: the render pipeline globs this
	// directory and would treat a stray file as a seg asset.
	entries, err := os.ReadDir(filepath.Join(mediaDir, "proj"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("media dir holds %v, want exactly the three seg files", names)
	}
}

func TestPlaceBackgroundAnchorSelectsTheSurvivingBand(t *testing.T) {
	ffmpeg := lookFFmpeg(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "poster.jpg")
	makeSource(t, ffmpeg, source)

	tests := []struct {
		anchor      media.Anchor
		wantTopBlue bool
	}{
		{anchor: media.AnchorTop, wantTopBlue: true},
		{anchor: media.AnchorCenter, wantTopBlue: false},
		{anchor: media.AnchorBottom, wantTopBlue: false},
	}
	for _, tt := range tests {
		t.Run(string(tt.anchor), func(t *testing.T) {
			mediaDir := filepath.Join(dir, string(tt.anchor))
			preparer := media.Preparer{MediaDir: mediaDir, FFmpegBinary: ffmpeg}
			result, err := preparer.PlaceBackground(context.Background(), media.Request{
				ProjectID: "proj", SegIDs: []string{"s1"},
				Source: source, Width: 1280, Height: 720, Anchor: tt.anchor,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := topRowIsBlue(t, ffmpeg, result.Files[0]); got != tt.wantTopBlue {
				t.Errorf("top row is the source's top band = %v, want %v", got, tt.wantTopBlue)
			}
		})
	}
}

func TestPlaceBackgroundRejectsUnusableRequests(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "poster.jpg")
	if err := os.WriteFile(source, []byte("not really an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	preparer := media.Preparer{MediaDir: filepath.Join(dir, "media")}

	tests := []struct {
		name string
		req  media.Request
		want string
	}{
		{name: "no project", req: media.Request{SegIDs: []string{"s1"}, Source: source, Width: 1, Height: 1}, want: "project id"},
		{name: "no segs", req: media.Request{ProjectID: "p", Source: source, Width: 1, Height: 1}, want: "seg ids"},
		{name: "no source", req: media.Request{ProjectID: "p", SegIDs: []string{"s1"}, Width: 1, Height: 1}, want: "source image"},
		{name: "missing source", req: media.Request{ProjectID: "p", SegIDs: []string{"s1"}, Source: filepath.Join(dir, "nope.jpg"), Width: 1, Height: 1}, want: "read source image"},
		{name: "bad frame", req: media.Request{ProjectID: "p", SegIDs: []string{"s1"}, Source: source}, want: "not usable"},
		{name: "bad anchor", req: media.Request{ProjectID: "p", SegIDs: []string{"s1"}, Source: source, Width: 1, Height: 1, Anchor: "sideways"}, want: "unknown anchor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := preparer.PlaceBackground(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want an error mentioning %q", err, tt.want)
			}
		})
	}
}

func TestPlaceBackgroundReportsNoSegs(t *testing.T) {
	_, err := media.Preparer{MediaDir: t.TempDir()}.PlaceBackground(context.Background(), media.Request{
		ProjectID: "p", Source: "x.jpg", Width: 16, Height: 9,
	})
	if !errors.Is(err, media.ErrNoSegs) {
		t.Fatalf("got %v, want ErrNoSegs", err)
	}
}
