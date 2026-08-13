package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sequencestream/video-stream/internal/hybrid"
)

// VideoGenInput is one seg's video model call. Resolution selects the tier;
// prompt/seed/ref come from SharedVisual and must not change between tiers.
type VideoGenInput struct {
	Resolution     Resolution
	ProjectID      string
	SegID          string
	Text           string
	DurationMS     int64
	RenderCacheKey string
	Prompt         string
	Seed           string
	RefURI         string
}

// FFmpegVideoGenerator turns local media into normalized, independently
// playable MP4 segments. It looks for an explicit file RefURI first, then for
// <MediaDir>/<project>/<seg> with a supported image or video extension. When
// no local asset exists it creates deterministic motion graphics, so the
// production render path always has real video frames without a remote model.
type FFmpegVideoGenerator struct {
	Binary    string
	OutputDir string
	MediaDir  string
	FPS       int
}

var (
	imageExtensions = []string{".jpg", ".jpeg", ".png", ".webp", ".bmp"}
	videoExtensions = []string{".mp4", ".mov", ".m4v", ".webm", ".mkv"}
)

// Generate renders one segment atomically. A repeated call safely replaces the
// prior output, so changing a local source never serves stale frames.
func (g FFmpegVideoGenerator) Generate(ctx context.Context, in VideoGenInput) (GeneratedVideo, error) {
	if err := ctx.Err(); err != nil {
		return GeneratedVideo{}, err
	}
	if strings.TrimSpace(g.OutputDir) == "" {
		return GeneratedVideo{}, errors.New("video output dir is not configured")
	}
	if in.DurationMS <= 0 {
		return GeneratedVideo{}, fmt.Errorf("segment %s duration must be positive", in.SegID)
	}
	w, h := in.Resolution.Dimensions()
	outDir := filepath.Join(g.OutputDir, safeFilename(in.ProjectID), "visuals")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return GeneratedVideo{}, fmt.Errorf("create visual output directory: %w", err)
	}
	outPath := filepath.Join(outDir, safeFilename(in.RenderCacheKey)+"_"+strconv.Itoa(w)+"x"+strconv.Itoa(h)+".mp4")
	tmp, err := os.CreateTemp(outDir, ".visual-*.mp4")
	if err != nil {
		return GeneratedVideo{}, fmt.Errorf("create temporary visual: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return GeneratedVideo{}, fmt.Errorf("close temporary visual: %w", err)
	}
	defer os.Remove(tmpPath)

	source, kind, err := g.resolveSource(in)
	if err != nil {
		return GeneratedVideo{}, err
	}
	args := g.arguments(in, source, kind, w, h, tmpPath)
	binary := strings.TrimSpace(g.Binary)
	if binary == "" {
		binary = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return GeneratedVideo{}, fmt.Errorf("ffmpeg visual interrupted: %w", ctxErr)
		}
		message := strings.TrimSpace(stderr.String())
		if len(message) > 8*1024 {
			message = message[len(message)-8*1024:]
		}
		return GeneratedVideo{}, fmt.Errorf("render visual for seg %s: %w: %s", in.SegID, err, message)
	}
	info, err := os.Stat(tmpPath)
	if err != nil || info.Size() == 0 {
		return GeneratedVideo{}, fmt.Errorf("ffmpeg produced no visual for seg %s", in.SegID)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		return GeneratedVideo{}, fmt.Errorf("publish visual for seg %s: %w", in.SegID, err)
	}
	actualDurationMS, err := probeVideoDurationMS(ctx, binary, outPath)
	if err != nil {
		return GeneratedVideo{}, fmt.Errorf("measure visual for seg %s: %w", in.SegID, err)
	}
	return GeneratedVideo{URI: outPath, DurationMS: actualDurationMS}, nil
}

func probeVideoDurationMS(ctx context.Context, ffmpegBinary, path string) (int64, error) {
	ffprobe := "ffprobe"
	if strings.TrimSpace(ffmpegBinary) != "" {
		ffprobe = filepath.Join(filepath.Dir(ffmpegBinary), "ffprobe")
	}
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("invalid ffprobe duration %q", strings.TrimSpace(string(out)))
	}
	return int64(seconds*1000 + 0.5), nil
}

type localMediaKind int

const (
	mediaMotion localMediaKind = iota
	mediaImage
	mediaVideo
)

func (g FFmpegVideoGenerator) resolveSource(in VideoGenInput) (string, localMediaKind, error) {
	if path, ok := localFilePath(in.RefURI); ok {
		kind := mediaKind(path)
		if kind == mediaMotion {
			return "", mediaMotion, fmt.Errorf("unsupported local media type for seg %s: %s", in.SegID, path)
		}
		if info, err := os.Stat(path); err != nil {
			return "", mediaMotion, fmt.Errorf("local media for seg %s: %w", in.SegID, err)
		} else if !info.Mode().IsRegular() {
			return "", mediaMotion, fmt.Errorf("local media for seg %s is not a regular file", in.SegID)
		}
		return path, kind, nil
	}
	if strings.TrimSpace(g.MediaDir) == "" {
		return "", mediaMotion, nil
	}
	base := filepath.Join(g.MediaDir, safeFilename(in.ProjectID), safeFilename(in.SegID))
	for _, ext := range append(append([]string(nil), videoExtensions...), imageExtensions...) {
		path := base + ext
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, mediaKind(path), nil
		}
	}
	return "", mediaMotion, nil
}

func localFilePath(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err == nil && u.Scheme == "file" && (u.Host == "" || u.Host == "localhost") {
		path, unescapeErr := url.PathUnescape(u.Path)
		return filepath.FromSlash(path), unescapeErr == nil
	}
	if filepath.IsAbs(raw) {
		return raw, true
	}
	return "", false
}

func mediaKind(path string) localMediaKind {
	ext := strings.ToLower(filepath.Ext(path))
	for _, candidate := range imageExtensions {
		if ext == candidate {
			return mediaImage
		}
	}
	for _, candidate := range videoExtensions {
		if ext == candidate {
			return mediaVideo
		}
	}
	return mediaMotion
}

func (g FFmpegVideoGenerator) arguments(in VideoGenInput, source string, kind localMediaKind, width, height int, output string) []string {
	fps := g.FPS
	if fps <= 0 {
		fps = 30
	}
	duration := time.Duration(in.DurationMS) * time.Millisecond
	seconds := strconv.FormatFloat(duration.Seconds(), 'f', 3, 64)
	frames := max(1, int(duration.Seconds()*float64(fps)+0.5))
	common := []string{"-hide_banner", "-loglevel", "error"}
	var args []string
	switch kind {
	case mediaVideo:
		filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,fps=%d,setsar=1", width, height, width, height, fps)
		args = append(common, "-stream_loop", "-1", "-i", source, "-t", seconds, "-vf", filter)
	case mediaImage:
		kb := hybrid.ComputeKenBurns(hybrid.KenBurnsSeed(in.SegID, in.Text), width, height)
		step := (kb.EndScale - kb.StartScale) / float64(frames)
		x := fmt.Sprintf("(iw-iw/zoom)*(%0.6f+(%0.6f-%0.6f)*on/%d)", kb.StartX, kb.EndX, kb.StartX, frames)
		y := fmt.Sprintf("(ih-ih/zoom)*(%0.6f+(%0.6f-%0.6f)*on/%d)", kb.StartY, kb.EndY, kb.StartY, frames)
		filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,zoompan=z='min(%0.6f+on*%0.9f,%0.6f)':x='%s':y='%s':d=1:s=%dx%d:fps=%d,setsar=1", width, height, width, height, kb.StartScale, step, kb.EndScale, x, y, width, height, fps)
		args = append(common, "-loop", "1", "-i", source, "-t", seconds, "-vf", filter)
	default:
		color := motionColor(in.Seed + in.RenderCacheKey)
		input := fmt.Sprintf("color=c=%s:s=%dx%d:r=%d:d=%s", color, width, height, fps, seconds)
		filter := fmt.Sprintf("drawgrid=w=iw/8:h=ih/8:t=2:c=white@0.12,zoompan=z='min(zoom+0.0008,1.08)':x='iw/2-iw/zoom/2':y='ih/2-ih/zoom/2':d=1:s=%dx%d:fps=%d,setsar=1", width, height, fps)
		args = append(common, "-f", "lavfi", "-i", input, "-vf", filter)
	}
	return append(args, "-an", "-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-y", output)
}

func motionColor(seed string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	// Keep channels away from black and white so the grid remains visible.
	v := h.Sum32()
	return fmt.Sprintf("0x%02x%02x%02x", 32+byte(v>>16)%128, 32+byte(v>>8)%128, 32+byte(v)%128)
}

// VideoGenerator produces per-seg visual clips. The 1080p pass reuses stored
// shared context and must not invoke LLM-backed prompt generation.
type VideoGenerator interface {
	Generate(ctx context.Context, in VideoGenInput) (GeneratedVideo, error)
}

// GeneratedVideo contains measured provider output. Local FFmpeg generation
// costs no provider credits; paid adapters populate CostMicros from receipts.
type GeneratedVideo struct {
	URI        string
	DurationMS int64
	CostMicros int64
}

// StubVideoGenerator writes deterministic stub clip paths.
type StubVideoGenerator struct {
	OutputDir string
}

func (g StubVideoGenerator) Generate(_ context.Context, in VideoGenInput) (GeneratedVideo, error) {
	w, h := in.Resolution.Dimensions()
	name := safeFilename(in.RenderCacheKey) + "_" + strconv.Itoa(w) + "x" + strconv.Itoa(h) + ".clip"
	path := filepath.Join(g.OutputDir, name)
	if err := writeStubFile(path, in.Seed); err != nil {
		return GeneratedVideo{}, err
	}
	return GeneratedVideo{URI: path, DurationMS: in.DurationMS}, nil
}

// CountingVideoGenerator wraps another generator for test assertions.
type CountingVideoGenerator struct {
	Inner VideoGenerator
	Calls int
}

func (c *CountingVideoGenerator) Generate(ctx context.Context, in VideoGenInput) (GeneratedVideo, error) {
	c.Calls++
	return c.Inner.Generate(ctx, in)
}

func safeFilename(key string) string {
	name := strings.NewReplacer(":", "_", "/", "_", `\`, "_").Replace(key)
	if name == "" || name == "." || name == ".." {
		return "_" + strings.ReplaceAll(name, ".", "_")
	}
	return name
}

func writeStubFile(path, seed string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("stub-clip seed="+seed), 0o644)
}
