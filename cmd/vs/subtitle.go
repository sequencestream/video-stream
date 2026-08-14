package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sequencestream/video-stream/internal/ffmpeg"
	"github.com/sequencestream/video-stream/internal/transcript"
)

type subtitleOptions struct {
	asr    asrFlags
	mode   string
	subs   string
	out    string
	outDir string
	keep   bool
	lang   string

	maxChars int
	maxLines int

	font         string
	fontSize     int
	color        string
	outlineColor string
	outline      float64
	shadow       float64
	marginV      int
	position     string
	bold         bool
	fontsDir     string

	crf    int
	preset string
}

type subtitleResult struct {
	Input    string `json:"input"`
	Output   string `json:"output"`
	Mode     string `json:"mode"`
	Subtitle string `json:"subtitle_file,omitempty"`
	Cues     int    `json:"cues"`
}

// Subtitle modes.
const (
	modeBurn = "burn"
	modeSoft = "soft"
	modeFile = "file"
)

func subtitleCommand() *Command {
	var opts subtitleOptions
	return &Command{
		Name:    "subtitle",
		Aliases: []string{"sub"},
		Group:   groupSpeech,
		Summary: "Put subtitles on a video, burned in or as a selectable track",
		Args:    "<input>...",
		Long: `Recognizes the speech if it has to, breaks it into readable captions, and
renders them onto the video.

Three modes:

  -mode burn   draw the captions into the pixels (the default). They survive
               every platform, including the ones that strip subtitle tracks,
               and can never be switched off. The video is re-encoded.
  -mode soft   add a selectable subtitle track. Fast and reversible, and
               invisible on a player that ignores subtitle tracks.
  -mode file   write only the .srt and leave the video alone.

Captions are re-broken from the recognizer's segments rather than used as-is:
a recognizer segments on pauses, which regularly produces twenty seconds of
text at once. -max-chars and -max-lines control the result.

If a transcript JSON sits next to the input, it is used and recognition is
skipped. Pass -sub to supply a subtitle file you already have.`,
		Examples: []Example{
			{Command: "vs subtitle talk.mp4", Note: "burn in captions, transcribing first if needed"},
			{Command: "vs subtitle -mode soft -lang zho talk.mp4", Note: "add a selectable track instead"},
			{Command: "vs subtitle -mode file -o talk.srt talk.mp4", Note: "just produce the subtitle file"},
			{Command: "vs subtitle -font 'PingFang SC' -font-size 56 -position bottom talk.mp4", Note: "restyle"},
			{Command: "vs subtitle -sub talk.srt -f talk.mp4", Note: "burn in subtitles you already edited by hand"},
		},
		Setup: func(fs *flag.FlagSet) {
			opts.asr.register(fs)
			fs.StringVar(&opts.mode, "mode", modeBurn, "burn, soft or file")
			fs.StringVar(&opts.subs, "sub", "", "use this subtitle file instead of recognizing speech")
			fs.StringVar(&opts.out, "o", "", "output file; only valid with a single input")
			fs.StringVar(&opts.outDir, "outdir", "", "directory for the outputs (default: beside each input)")
			fs.BoolVar(&opts.keep, "keep-srt", false, "also write the generated .srt beside the output")
			fs.StringVar(&opts.lang, "lang-tag", "", "language tag stored on a soft subtitle track, e.g. zho or eng")

			fs.IntVar(&opts.maxChars, "max-chars", 0, "characters per line before wrapping")
			fs.IntVar(&opts.maxLines, "max-lines", 0, "lines per caption")

			fs.StringVar(&opts.font, "font", "", "font family name, as installed on this machine")
			fs.IntVar(&opts.fontSize, "font-size", 0, "font size in points")
			fs.StringVar(&opts.color, "color", "", "text color as ASS &HBBGGRR, e.g. &H00FFFFFF for white")
			fs.StringVar(&opts.outlineColor, "outline-color", "", "outline color as ASS &HBBGGRR")
			fs.Float64Var(&opts.outline, "outline", -1, "outline thickness in pixels")
			fs.Float64Var(&opts.shadow, "shadow", -1, "shadow distance in pixels")
			fs.IntVar(&opts.marginV, "margin", 0, "distance from the frame edge in pixels")
			fs.StringVar(&opts.position, "position", "", "bottom, center or top")
			fs.BoolVar(&opts.bold, "bold", false, "render the captions bold")
			fs.StringVar(&opts.fontsDir, "fonts-dir", "", "directory of font files, for a font that is not installed")

			fs.IntVar(&opts.crf, "crf", 0, "x264 quality: lower is better, 18 is visually lossless")
			fs.StringVar(&opts.preset, "preset", "", "x264 speed preset: ultrafast .. veryslow")
		},
		Run: func(ctx context.Context, env *Env, args []string) error {
			return runSubtitle(ctx, env, &opts, args)
		},
	}
}

func runSubtitle(ctx context.Context, env *Env, opts *subtitleOptions, args []string) error {
	inputs, err := requireInputs("subtitle", args)
	if err != nil {
		return err
	}
	mode := strings.ToLower(strings.TrimSpace(opts.mode))
	switch mode {
	case modeBurn, modeSoft, modeFile:
	default:
		return fmt.Errorf("unknown -mode %q: want burn, soft or file", opts.mode)
	}
	if opts.subs != "" && len(inputs) > 1 {
		return fmt.Errorf("-sub names one subtitle file but %d inputs were given", len(inputs))
	}
	if err := preflight(env); err != nil {
		return err
	}

	tag, ext := "sub", ""
	if mode == modeFile {
		tag, ext = "", ".srt"
	}
	outputs, err := resolveOutputs(inputs, opts.out, opts.outDir, tag, ext)
	if err != nil {
		return err
	}

	style := opts.style(env)
	lines := lineOptions(env.Config, opts.maxChars, opts.maxLines)
	enc := encodeFromConfig(env.Config, opts.crf, opts.preset)

	results := make([]subtitleResult, 0, len(inputs))
	for i, in := range inputs {
		out := outputs[i]
		result := subtitleResult{Input: in, Output: out, Mode: mode}

		// A supplied subtitle file is used verbatim: it has probably been
		// hand-corrected, and re-breaking it would undo that work.
		subPath := opts.subs
		var cleanup func()
		if subPath == "" {
			t, _, err := resolveTranscript(ctx, env, in, &opts.asr)
			if err != nil {
				return err
			}
			subs := t.Subtitles(lines)
			if len(subs) == 0 {
				return fmt.Errorf("%s: no speech was recognized, so there is nothing to caption", in)
			}
			result.Cues = len(subs)

			if mode == modeFile {
				if !env.Force {
					if err := refuseExisting(out); err != nil {
						return err
					}
				}
				if err := t.WriteAs(out, transcript.FormatSRT, lines); err != nil {
					return err
				}
				result.Subtitle = out
				results = append(results, result)
				env.Printf("%s\n  cues       %d\n", out, len(subs))
				continue
			}

			subPath, cleanup, err = writeTempSRT(t, lines)
			if err != nil {
				return err
			}
			defer cleanup()

			if opts.keep {
				kept := strings.TrimSuffix(out, extOf(out)) + ".srt"
				if err := t.WriteAs(kept, transcript.FormatSRT, lines); err != nil {
					return err
				}
				result.Subtitle = kept
			}
		} else if mode == modeFile {
			return fmt.Errorf("-mode file with -sub would just copy %s", opts.subs)
		}

		switch mode {
		case modeBurn:
			env.Progress("burning subtitles into %s\n", out)
			err = env.FFmpeg.BurnSubtitles(ctx, in, out, subPath, style, enc)
		case modeSoft:
			env.Progress("adding a subtitle track to %s\n", out)
			err = env.FFmpeg.EmbedSubtitles(ctx, in, out, subPath, opts.lang)
		}
		if err != nil {
			return err
		}

		results = append(results, result)
		env.Printf("%s\n", out)
		env.Printf("  mode       %s\n", mode)
		if result.Cues > 0 {
			env.Printf("  cues       %d\n", result.Cues)
		}
	}
	return emitResults(env, results)
}

// style resolves the caption look from the config and the flags. The negative
// defaults on -outline and -shadow exist so that "0" stays a usable value:
// asking for no outline at all has to be distinguishable from not asking.
func (o *subtitleOptions) style(env *Env) ffmpeg.SubtitleStyle {
	cfg := env.Config.Subtitle
	style := ffmpeg.SubtitleStyle{
		Font:         firstNonEmpty(o.font, cfg.Font),
		FontSize:     pickInt(o.fontSize, cfg.FontSize),
		PrimaryColor: firstNonEmpty(o.color, cfg.PrimaryColor),
		OutlineColor: firstNonEmpty(o.outlineColor, cfg.OutlineColor),
		Outline:      cfg.Outline,
		Shadow:       cfg.Shadow,
		MarginV:      pickInt(o.marginV, cfg.MarginV),
		Position:     firstNonEmpty(o.position, cfg.Position),
		Bold:         o.bold,
		FontsDir:     o.fontsDir,
	}
	if o.outline >= 0 {
		style.Outline = o.outline
	}
	if o.shadow >= 0 {
		style.Shadow = o.shadow
	}
	return style
}

// writeTempSRT stages captions for ffmpeg to read.
func writeTempSRT(t transcript.Transcript, lines transcript.LineOptions) (string, func(), error) {
	f, err := os.CreateTemp("", "vs-cues-*.srt")
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	cleanup := func() { os.Remove(path) }
	if err := transcript.WriteSRT(f, t.Subtitles(lines)); err != nil {
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

func extOf(path string) string {
	if i := strings.LastIndex(path, "."); i > strings.LastIndex(path, "/") {
		return path[i:]
	}
	return ""
}
