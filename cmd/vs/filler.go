package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/sequencestream/video-stream/internal/ffmpeg"
	"github.com/sequencestream/video-stream/internal/filler"
	"github.com/sequencestream/video-stream/internal/timespan"
)

type fillerOptions struct {
	asr asrFlags

	aggressive bool
	words      string
	add        string
	keepWords  string
	noRepeats  bool

	maxPause time.Duration
	padHead  time.Duration
	padTail  time.Duration
	minKeep  time.Duration
	trimEnds bool

	list   bool
	out    string
	outDir string
	crf    int
	preset string
}

type fillerResult struct {
	Input     string         `json:"input"`
	Output    string         `json:"output,omitempty"`
	SourceMS  int64          `json:"source_ms"`
	OutputMS  int64          `json:"output_ms"`
	RemovedMS int64          `json:"removed_ms"`
	Counts    map[string]int `json:"counts"`
	Cuts      []filler.Cut   `json:"cuts"`
}

func fillerCommand() *Command {
	var opts fillerOptions
	return &Command{
		Name:    "filler",
		Group:   groupCut,
		Summary: "Cut the hesitation sounds, stutters and dead air out of a take",
		Args:    "<input>...",
		Long: `Recognizes the speech, finds the parts that carry nothing, and splices them
out. Three things get removed:

  hesitation sounds   嗯 呃 um uh — the built-in list, and only that list
  stutters            a word said twice in a row inside half a second
  long pauses         silence past -max-pause, shortened rather than closed

The default vocabulary is deliberately short: sounds nobody meant to make.
Words a speaker might have meant — 那个, 就是, "like", "basically" — are behind
-aggressive, because deleting them changes what was said rather than tidying
how it was said.

Run it with -list first. It prints every cut with its reason and timestamp and
touches nothing, which is the cheap way to find out that -aggressive is eating
half your sentences.

Cuts are frame-accurate, so the video is re-encoded. Word boundaries from a
recognizer are approximate, so every cut is pulled back from its neighbours by
-pad-head and -pad-tail before it is applied.`,
		Examples: []Example{
			{Command: "vs filler -list talk.mp4", Note: "show what would be cut, change nothing"},
			{Command: "vs filler talk.mp4", Note: "write talk.clean.mp4"},
			{Command: "vs filler -aggressive -max-pause 500ms talk.mp4", Note: "tighten it much harder"},
			{Command: "vs filler -keep-words 嗯 -add 那什么 talk.mp4", Note: "adjust the vocabulary for this speaker"},
			{Command: "vs filler -transcript talk.json -o clean.mp4 talk.mp4", Note: "reuse a transcript, name the output"},
		},
		Setup: func(fs *flag.FlagSet) {
			opts.asr.register(fs)
			fs.BoolVar(&opts.aggressive, "aggressive", false, "also cut real words used as padding (那个, 就是, like, basically)")
			fs.StringVar(&opts.words, "words", "", "replace the vocabulary entirely, comma-separated")
			fs.StringVar(&opts.add, "add", "", "add phrases to the vocabulary, comma-separated")
			fs.StringVar(&opts.keepWords, "keep-words", "", "remove phrases from the vocabulary, comma-separated")
			fs.BoolVar(&opts.noRepeats, "no-repeats", false, "leave stuttered repeats alone")

			fs.DurationVar(&opts.maxPause, "max-pause", -1, "longest silence to leave intact; 0 leaves every pause alone (default from config, else 700ms)")
			fs.DurationVar(&opts.padHead, "pad-head", -1, "pull each cut back from the word before it")
			fs.DurationVar(&opts.padTail, "pad-tail", -1, "pull each cut back from the word after it")
			fs.DurationVar(&opts.minKeep, "min-keep", -1, "drop surviving fragments shorter than this")
			fs.BoolVar(&opts.trimEnds, "trim-ends", false, "also shorten the silence before the first word and after the last")

			fs.BoolVar(&opts.list, "list", false, "print the cut plan and stop")
			fs.StringVar(&opts.out, "o", "", "output file; only valid with a single input")
			fs.StringVar(&opts.outDir, "outdir", "", "directory for the outputs (default: beside each input)")
			fs.IntVar(&opts.crf, "crf", 0, "x264 quality: lower is better, 18 is visually lossless")
			fs.StringVar(&opts.preset, "preset", "", "x264 speed preset: ultrafast .. veryslow")
		},
		Run: func(ctx context.Context, env *Env, args []string) error {
			return runFiller(ctx, env, &opts, args)
		},
	}
}

func runFiller(ctx context.Context, env *Env, opts *fillerOptions, args []string) error {
	inputs, err := requireInputs("filler", args)
	if err != nil {
		return err
	}
	if err := preflight(env); err != nil {
		return err
	}

	outputs, err := resolveOutputs(inputs, opts.out, opts.outDir, "clean", "")
	if err != nil {
		return err
	}
	enc := encodeFromConfig(env.Config, opts.crf, opts.preset)

	results := make([]fillerResult, 0, len(inputs))
	for i, in := range inputs {
		media, err := env.FFmpeg.Probe(ctx, in)
		if err != nil {
			return err
		}
		t, _, err := resolveTranscript(ctx, env, in, &opts.asr)
		if err != nil {
			return err
		}

		plan, err := filler.Detect(t, opts.detect(env, media.DurationMS))
		if err != nil {
			return err
		}

		result := fillerResult{
			Input: in, SourceMS: plan.SourceMS, OutputMS: plan.OutputMS,
			RemovedMS: plan.RemovedMS, Counts: plan.Counts, Cuts: plan.Cuts,
		}

		printFillerPlan(env, in, plan)
		if opts.list {
			results = append(results, result)
			continue
		}

		// The output is written even when nothing was cut, and it is a stream
		// copy in that case. A command in a chain has to leave a file behind
		// under a predictable name, or `vs filler x.mp4 && vs subtitle
		// x.clean.mp4` breaks on exactly the takes that needed no cleaning.
		out := outputs[i]
		result.Output = out
		env.Progress("writing %s\n", out)
		if err := env.FFmpeg.Cut(ctx, in, out, plan.Keep, media, ffmpeg.CutOptions{Encode: enc}); err != nil {
			return err
		}
		if len(plan.Cuts) == 0 {
			env.Printf("  output     %s (unchanged copy)\n", out)
		} else {
			env.Printf("  output     %s\n", out)
		}
		results = append(results, result)
	}
	return emitResults(env, results)
}

// detect resolves the detection settings from the config and the flags. The
// negative defaults keep zero meaningful: -pad-head 0 must mean "no padding",
// not "unset".
func (o *fillerOptions) detect(env *Env, totalMS int64) filler.Options {
	cfg := env.Config.Filler
	opts := filler.DefaultOptions(totalMS)
	opts.Aggressive = o.aggressive
	opts.Repeats = !o.noRepeats
	opts.TrimEnds = o.trimEnds
	opts.Only = splitList(o.words)
	opts.Extra = append(splitList(o.add), cfg.ExtraWords...)
	opts.Keep = append(splitList(o.keepWords), cfg.KeepWords...)

	opts.MaxPause = pickDuration(o.maxPause, cfg.MaxPause)
	opts.PadHead = pickDuration(o.padHead, cfg.PadHead)
	opts.PadTail = pickDuration(o.padTail, cfg.PadTail)
	opts.MinKeep = pickDuration(o.minKeep, cfg.MinKeep)
	return opts
}

// printFillerPlan renders the cut list. It prints for every run, not only for
// -list: the one number that matters is how much of the take just disappeared,
// and finding that out after the encode is too late to object.
func printFillerPlan(env *Env, input string, plan filler.Result) {
	env.Printf("%s\n", input)
	if len(plan.Cuts) == 0 {
		env.Printf("  nothing to cut: no filler words, stutters or long pauses found\n")
		return
	}
	width := 0
	for _, c := range plan.Cuts {
		width = max(width, displayWidth(c.Label()))
	}
	for _, c := range plan.Cuts {
		env.Printf("  %s  %s  -%s\n",
			timespan.FormatTime(c.StartMS), padDisplay(c.Label(), width), humanMS(c.Duration()))
	}
	env.Printf("  ----\n")
	env.Printf("  cuts       %d (%s)\n", len(plan.Cuts), countSummary(plan.Counts))
	env.Printf("  removed    %s of %s (%s)\n",
		humanMS(plan.RemovedMS), humanMS(plan.SourceMS), percent(plan.RemovedMS, plan.SourceMS))
	env.Printf("  result     %s\n", humanMS(plan.OutputMS))
}

func countSummary(counts map[string]int) string {
	var parts []string
	for _, kind := range []string{string(filler.KindFiller), string(filler.KindRepeat), string(filler.KindPause)} {
		if n := counts[kind]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, kind))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// splitList parses a comma-separated flag value, tolerating spaces around the
// entries so -add "那个, 就是" behaves as typed.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// pickDuration returns the first value that was actually set.
//
// A negative value means "not set", which is what keeps zero meaningful:
// -pad-head 0 asks for no padding, and that is a different request from not
// passing the flag at all.
func pickDuration(values ...time.Duration) time.Duration {
	for _, v := range values {
		if v >= 0 {
			return v
		}
	}
	return 0
}
