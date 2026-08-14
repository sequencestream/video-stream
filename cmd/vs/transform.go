package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sequencestream/video-stream/internal/ffmpeg"
)

// mediaResult is the -json document for the transform commands, which all
// answer the same question: what went in, what came out, how long is it.
type mediaResult struct {
	Input    []string `json:"input"`
	Output   string   `json:"output"`
	SourceMS int64    `json:"source_ms,omitempty"`
	OutputMS int64    `json:"output_ms,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

func resizeCommand() *Command {
	var (
		size, fit, background string
		out, outDir           string
		crf                   int
		preset                string
	)
	return &Command{
		Name:    "resize",
		Aliases: []string{"scale"},
		Group:   groupTransform,
		Summary: "Re-frame a video to another resolution or aspect ratio",
		Args:    "<input>...",
		Long: `Scales the video to a target frame, deciding what to do about the aspect
ratio the source does not share.

  -fit cover     fill the frame and crop the overflow (the default)
  -fit contain   fit the whole picture in and pad the rest
  -fit stretch   distort to fill exactly

-size takes WxH, or one of the names for the shapes platforms ask for: 720p,
1080p, 1440p, 4k, vertical (1080x1920), square.

Turning a landscape talk into vertical with -fit cover crops the left and right
thirds away, which is fine for a face in the middle and wrong for a slide. Check
one before batching a hundred.`,
		Examples: []Example{
			{Command: "vs resize -size vertical talk.mp4", Note: "landscape to 1080x1920 for shorts"},
			{Command: "vs resize -size 720p -fit contain -bg white slides.mp4", Note: "letterbox instead of cropping"},
			{Command: "vs resize -size 1080p -outdir out/ *.mov", Note: "normalize a folder of clips"},
		},
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&size, "size", "", "target frame: WxH, or 720p, 1080p, 4k, vertical, square")
			fs.StringVar(&fit, "fit", "cover", "cover, contain or stretch")
			fs.StringVar(&background, "bg", "black", "padding color for -fit contain")
			fs.StringVar(&out, "o", "", "output file; only valid with a single input")
			fs.StringVar(&outDir, "outdir", "", "directory for the outputs (default: beside each input)")
			fs.IntVar(&crf, "crf", 0, "x264 quality: lower is better, 18 is visually lossless")
			fs.StringVar(&preset, "preset", "", "x264 speed preset: ultrafast .. veryslow")
		},
		Run: func(ctx context.Context, env *Env, args []string) error {
			inputs, err := requireInputs("resize", args)
			if err != nil {
				return err
			}
			width, height, err := ffmpeg.ParseSize(size)
			if err != nil {
				return err
			}
			mode, err := ffmpeg.ParseFit(fit)
			if err != nil {
				return err
			}
			if err := preflight(env); err != nil {
				return err
			}
			outputs, err := resolveOutputs(inputs, out, outDir, fmt.Sprintf("%dx%d", width, height), "")
			if err != nil {
				return err
			}
			enc := encodeFromConfig(env.Config, crf, preset)

			results := make([]mediaResult, 0, len(inputs))
			for i, in := range inputs {
				env.Progress("resizing %s to %dx%d\n", filepath.Base(in), width, height)
				if err := env.FFmpeg.Resize(ctx, in, outputs[i], width, height, mode, background, enc); err != nil {
					return err
				}
				results = append(results, mediaResult{
					Input: []string{in}, Output: outputs[i],
					Detail: fmt.Sprintf("%dx%d %s", width, height, mode),
				})
				env.Printf("%s\n  frame      %dx%d (%s)\n", outputs[i], width, height, mode)
			}
			return emitResults(env, results)
		},
	}
}

func speedCommand() *Command {
	var (
		rate        float64
		out, outDir string
		crf         int
		preset      string
	)
	return &Command{
		Name:    "speed",
		Group:   groupTransform,
		Summary: "Speed a video up or slow it down, audio included",
		Args:    "<input>...",
		Long: `Changes the playback rate of the video and the audio together, keeping them
in sync and keeping the audio's pitch.

Rates above 2 or below 0.5 are chained internally, because ffmpeg's audio tempo
filter only accepts that range in one step — 4× is two doublings, not one
quadrupling.

Frames are dropped or repeated rather than interpolated, so a large slowdown
looks stuttery. That is the honest result of not having frames that were never
recorded.`,
		Examples: []Example{
			{Command: "vs speed -rate 1.5 lecture.mp4", Note: "50% faster"},
			{Command: "vs speed -rate 0.5 -o slowmo.mp4 action.mp4", Note: "half speed"},
			{Command: "vs speed -rate 4 screencast.mp4", Note: "a long screen recording as a time-lapse"},
		},
		Setup: func(fs *flag.FlagSet) {
			fs.Float64Var(&rate, "rate", 0, "playback rate: 2 is twice as fast, 0.5 is half speed")
			fs.StringVar(&out, "o", "", "output file; only valid with a single input")
			fs.StringVar(&outDir, "outdir", "", "directory for the outputs (default: beside each input)")
			fs.IntVar(&crf, "crf", 0, "x264 quality: lower is better, 18 is visually lossless")
			fs.StringVar(&preset, "preset", "", "x264 speed preset: ultrafast .. veryslow")
		},
		Run: func(ctx context.Context, env *Env, args []string) error {
			inputs, err := requireInputs("speed", args)
			if err != nil {
				return err
			}
			if rate <= 0 {
				return fmt.Errorf("pass -rate: 2 for twice as fast, 0.5 for half speed")
			}
			if err := preflight(env); err != nil {
				return err
			}
			tag := strings.ReplaceAll(fmt.Sprintf("%gx", rate), ".", "_")
			outputs, err := resolveOutputs(inputs, out, outDir, tag, "")
			if err != nil {
				return err
			}
			enc := encodeFromConfig(env.Config, crf, preset)

			results := make([]mediaResult, 0, len(inputs))
			for i, in := range inputs {
				media, err := env.FFmpeg.Probe(ctx, in)
				if err != nil {
					return err
				}
				env.Progress("re-timing %s at %gx\n", filepath.Base(in), rate)
				if err := env.FFmpeg.Speed(ctx, in, outputs[i], rate, media, enc); err != nil {
					return err
				}
				expected := int64(float64(media.DurationMS) / rate)
				results = append(results, mediaResult{
					Input: []string{in}, Output: outputs[i],
					SourceMS: media.DurationMS, OutputMS: expected,
					Detail: fmt.Sprintf("%gx", rate),
				})
				env.Printf("%s\n  duration   %s (was %s)\n", outputs[i], humanMS(expected), humanMS(media.DurationMS))
			}
			return emitResults(env, results)
		},
	}
}

func concatCommand() *Command {
	var (
		out  string
		size string
		fit  string
		fast bool

		crf    int
		preset string
	)
	return &Command{
		Name:    "concat",
		Aliases: []string{"join"},
		Group:   groupTransform,
		Summary: "Join several videos into one, in the order given",
		Args:    "<input> <input>...",
		Long: `Concatenates the inputs end to end.

By default everything is re-encoded through a filter graph, which is what makes
it work on a folder of clips that differ in resolution, frame rate or codec.
Pass -size to normalize them all to one frame first.

-fast joins the compressed streams without touching them: instant, and correct
only when every input already shares the same codec, resolution and frame rate.
On mismatched inputs it produces a file that plays the first clip and then
falls apart.`,
		Examples: []Example{
			{Command: "vs concat -o full.mp4 part1.mp4 part2.mp4 part3.mp4"},
			{Command: "vs concat -size 1080p -o reel.mp4 clip*.mov", Note: "normalize mismatched clips while joining"},
			{Command: "vs concat -fast -o out.mp4 seg*.mp4", Note: "same encoder settings throughout, so copy them"},
		},
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&out, "o", "", "output file (required)")
			fs.StringVar(&size, "size", "", "normalize every input to this frame first: WxH or 720p, 1080p, vertical")
			fs.StringVar(&fit, "fit", "cover", "cover, contain or stretch, when -size is given")
			fs.BoolVar(&fast, "fast", false, "stream copy; requires identical codecs and geometry")
			fs.IntVar(&crf, "crf", 0, "x264 quality: lower is better, 18 is visually lossless")
			fs.StringVar(&preset, "preset", "", "x264 speed preset: ultrafast .. veryslow")
		},
		Run: func(ctx context.Context, env *Env, args []string) error {
			inputs, err := requireInputs("concat", args)
			if err != nil {
				return err
			}
			if len(inputs) < 2 {
				return fmt.Errorf("concat needs at least two inputs, got %d", len(inputs))
			}
			if strings.TrimSpace(out) == "" {
				return fmt.Errorf("pass -o to name the joined output")
			}
			var width, height int
			if size != "" {
				if width, height, err = ffmpeg.ParseSize(size); err != nil {
					return err
				}
			}
			mode, err := ffmpeg.ParseFit(fit)
			if err != nil {
				return err
			}
			if err := preflight(env); err != nil {
				return err
			}
			for _, in := range inputs {
				if sameFile(in, out) {
					return fmt.Errorf("output %s is also an input", out)
				}
			}

			enc := encodeFromConfig(env.Config, crf, preset)
			env.Progress("joining %d files into %s\n", len(inputs), out)
			if err := env.FFmpeg.ConcatFiles(ctx, inputs, out, width, height, mode, fast, enc); err != nil {
				return err
			}

			var total int64
			for _, in := range inputs {
				if media, err := env.FFmpeg.Probe(ctx, in); err == nil {
					total += media.DurationMS
				}
			}
			env.Printf("%s\n  parts      %d\n  duration   %s\n", out, len(inputs), humanMS(total))
			return emitResults(env, []mediaResult{{Input: inputs, Output: out, OutputMS: total}})
		},
	}
}

func audioCommand() *Command {
	var (
		format      string
		rate        int
		channels    int
		out, outDir string
	)
	return &Command{
		Name:    "audio",
		Group:   groupTransform,
		Summary: "Pull the audio track out of a video",
		Args:    "<input>...",
		Long: `Writes the input's audio to a standalone file.

The default is WAV, which is lossless and is what a transcription or editing
tool wants. -format m4a or mp3 re-encodes for something you intend to listen to
or upload.

Pass -rate 16000 -channels 1 to produce exactly what a speech model wants,
though ` + "`vs transcribe`" + ` already does that for you internally.`,
		Examples: []Example{
			{Command: "vs audio talk.mp4", Note: "write talk.wav"},
			{Command: "vs audio -format mp3 -o podcast.mp3 episode.mkv"},
			{Command: "vs audio -rate 16000 -channels 1 interview.mp4", Note: "mono 16 kHz, ready for a speech model"},
		},
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&format, "format", "wav", "wav, mp3, m4a or flac")
			fs.IntVar(&rate, "rate", 0, "sample rate in Hz; 0 keeps the source's")
			fs.IntVar(&channels, "channels", 0, "channel count; 0 keeps the source's")
			fs.StringVar(&out, "o", "", "output file; only valid with a single input")
			fs.StringVar(&outDir, "outdir", "", "directory for the outputs (default: beside each input)")
		},
		Run: func(ctx context.Context, env *Env, args []string) error {
			inputs, err := requireInputs("audio", args)
			if err != nil {
				return err
			}
			ext := "." + strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), "."))
			if out != "" {
				ext = filepath.Ext(out)
			}
			if err := preflight(env); err != nil {
				return err
			}
			outputs, err := resolveOutputs(inputs, out, outDir, "", ext)
			if err != nil {
				return err
			}

			results := make([]mediaResult, 0, len(inputs))
			for i, in := range inputs {
				media, err := env.FFmpeg.Probe(ctx, in)
				if err != nil {
					return err
				}
				if !media.HasAudio() {
					return errNoAudio(in)
				}
				env.Progress("extracting audio from %s\n", filepath.Base(in))
				if err := extractAudioAs(ctx, env, in, outputs[i], rate, channels); err != nil {
					return err
				}
				results = append(results, mediaResult{
					Input: []string{in}, Output: outputs[i], OutputMS: media.DurationMS,
				})
				env.Printf("%s\n  duration   %s\n", outputs[i], humanMS(media.DurationMS))
			}
			return emitResults(env, results)
		},
	}
}

// extractAudioAs writes WAV through the PCM path and anything else through the
// container's default encoder, which is what ffmpeg picks from the extension.
func extractAudioAs(ctx context.Context, env *Env, input, output string, rate, channels int) error {
	if strings.EqualFold(filepath.Ext(output), ".wav") {
		return env.FFmpeg.ExtractAudio(ctx, input, output, rate, channels)
	}
	return env.FFmpeg.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(env.FFmpeg.BaseArgs(), "-i", input, "-vn", "-map", "0:a:0")
		if channels > 0 {
			args = append(args, "-ac", fmt.Sprint(channels))
		}
		if rate > 0 {
			args = append(args, "-ar", fmt.Sprint(rate))
		}
		if bitrate := env.Config.Encode.AudioBitrate; bitrate != "" {
			args = append(args, "-b:a", bitrate)
		}
		return append(args, tmp)
	})
}
