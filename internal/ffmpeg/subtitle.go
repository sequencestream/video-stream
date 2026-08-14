package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SubtitleStyle is the look of burned-in captions.
//
// The fields map onto ASS style properties, because that is what libass — the
// renderer inside ffmpeg's subtitles filter — actually reads. Colors are
// &HBBGGRR: blue, green, red, in that order. Writing white as &H00FFFFFF and
// red as &H000000FF looks wrong and is correct.
type SubtitleStyle struct {
	Font         string
	FontSize     int
	PrimaryColor string
	OutlineColor string
	Outline      float64
	Shadow       float64
	MarginV      int
	// Position is bottom, center or top.
	Position string
	Bold     bool
	// FontsDir points libass at a directory of font files, for a font that is
	// not installed system-wide.
	FontsDir string
}

// DefaultSubtitleStyle is white text with a black outline near the bottom of
// the frame: the one combination that stays readable over arbitrary footage.
func DefaultSubtitleStyle() SubtitleStyle {
	return SubtitleStyle{
		FontSize: 42, PrimaryColor: "&H00FFFFFF", OutlineColor: "&H00000000",
		Outline: 2, Shadow: 0, MarginV: 60, Position: "bottom",
	}
}

// ForceStyle renders the style as the subtitles filter's force_style value.
func (s SubtitleStyle) ForceStyle() string {
	var parts []string
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	add("FontName", strings.TrimSpace(s.Font))
	if s.FontSize > 0 {
		add("FontSize", strconv.Itoa(s.FontSize))
	}
	add("PrimaryColour", strings.TrimSpace(s.PrimaryColor))
	add("OutlineColour", strings.TrimSpace(s.OutlineColor))
	if s.Outline > 0 {
		add("Outline", trimFloat(s.Outline))
		// BorderStyle 1 is outline plus shadow; 3 would paint an opaque box.
		add("BorderStyle", "1")
	}
	if s.Shadow > 0 {
		add("Shadow", trimFloat(s.Shadow))
	}
	if s.MarginV > 0 {
		add("MarginV", strconv.Itoa(s.MarginV))
	}
	if s.Bold {
		add("Bold", "-1") // ASS booleans are 0 and -1, not 0 and 1.
	}
	add("Alignment", strconv.Itoa(alignment(s.Position)))
	return strings.Join(parts, ",")
}

// alignment maps a position name to the ASS numpad alignment libass expects.
func alignment(position string) int {
	switch strings.ToLower(strings.TrimSpace(position)) {
	case "top":
		return 8
	case "center", "middle":
		return 5
	default:
		return 2
	}
}

// ErrNoSubtitlesFilter means this ffmpeg cannot draw text into video.
//
// Rendering subtitles needs libass, and a build without it has no `subtitles`
// filter at all. Minimal and headless-server builds routinely omit it, and the
// raw failure is a filter-graph parse error that never says why.
var ErrNoSubtitlesFilter = errors.New(
	"this ffmpeg build has no `subtitles` filter, so captions cannot be burned in\n" +
		"  it was compiled without libass; install a full build (brew install ffmpeg, or a static build from ffmpeg.org)\n" +
		"  or use -mode soft to add a selectable subtitle track instead, which needs no libass")

// BurnSubtitles renders captions into the video pixels.
//
// Burning in is the only way captions survive a platform that strips subtitle
// tracks, which is most of them for vertical video. The cost is that they can
// never be turned off, so this always re-encodes the video.
func (t Tool) BurnSubtitles(ctx context.Context, input, output, subtitlePath string, style SubtitleStyle, enc Encode) error {
	if !t.HasFilter(ctx, "subtitles") {
		return ErrNoSubtitlesFilter
	}

	// Stage the subtitle under a path with no characters the filter parser
	// treats as syntax. Escaping is possible but has to be right twice over —
	// once for the graph, once for the option — and a user's Downloads folder
	// is exactly where a colon or a quote shows up.
	staged, cleanup, err := stageSubtitle(subtitlePath)
	if err != nil {
		return err
	}
	defer cleanup()

	filter := "subtitles=" + staged
	if forced := style.ForceStyle(); forced != "" {
		filter += ":force_style='" + forced + "'"
	}
	if dir := strings.TrimSpace(style.FontsDir); dir != "" {
		filter += ":fontsdir='" + dir + "'"
	}

	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(t.BaseArgs(), "-i", input, "-vf", filter)
		args = append(args, enc.VideoArgs()...)
		// Audio is untouched: re-encoding it would be a second generation of
		// loss for a change that happens entirely in the video stream.
		args = append(args, "-c:a", "copy")
		return append(args, faststart(output, tmp)...)
	})
}

// EmbedSubtitles adds the captions as a selectable track, copying both media
// streams. It is fast and reversible, and a player that ignores subtitle
// tracks will show nothing at all.
func (t Tool) EmbedSubtitles(ctx context.Context, input, output, subtitlePath, language string) error {
	codec := subtitleCodecFor(output)
	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(t.BaseArgs(),
			"-i", input, "-i", subtitlePath,
			"-map", "0", "-map", "1",
			"-c", "copy", "-c:s", codec,
		)
		if lang := strings.TrimSpace(language); lang != "" {
			args = append(args, "-metadata:s:s:0", "language="+lang)
		}
		return append(args, faststart(output, tmp)...)
	})
}

// subtitleCodecFor picks the subtitle codec the output container accepts. MP4
// only carries mov_text; Matroska takes SubRip directly.
func subtitleCodecFor(output string) string {
	switch strings.ToLower(filepath.Ext(output)) {
	case ".mp4", ".m4v", ".mov":
		return "mov_text"
	default:
		return "srt"
	}
}

// stageSubtitle copies the subtitle file to a temp path built from characters
// the filter parser never treats as syntax.
func stageSubtitle(path string) (string, func(), error) {
	src, err := os.Open(path)
	if err != nil {
		return "", func() {}, fmt.Errorf("read subtitle file: %w", err)
	}
	defer src.Close()

	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".srt" && ext != ".ass" && ext != ".ssa" && ext != ".vtt" {
		ext = ".srt"
	}
	dst, err := os.CreateTemp("", "vs-sub-*"+ext)
	if err != nil {
		return "", func() {}, err
	}
	staged := dst.Name()
	cleanup := func() { os.Remove(staged) }
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := dst.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return staged, cleanup, nil
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
