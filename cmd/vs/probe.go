package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/sequencestream/video-stream/internal/ffmpeg"
)

func probeCommand() *Command {
	var streams bool
	return &Command{
		Name:    "probe",
		Aliases: []string{"info"},
		Group:   groupInspect,
		Summary: "Show what is inside a media file",
		Args:    "<input>...",
		Long: `Reports duration, resolution, frame rate and codecs — ffprobe's answer,
without the JSON.

Worth running first when a command behaves oddly: a file with no audio track
cannot have its silence detected, and a variable-frame-rate screen recording
explains a great many surprises further down the pipeline.`,
		Examples: []Example{
			{Command: "vs probe talk.mp4"},
			{Command: "vs probe -streams talk.mp4", Note: "list every track, not just the first of each kind"},
			{Command: "vs probe -json *.mp4", Note: "machine-readable, for a batch"},
		},
		Setup: func(fs *flag.FlagSet) {
			fs.BoolVar(&streams, "streams", false, "list every stream in the file")
		},
		Run: func(ctx context.Context, env *Env, args []string) error {
			inputs, err := requireInputs("probe", args)
			if err != nil {
				return err
			}
			if err := preflight(env); err != nil {
				return err
			}

			results := make([]ffmpeg.Media, 0, len(inputs))
			for _, in := range inputs {
				media, err := env.FFmpeg.Probe(ctx, in)
				if err != nil {
					return err
				}
				results = append(results, media)
				printMedia(env, media, streams)
			}
			return emitResults(env, results)
		},
	}
}

func printMedia(env *Env, media ffmpeg.Media, allStreams bool) {
	env.Printf("%s\n", media.Path)
	env.Printf("  duration   %s\n", humanMS(media.DurationMS))
	env.Printf("  format     %s\n", media.Format)
	if media.SizeBytes > 0 {
		env.Printf("  size       %s\n", humanBytes(media.SizeBytes))
	}
	if v, ok := media.Video(); ok {
		env.Printf("  video      %s %dx%d", v.Codec, v.Width, v.Height)
		if v.FPS > 0 {
			env.Printf(" @ %.3g fps", v.FPS)
		}
		env.Printf("\n")
	} else {
		env.Printf("  video      none\n")
	}
	if a, ok := media.Audio(); ok {
		env.Printf("  audio      %s %d Hz, %d ch\n", a.Codec, a.SampleRate, a.Channels)
	} else {
		env.Printf("  audio      none\n")
	}
	if subs := media.Subtitles(); len(subs) > 0 {
		for _, s := range subs {
			env.Printf("  subtitle   %s (%s)\n", s.Codec, orNA(s.Language))
		}
	}
	if allStreams {
		for _, s := range media.Streams {
			env.Printf("  stream %-2d  %s %s\n", s.Index, s.Type, s.Codec)
		}
	}
}

// humanBytes renders a file size the way a person reads one.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
