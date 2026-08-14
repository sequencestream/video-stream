package main

import (
	"context"
	"flag"

	"github.com/sequencestream/video-stream/internal/transcript"
)

type transcribeOptions struct {
	asr      asrFlags
	format   string
	out      string
	outDir   string
	maxChars int
	maxLines int
}

// transcribeResult is the -json document for one input.
type transcribeResult struct {
	Input      string `json:"input"`
	Output     string `json:"output"`
	Language   string `json:"language,omitempty"`
	Model      string `json:"model,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Cues       int    `json:"cues"`
	Words      int    `json:"words"`
	Text       string `json:"text,omitempty"`
}

func transcribeCommand() *Command {
	var opts transcribeOptions
	return &Command{
		Name:    "transcribe",
		Aliases: []string{"asr"},
		Group:   groupSpeech,
		Summary: "Recognize the speech in a video and write it out with timings",
		Args:    "<input>...",
		Long: `Pulls the audio out with ffmpeg and runs it through faster-whisper,
producing word-level timings.

The default output is vs's own JSON, and it is worth keeping: it is the only
format that carries per-word timing, which is what every other command needs.
` + "`vs subtitle`" + ` and ` + "`vs filler`" + ` both pick up a transcript sitting next to
their input automatically, so transcribing once up front means the slow step
happens once no matter how many times you re-cut or restyle.

Recognition needs faster-whisper installed for the configured interpreter:

  python3 -m pip install faster-whisper

The first run for a given model downloads it, which needs network access; every
run after that is offline. Run ` + "`vs doctor`" + ` to check the setup.

Two things are worth knowing about Chinese. Whisper often returns traditional
characters regardless of what was said; a -prompt written in simplified
characters pulls it back. And the small models mangle names and technical terms
badly enough to be worth the wait for -model large-v3.`,
		Examples: []Example{
			{Command: "vs transcribe talk.mp4", Note: "write talk.json beside the video"},
			{Command: "vs transcribe -format srt -o talk.srt talk.mp4", Note: "produce subtitles directly"},
			{Command: "vs transcribe -lang zh -model large-v3 talk.mp4", Note: "skip language detection, use the best model"},
			{Command: "vs transcribe -prompt '以下是简体中文的普通话内容。' talk.mp4", Note: "keep the output in simplified characters"},
			{Command: "vs transcribe -prompt '沈括, 梦溪笔谈' lecture.mp4", Note: "bias the vocabulary toward names the speaker uses"},
			{Command: "vs transcribe -outdir out/ *.mp4", Note: "batch a directory of recordings"},
		},
		Setup: func(fs *flag.FlagSet) {
			opts.asr.register(fs)
			fs.StringVar(&opts.format, "format", "", "output format: json, srt, vtt or txt (inferred from -o, else json)")
			fs.StringVar(&opts.out, "o", "", "output file; only valid with a single input")
			fs.StringVar(&opts.outDir, "outdir", "", "directory for the outputs (default: beside each input)")
			fs.IntVar(&opts.maxChars, "max-chars", 0, "characters per subtitle line, for srt and vtt output")
			fs.IntVar(&opts.maxLines, "max-lines", 0, "lines per caption, for srt and vtt output")
		},
		Run: func(ctx context.Context, env *Env, args []string) error {
			return runTranscribe(ctx, env, &opts, args)
		},
	}
}

func runTranscribe(ctx context.Context, env *Env, opts *transcribeOptions, args []string) error {
	inputs, err := requireInputs("transcribe", args)
	if err != nil {
		return err
	}
	if err := preflight(env); err != nil {
		return err
	}

	// The format decides the extension, so it has to be settled before output
	// paths are derived.
	format := transcript.FormatJSON
	switch {
	case opts.format != "":
		if format, err = transcript.ParseFormat(opts.format); err != nil {
			return err
		}
	case opts.out != "":
		format = transcript.FormatForPath(opts.out)
	}

	// A transcript's natural name is the input's with a new extension: there is
	// only ever one transcript per file, so a tag would be noise.
	outputs, err := resolveOutputs(inputs, opts.out, opts.outDir, "", format.Ext())
	if err != nil {
		return err
	}

	lines := lineOptions(env.Config, opts.maxChars, opts.maxLines)
	asrOpts := opts.asr.options(env.Config, env.wantsProgress())

	results := make([]transcribeResult, 0, len(inputs))
	for i, in := range inputs {
		out := outputs[i]
		if !env.Force {
			if err := refuseExisting(out); err != nil {
				return err
			}
		}

		result, err := transcribeFile(ctx, env, in, asrOpts)
		if err != nil {
			return err
		}
		if err := result.WriteAs(out, format, lines); err != nil {
			return err
		}

		summary := transcribeResult{
			Input: in, Output: out,
			Language: result.Language, Model: result.Model,
			DurationMS: result.DurationMS,
			Cues:       len(result.Cues), Words: len(result.Words()),
		}
		if format == transcript.FormatText {
			summary.Text = result.Text()
		}
		results = append(results, summary)

		env.Printf("%s\n", out)
		env.Printf("  language   %s\n", orNA(result.Language))
		env.Printf("  duration   %s\n", humanMS(result.DurationMS))
		env.Printf("  cues       %d (%d words)\n", len(result.Cues), len(result.Words()))
	}
	return emitResults(env, results)
}

func orNA(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
