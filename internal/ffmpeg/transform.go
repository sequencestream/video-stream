package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Fit selects what happens when the source aspect ratio does not match the
// target frame.
type Fit string

// The fit modes.
const (
	// FitCover fills the frame and crops the overflow. Nothing is letterboxed
	// and something is lost — usually the right choice for a background, and
	// the wrong one for a slide with text at the edges.
	FitCover Fit = "cover"
	// FitContain fits the whole frame inside and pads the rest.
	FitContain Fit = "contain"
	// FitStretch distorts the picture to fill the frame exactly.
	FitStretch Fit = "stretch"
)

// ParseFit resolves a fit mode name.
func ParseFit(s string) (Fit, error) {
	switch Fit(strings.ToLower(strings.TrimSpace(s))) {
	case FitCover, "":
		return FitCover, nil
	case FitContain, "fit", "pad":
		return FitContain, nil
	case FitStretch, "fill":
		return FitStretch, nil
	default:
		return "", fmt.Errorf("unknown fit %q: want cover, contain or stretch", s)
	}
}

// ScaleFilter builds the filter chain that puts the source into a width×height
// frame.
//
// The even-dimension rounding is not cosmetic: H.264's 4:2:0 chroma
// subsampling cannot represent an odd width or height, and ffmpeg fails
// outright rather than rounding for you.
func ScaleFilter(width, height int, fit Fit, background string) string {
	width, height = width&^1, height&^1
	switch fit {
	case FitStretch:
		return fmt.Sprintf("scale=%d:%d", width, height)
	case FitContain:
		if strings.TrimSpace(background) == "" {
			background = "black"
		}
		return fmt.Sprintf(
			"scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:%s",
			width, height, width, height, background)
	default:
		return fmt.Sprintf(
			"scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d",
			width, height, width, height)
	}
}

// Resize re-frames the video, leaving the audio untouched.
func (t Tool) Resize(ctx context.Context, input, output string, width, height int, fit Fit, background string, enc Encode) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("frame size %dx%d is not usable", width, height)
	}
	filter := ScaleFilter(width, height, fit, background)
	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(t.BaseArgs(), "-i", input, "-vf", filter)
		args = append(args, enc.VideoArgs()...)
		args = append(args, "-c:a", "copy")
		return append(args, faststart(output, tmp)...)
	})
}

// Speed changes the playback rate of both streams.
//
// Audio goes through a chain of atempo filters because a single one only
// accepts 0.5 to 2.0; 4× playback is two atempo=2 stages, not one atempo=4.
func (t Tool) Speed(ctx context.Context, input, output string, rate float64, media Media, enc Encode) error {
	if rate <= 0 {
		return fmt.Errorf("speed %g must be positive", rate)
	}
	if rate == 1 {
		return t.remux(ctx, input, output)
	}

	var filters []string
	if media.HasVideo() {
		filters = append(filters, fmt.Sprintf("[0:v]setpts=%s*PTS[v]", trimFloat(1/rate)))
	}
	if media.HasAudio() {
		filters = append(filters, "[0:a]"+atempoChain(rate)+"[a]")
	}
	graph := strings.Join(filters, ";")

	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(t.BaseArgs(), "-i", input, "-filter_complex", graph)
		if media.HasVideo() {
			args = append(args, "-map", "[v]")
			args = append(args, enc.VideoArgs()...)
		}
		if media.HasAudio() {
			args = append(args, "-map", "[a]")
			args = append(args, enc.AudioArgs()...)
		}
		return append(args, faststart(output, tmp)...)
	})
}

// atempoChain factors a rate into stages atempo will accept.
func atempoChain(rate float64) string {
	var stages []string
	remaining := rate
	for remaining > 2 {
		stages = append(stages, "atempo=2")
		remaining /= 2
	}
	for remaining < 0.5 {
		stages = append(stages, "atempo=0.5")
		remaining *= 2
	}
	stages = append(stages, "atempo="+trimFloat(remaining))
	return strings.Join(stages, ",")
}

// ConcatFiles joins several files into one.
//
// Re-encoding is the default because the concat filter accepts inputs that
// differ in resolution, frame rate or codec, which is the normal state of a
// folder of clips. fast uses the concat demuxer instead: instant, but it
// produces a broken file unless every input already matches exactly.
func (t Tool) ConcatFiles(ctx context.Context, inputs []string, output string, width, height int, fit Fit, fast bool, enc Encode) error {
	if len(inputs) < 2 {
		return errors.New("concat needs at least two input files")
	}
	if fast {
		return t.concatByDemuxer(ctx, inputs, output)
	}

	var b strings.Builder
	for i := range inputs {
		if width > 0 && height > 0 {
			fmt.Fprintf(&b, "[%d:v]%s,setsar=1[v%d];\n", i, ScaleFilter(width, height, fit, ""), i)
		} else {
			fmt.Fprintf(&b, "[%d:v]setsar=1[v%d];\n", i, i)
		}
	}
	for i := range inputs {
		fmt.Fprintf(&b, "[v%d][%d:a]", i, i)
	}
	fmt.Fprintf(&b, "concat=n=%d:v=1:a=1[outv][outa]", len(inputs))

	scriptPath, cleanup, err := writeTempFile("vs-concat-*.txt", b.String())
	if err != nil {
		return err
	}
	defer cleanup()

	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := t.BaseArgs()
		for _, in := range inputs {
			args = append(args, "-i", in)
		}
		args = append(args, "-filter_complex_script", scriptPath, "-map", "[outv]", "-map", "[outa]")
		args = append(args, enc.VideoArgs()...)
		args = append(args, enc.AudioArgs()...)
		return append(args, faststart(output, tmp)...)
	})
}

func (t Tool) concatByDemuxer(ctx context.Context, inputs []string, output string) error {
	var list strings.Builder
	for _, in := range inputs {
		abs, err := filepath.Abs(in)
		if err != nil {
			return err
		}
		fmt.Fprintf(&list, "file '%s'\n", strings.ReplaceAll(abs, "'", `'\''`))
	}
	listPath, cleanup, err := writeTempFile("vs-concat-*.txt", list.String())
	if err != nil {
		return err
	}
	defer cleanup()

	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(t.BaseArgs(),
			"-f", "concat", "-safe", "0", "-i", listPath,
			"-map", "0", "-c", "copy",
		)
		return append(args, faststart(output, tmp)...)
	})
}

// Thumbnail writes a single frame from the given timestamp.
func (t Tool) Thumbnail(ctx context.Context, input, output string, atMS int64, width int) error {
	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(t.BaseArgs(), "-ss", strconv.FormatFloat(float64(atMS)/1000, 'f', 3, 64), "-i", input)
		if width > 0 {
			args = append(args, "-vf", fmt.Sprintf("scale=%d:-2", width&^1))
		}
		return append(args, "-frames:v", "1", "-q:v", "2", tmp)
	})
}

// ParseSize reads a WxH frame size, or one of the shorthand names people
// actually use for the shapes a platform wants.
func ParseSize(s string) (int, int, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return 0, 0, errors.New("a frame size is required")
	case "720p":
		return 1280, 720, nil
	case "1080p":
		return 1920, 1080, nil
	case "1440p":
		return 2560, 1440, nil
	case "4k", "2160p":
		return 3840, 2160, nil
	case "vertical", "shorts", "reels", "douyin":
		return 1080, 1920, nil
	case "square":
		return 1080, 1080, nil
	}
	w, h, ok := strings.Cut(strings.ToLower(s), "x")
	if !ok {
		return 0, 0, fmt.Errorf("size %q must be WxH or a name like 1080p, vertical or square", s)
	}
	width, err := strconv.Atoi(strings.TrimSpace(w))
	if err != nil {
		return 0, 0, fmt.Errorf("size %q: %w", s, err)
	}
	height, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil {
		return 0, 0, fmt.Errorf("size %q: %w", s, err)
	}
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("size %q must be positive", s)
	}
	return width, height, nil
}

// EnsureAbsent reports whether a path is free, for the commands that write
// something other than through RunAtomic.
func EnsureAbsent(path string, overwrite bool) error {
	if overwrite {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s: %w", path, ErrOutputExists)
	}
	return nil
}
