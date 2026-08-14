package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sequencestream/video-stream/internal/timespan"
)

// CutOptions controls how a keep list becomes an output file.
type CutOptions struct {
	Encode Encode
	// Fast copies the compressed streams instead of re-encoding. It is many
	// times faster and produces no generation loss, but every cut snaps to the
	// nearest preceding keyframe — often a second or two early. Use it to pull
	// rough selects out of a long recording, never to remove a filler word.
	Fast bool
}

// ErrNothingKept means the keep list is empty, which would produce a zero-frame
// file. Writing one is never what the user meant.
var ErrNothingKept = errors.New("nothing left to keep")

// Cut writes the parts of input named by keep, joined in order, to output.
//
// This is the operation behind every command that shortens a video —
// `vs cut`, `vs filler`, `vs silence` — and they differ only in how they
// compute the keep list.
func (t Tool) Cut(ctx context.Context, input, output string, keep timespan.Ranges, media Media, opts CutOptions) error {
	keep = keep.Clamp(media.DurationMS)
	if len(keep) == 0 {
		return ErrNothingKept
	}

	// Nothing was actually removed: re-encoding here would cost minutes and
	// lose a generation of quality to produce the same video.
	if len(keep) == 1 && keep[0].StartMS == 0 && (media.DurationMS == 0 || keep[0].EndMS >= media.DurationMS) {
		return t.remux(ctx, input, output)
	}
	if len(keep) == 1 {
		return t.cutSingle(ctx, input, output, keep[0], opts)
	}
	if opts.Fast {
		return t.cutByCopy(ctx, input, output, keep)
	}
	return t.cutByFilter(ctx, input, output, keep, media, opts)
}

// Remove is Cut's complement: it writes everything except the named ranges.
func (t Tool) Remove(ctx context.Context, input, output string, cut timespan.Ranges, media Media, opts CutOptions) error {
	if media.DurationMS <= 0 {
		return errors.New("cannot remove ranges from a file with an unknown duration")
	}
	return t.Cut(ctx, input, output, cut.Invert(media.DurationMS), media, opts)
}

// remux copies both streams into a fresh container.
func (t Tool) remux(ctx context.Context, input, output string) error {
	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(t.BaseArgs(), "-i", input, "-c", "copy", "-map", "0")
		return append(args, faststart(output, tmp)...)
	})
}

// cutSingle extracts one span. With -ss before -i, ffmpeg seeks rather than
// decoding everything up to the start, which on a one-hour source is the
// difference between seconds and minutes.
func (t Tool) cutSingle(ctx context.Context, input, output string, r timespan.Range, opts CutOptions) error {
	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(t.BaseArgs(),
			"-ss", timespan.FormatSeconds(r.StartMS),
			"-i", input,
			"-t", timespan.FormatSeconds(r.Duration()),
			"-map", "0",
		)
		if opts.Fast {
			args = append(args, "-c", "copy", "-avoid_negative_ts", "make_zero")
		} else {
			args = append(args, opts.Encode.VideoArgs()...)
			args = append(args, opts.Encode.AudioArgs()...)
		}
		return append(args, faststart(output, tmp)...)
	})
}

// cutByFilter builds one trim/concat graph covering every kept span.
//
// The graph goes into a file rather than onto the command line: a filler pass
// over a ten-minute talk routinely produces two hundred ranges, and the
// resulting argument runs past what the OS will accept.
func (t Tool) cutByFilter(ctx context.Context, input, output string, keep timespan.Ranges, media Media, opts CutOptions) error {
	hasAudio := media.HasAudio()
	hasVideo := media.HasVideo()
	if !hasVideo && !hasAudio {
		return errors.New("input has neither a video nor an audio stream")
	}

	graph := buildTrimGraph(keep, hasVideo, hasAudio)
	if t.DryRun {
		fmt.Fprintf(t.errWriter(), "# filter graph (%d segments):\n%s\n", len(keep), graph)
	}

	scriptPath, cleanup, err := writeTempFile("vs-filter-*.txt", graph)
	if err != nil {
		return err
	}
	defer cleanup()

	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(t.BaseArgs(), "-i", input, "-filter_complex_script", scriptPath)
		if hasVideo {
			args = append(args, "-map", "[outv]")
			args = append(args, opts.Encode.VideoArgs()...)
		}
		if hasAudio {
			args = append(args, "-map", "[outa]")
			args = append(args, opts.Encode.AudioArgs()...)
		}
		return append(args, faststart(output, tmp)...)
	})
}

// buildTrimGraph renders the trim/concat filter graph for a keep list.
func buildTrimGraph(keep timespan.Ranges, hasVideo, hasAudio bool) string {
	var b strings.Builder
	for i, r := range keep {
		start := timespan.FormatSeconds(r.StartMS)
		end := timespan.FormatSeconds(r.EndMS)
		if hasVideo {
			fmt.Fprintf(&b, "[0:v]trim=start=%s:end=%s,setpts=PTS-STARTPTS[v%d];\n", start, end, i)
		}
		if hasAudio {
			fmt.Fprintf(&b, "[0:a]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS[a%d];\n", start, end, i)
		}
	}
	for i := range keep {
		if hasVideo {
			fmt.Fprintf(&b, "[v%d]", i)
		}
		if hasAudio {
			fmt.Fprintf(&b, "[a%d]", i)
		}
	}
	fmt.Fprintf(&b, "concat=n=%d:v=%d:a=%d", len(keep), boolToInt(hasVideo), boolToInt(hasAudio))
	if hasVideo {
		b.WriteString("[outv]")
	}
	if hasAudio {
		b.WriteString("[outa]")
	}
	return b.String()
}

// cutByCopy extracts each span with stream copy and joins them with the concat
// demuxer. Cuts land on keyframes; the payoff is that a 4K source is spliced in
// seconds instead of re-encoded for an hour.
func (t Tool) cutByCopy(ctx context.Context, input, output string, keep timespan.Ranges) error {
	dir, err := os.MkdirTemp("", "vs-cut-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	ext := filepath.Ext(input)
	if ext == "" {
		ext = filepath.Ext(output)
	}

	var list strings.Builder
	for i, r := range keep {
		part := filepath.Join(dir, fmt.Sprintf("part-%04d%s", i, ext))
		args := append(t.BaseArgs(),
			"-ss", timespan.FormatSeconds(r.StartMS),
			"-i", input,
			"-t", timespan.FormatSeconds(r.Duration()),
			"-map", "0", "-c", "copy", "-avoid_negative_ts", "make_zero",
			part,
		)
		if err := t.Run(ctx, args...); err != nil {
			return err
		}
		// The concat demuxer reads these as literal paths; single quotes in a
		// temp path would end the field early.
		fmt.Fprintf(&list, "file '%s'\n", strings.ReplaceAll(part, "'", `'\''`))
	}
	if t.DryRun {
		return nil
	}

	listPath := filepath.Join(dir, "concat.txt")
	if err := os.WriteFile(listPath, []byte(list.String()), 0o644); err != nil {
		return err
	}
	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(t.BaseArgs(),
			"-f", "concat", "-safe", "0", "-i", listPath,
			"-map", "0", "-c", "copy",
		)
		return append(args, faststart(output, tmp)...)
	})
}

// faststart appends the output path, moving the MP4 index to the front so the
// file can start playing before it has fully downloaded. Harmless elsewhere,
// but only MP4-family muxers accept the flag.
func faststart(output, tmp string) []string {
	switch strings.ToLower(filepath.Ext(output)) {
	case ".mp4", ".m4v", ".mov", ".m4a":
		return []string{"-movflags", "+faststart", tmp}
	default:
		return []string{tmp}
	}
}

// writeTempFile stores content in a temp file and returns its path with a
// cleanup func.
func writeTempFile(pattern, content string) (string, func(), error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	cleanup := func() { os.Remove(path) }
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
