package main

import (
	"context"
	"flag"
	"time"

	"github.com/sequencestream/video-stream/internal/ffmpeg"
	"github.com/sequencestream/video-stream/internal/timespan"
)

type silenceOptions struct {
	threshold float64
	minLen    time.Duration
	maxPause  time.Duration
	pad       time.Duration
	minKeep   time.Duration
	list      bool
	fast      bool
	out       string
	outDir    string
	crf       int
	preset    string
}

type silenceResult struct {
	Input     string          `json:"input"`
	Output    string          `json:"output,omitempty"`
	SourceMS  int64           `json:"source_ms"`
	OutputMS  int64           `json:"output_ms"`
	RemovedMS int64           `json:"removed_ms"`
	Silences  timespan.Ranges `json:"silences"`
}

func silenceCommand() *Command {
	var opts silenceOptions
	return &Command{
		Name:    "silence",
		Group:   groupCut,
		Summary: "Shorten or remove the silent stretches in a recording",
		Args:    "<input>...",
		Long: `Finds silence with ffmpeg's own silencedetect filter and splices it out. No
speech recognition is involved, so it works on any audio in any language and
costs one pass over the file.

Silence is shortened to -max-pause rather than closed completely. Removing a
pause entirely makes speech sound spliced, and the gap where someone breathes
is part of how a sentence reads.

Reach for this on a screen recording or a long take with dead air. For the
noises between words — 嗯, 呃, a stuttered word — use ` + "`vs filler`" + ` instead:
those are speech, so silencedetect cannot see them.

-threshold is the level below which audio counts as silent, in dBFS. -30 is
room tone in a quiet room; -50 catches only true digital silence. If nothing is
found, the recording has a noise floor above the threshold: raise it toward
zero.`,
		Examples: []Example{
			{Command: "vs silence -list recording.mp4", Note: "show the silent stretches, change nothing"},
			{Command: "vs silence recording.mp4", Note: "write recording.tight.mp4"},
			{Command: "vs silence -threshold -35 -min 1s screencast.mp4", Note: "only cut silences of a second or more"},
			{Command: "vs silence -max-pause 0 -fast raw.mp4", Note: "close the gaps entirely, using keyframe-snapped stream copy"},
		},
		Setup: func(fs *flag.FlagSet) {
			fs.Float64Var(&opts.threshold, "threshold", -30, "level below which audio counts as silence, in dBFS")
			fs.DurationVar(&opts.minLen, "min", 500*time.Millisecond, "ignore silences shorter than this")
			fs.DurationVar(&opts.maxPause, "max-pause", 300*time.Millisecond, "how much of each silence to leave in place")
			fs.DurationVar(&opts.pad, "pad", 60*time.Millisecond, "pull each cut back from the speech on either side")
			fs.DurationVar(&opts.minKeep, "min-keep", 200*time.Millisecond, "drop surviving fragments shorter than this")
			fs.BoolVar(&opts.list, "list", false, "print the silent stretches and stop")
			fs.BoolVar(&opts.fast, "fast", false, "stream copy instead of re-encoding; cuts snap to keyframes")
			fs.StringVar(&opts.out, "o", "", "output file; only valid with a single input")
			fs.StringVar(&opts.outDir, "outdir", "", "directory for the outputs (default: beside each input)")
			fs.IntVar(&opts.crf, "crf", 0, "x264 quality: lower is better, 18 is visually lossless")
			fs.StringVar(&opts.preset, "preset", "", "x264 speed preset: ultrafast .. veryslow")
		},
		Run: func(ctx context.Context, env *Env, args []string) error {
			return runSilence(ctx, env, &opts, args)
		},
	}
}

func runSilence(ctx context.Context, env *Env, opts *silenceOptions, args []string) error {
	inputs, err := requireInputs("silence", args)
	if err != nil {
		return err
	}
	if err := preflight(env); err != nil {
		return err
	}
	outputs, err := resolveOutputs(inputs, opts.out, opts.outDir, "tight", "")
	if err != nil {
		return err
	}
	enc := encodeFromConfig(env.Config, opts.crf, opts.preset)

	results := make([]silenceResult, 0, len(inputs))
	for i, in := range inputs {
		media, err := env.FFmpeg.Probe(ctx, in)
		if err != nil {
			return err
		}
		if !media.HasAudio() {
			return errNoAudio(in)
		}

		env.Progress("scanning %s for silence\n", in)
		silences, err := env.FFmpeg.DetectSilence(ctx, in, opts.threshold, opts.minLen.Milliseconds(), media)
		if err != nil {
			return err
		}

		cuts := shortenSilences(silences, opts.maxPause)
		keep := cuts.Shrink(opts.pad, opts.pad).
			Invert(media.DurationMS).
			DropShorterThan(opts.minKeep.Milliseconds())

		result := silenceResult{
			Input: in, SourceMS: media.DurationMS,
			OutputMS: keep.Total(), Silences: silences,
		}
		result.RemovedMS = result.SourceMS - result.OutputMS

		env.Printf("%s\n", in)
		for _, s := range silences {
			env.Printf("  %s  silence  %s\n", timespan.FormatTime(s.StartMS), humanMS(s.Duration()))
		}
		if len(silences) == 0 {
			env.Printf("  no silence found below %gdB; raise -threshold toward zero if the room is noisy\n", opts.threshold)
		} else {
			env.Printf("  ----\n")
			env.Printf("  silences   %d\n", len(silences))
			env.Printf("  removed    %s of %s (%s)\n",
				humanMS(result.RemovedMS), humanMS(result.SourceMS), percent(result.RemovedMS, result.SourceMS))
		}

		if opts.list {
			results = append(results, result)
			continue
		}

		// Written even when nothing was cut, so a chained command always finds
		// the file it expects. With no cuts this is a stream copy.
		out := outputs[i]
		result.Output = out
		if err := env.FFmpeg.Cut(ctx, in, out, keep, media, ffmpeg.CutOptions{Encode: enc, Fast: opts.fast}); err != nil {
			return err
		}
		if len(cuts) == 0 {
			env.Printf("  output     %s (unchanged copy)\n", out)
		} else {
			env.Printf("  output     %s\n", out)
		}
		results = append(results, result)
	}
	return emitResults(env, results)
}

// shortenSilences turns detected silences into cut spans, leaving maxPause of
// each one in place and taking the excess out of the middle.
func shortenSilences(silences timespan.Ranges, maxPause time.Duration) timespan.Ranges {
	limit := maxPause.Milliseconds()
	out := make(timespan.Ranges, 0, len(silences))
	for _, s := range silences {
		excess := s.Duration() - limit
		if excess <= 0 {
			continue
		}
		head := limit / 2
		out = append(out, timespan.Range{
			StartMS: s.StartMS + head,
			EndMS:   s.EndMS - (limit - head),
		})
	}
	return out.Normalize()
}
