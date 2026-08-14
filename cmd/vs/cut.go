package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/sequencestream/video-stream/internal/ffmpeg"
	"github.com/sequencestream/video-stream/internal/timespan"
)

type cutOptions struct {
	keep   stringList
	drop   stringList
	fast   bool
	out    string
	outDir string
	crf    int
	preset string
}

type cutResult struct {
	Input     string          `json:"input"`
	Output    string          `json:"output"`
	SourceMS  int64           `json:"source_ms"`
	OutputMS  int64           `json:"output_ms"`
	RemovedMS int64           `json:"removed_ms"`
	Keep      timespan.Ranges `json:"keep"`
}

// stringList collects a flag given more than once.
type stringList []string

func (s *stringList) String() string { return fmt.Sprint(*s) }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func cutCommand() *Command {
	var opts cutOptions
	return &Command{
		Name:    "cut",
		Aliases: []string{"trim"},
		Group:   groupCut,
		Summary: "Keep or drop time ranges, joining what survives",
		Args:    "<input>...",
		Long: `Takes a list of time ranges and produces the video they describe. Repeat
-keep to assemble several spans in order, or repeat -drop to remove them and
join what is left.

Timestamps accept the forms you would actually type: 90, 1:30, 00:01:30.500, or
a duration like 90s. Either side of a range may be left off, so -keep -0:30 is
the first thirty seconds and -keep 9:00- runs to the end.

By default the video is re-encoded so cuts land exactly where you asked. -fast
copies the compressed streams instead: seconds rather than minutes on a long
file, at the price of every cut snapping back to the nearest keyframe, which is
usually one to ten seconds early.`,
		Examples: []Example{
			{Command: "vs cut -keep 1:30-4:15 lecture.mp4", Note: "pull one section out"},
			{Command: "vs cut -keep 0:10-1:00 -keep 5:00-6:30 talk.mp4", Note: "assemble two spans into one file"},
			{Command: "vs cut -drop -0:12 -drop 9:40- stream.mp4", Note: "trim the intro and the outro"},
			{Command: "vs cut -fast -keep 10:00-20:00 -o clip.mp4 recording.mp4", Note: "grab a rough select without re-encoding"},
		},
		Setup: func(fs *flag.FlagSet) {
			fs.Var(&opts.keep, "keep", "a range to keep, as START-END; repeatable")
			fs.Var(&opts.drop, "drop", "a range to remove, as START-END; repeatable")
			fs.BoolVar(&opts.fast, "fast", false, "stream copy instead of re-encoding; cuts snap to keyframes")
			fs.StringVar(&opts.out, "o", "", "output file; only valid with a single input")
			fs.StringVar(&opts.outDir, "outdir", "", "directory for the outputs (default: beside each input)")
			fs.IntVar(&opts.crf, "crf", 0, "x264 quality: lower is better, 18 is visually lossless")
			fs.StringVar(&opts.preset, "preset", "", "x264 speed preset: ultrafast .. veryslow")
		},
		Run: func(ctx context.Context, env *Env, args []string) error {
			return runCut(ctx, env, &opts, args)
		},
	}
}

func runCut(ctx context.Context, env *Env, opts *cutOptions, args []string) error {
	inputs, err := requireInputs("cut", args)
	if err != nil {
		return err
	}
	if len(opts.keep) == 0 && len(opts.drop) == 0 {
		return fmt.Errorf("nothing to do: pass -keep or -drop\nRun `vs cut --help` for the range syntax.")
	}
	if len(opts.keep) > 0 && len(opts.drop) > 0 {
		// Combining them has two defensible readings — drop within the kept
		// spans, or the intersection — and guessing wrong silently produces
		// the wrong video.
		return fmt.Errorf("-keep and -drop cannot be combined; run vs cut twice instead")
	}
	if err := preflight(env); err != nil {
		return err
	}
	outputs, err := resolveOutputs(inputs, opts.out, opts.outDir, "cut", "")
	if err != nil {
		return err
	}
	enc := encodeFromConfig(env.Config, opts.crf, opts.preset)

	results := make([]cutResult, 0, len(inputs))
	for i, in := range inputs {
		media, err := env.FFmpeg.Probe(ctx, in)
		if err != nil {
			return err
		}

		// Ranges are parsed per input: an open-ended "9:00-" means a different
		// timestamp in each file of a batch.
		keep, err := parseRanges(opts.keep, media.DurationMS)
		if err != nil {
			return err
		}
		drop, err := parseRanges(opts.drop, media.DurationMS)
		if err != nil {
			return err
		}
		if len(drop) > 0 {
			keep = drop.Invert(media.DurationMS)
		}
		keep = keep.Clamp(media.DurationMS)
		if len(keep) == 0 {
			return fmt.Errorf("%s: the ranges leave nothing behind", in)
		}

		out := outputs[i]
		env.Progress("writing %d span(s) to %s\n", len(keep), out)
		if err := env.FFmpeg.Cut(ctx, in, out, keep, media, ffmpeg.CutOptions{Encode: enc, Fast: opts.fast}); err != nil {
			return err
		}

		result := cutResult{
			Input: in, Output: out, SourceMS: media.DurationMS,
			OutputMS: keep.Total(), Keep: keep,
		}
		result.RemovedMS = result.SourceMS - result.OutputMS
		results = append(results, result)

		env.Printf("%s\n", out)
		for _, r := range keep {
			env.Printf("  keep       %s  (%s)\n", r, humanMS(r.Duration()))
		}
		env.Printf("  duration   %s of %s\n", humanMS(result.OutputMS), humanMS(result.SourceMS))
	}
	return emitResults(env, results)
}

func parseRanges(specs []string, totalMS int64) (timespan.Ranges, error) {
	out := make(timespan.Ranges, 0, len(specs))
	for _, spec := range specs {
		r, err := timespan.ParseRange(spec, totalMS)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out.Normalize(), nil
}

func errNoAudio(input string) error {
	return fmt.Errorf("%s has no audio track", input)
}
