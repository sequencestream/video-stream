package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/sequencestream/video-stream/internal/asr"
	"github.com/sequencestream/video-stream/internal/config"
	"github.com/sequencestream/video-stream/internal/ffmpeg"
	"github.com/sequencestream/video-stream/internal/transcript"
)

// requireInputs validates the positional arguments as readable media files.
//
// It runs before any work so a mistyped path in the fourth file of a batch is
// reported immediately, rather than after three files have already been
// re-encoded.
func requireInputs(cmd string, args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("no input file given\n\nUsage: vs %s [flags] <input>...\nRun `vs %s --help` for details.", cmd, cmd)
	}
	for _, in := range args {
		info, err := os.Stat(in)
		if err != nil {
			return nil, fmt.Errorf("input %s: %w", in, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("input %s is a directory", in)
		}
	}
	return args, nil
}

// emitResults writes the JSON result document: an object for a single input, an
// array when several were processed. A caller handling one file should not have
// to unwrap a one-element array.
func emitResults[T any](env *Env, results []T) error {
	if !env.JSON {
		return nil
	}
	if len(results) == 1 {
		return env.EmitJSON(results[0])
	}
	return env.EmitJSON(results)
}

// asrFlags is the recognition flag group, shared by every command that may
// need to run speech recognition.
//
// Empty values mean "take it from the config", which is resolved in options()
// rather than at registration time: the config file has not been read yet when
// the flags are declared.
type asrFlags struct {
	transcriptPath string
	model          string
	language       string
	device         string
	computeType    string
	modelDir       string
	prompt         string
	threads        int
	beamSize       int
	noVAD          bool
}

func (a *asrFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&a.transcriptPath, "transcript", "", "reuse a transcript JSON instead of recognizing again")
	fs.StringVar(&a.model, "model", "", "whisper model: tiny, base, small, medium, large-v3, or a model directory (default from config, else small)")
	fs.StringVar(&a.language, "lang", "", "spoken language as a two-letter code; empty autodetects")
	fs.StringVar(&a.device, "device", "", "auto, cpu or cuda (default from config, else auto)")
	fs.StringVar(&a.computeType, "compute-type", "", "auto, int8, int8_float16, float16 or float32")
	fs.StringVar(&a.modelDir, "model-dir", "", "where to cache downloaded models")
	fs.StringVar(&a.prompt, "prompt", "", "vocabulary hint: names and jargon the speaker uses")
	fs.IntVar(&a.threads, "threads", 0, "CPU threads; 0 lets the backend decide")
	fs.IntVar(&a.beamSize, "beam", 0, "beam size; higher is slower and slightly more accurate")
	fs.BoolVar(&a.noVAD, "no-vad", false, "keep silence in the audio fed to the model (whisper invents text to fill it)")
}

// options resolves the recognition settings against the config.
func (a *asrFlags) options(cfg config.Config, progress bool) asr.Options {
	return asr.Options{
		Model:       firstNonEmpty(a.model, cfg.ASR.Model, "small"),
		Language:    firstNonEmpty(a.language, cfg.ASR.Language),
		Device:      firstNonEmpty(a.device, cfg.ASR.Device, "auto"),
		ComputeType: firstNonEmpty(a.computeType, cfg.ASR.ComputeType),
		ModelDir:    firstNonEmpty(a.modelDir, cfg.ASR.ModelDir),
		Threads:     pickInt(a.threads, cfg.ASR.Threads),
		VAD:         !a.noVAD && cfg.ASR.VAD,
		Prompt:      a.prompt,
		BeamSize:    a.beamSize,
		Progress:    progress,
	}
}

// transcribeFile extracts the audio and recognizes it.
//
// Extraction always happens even for a file that is already audio: the model
// wants 16 kHz mono PCM, and letting ffmpeg produce exactly that is cheaper
// than making the Python side decode an arbitrary container.
func transcribeFile(ctx context.Context, env *Env, input string, opts asr.Options) (transcript.Transcript, error) {
	recognizer := asr.FasterWhisper{Python: env.Config.Tools.Python}
	if err := recognizer.Check(ctx); err != nil {
		return transcript.Transcript{}, err
	}

	wav, cleanup, err := ffmpeg.TempWAV()
	if err != nil {
		return transcript.Transcript{}, err
	}
	defer cleanup()

	// Extraction must run even under -n: without the audio there is nothing to
	// recognize, and a dry run that reports a recognition it could not attempt
	// would be a lie.
	extractor := env.FFmpeg
	extractor.DryRun = false
	extractor.Overwrite = true
	env.Progress("extracting audio from %s\n", filepath.Base(input))
	if err := extractor.ExtractAudio(ctx, input, wav, ffmpeg.ASRSampleRate, 1); err != nil {
		return transcript.Transcript{}, err
	}

	env.Progress("recognizing with %s (%s)\n", opts.Model, recognizer.Name())
	result, err := recognizer.Transcribe(ctx, wav, opts)
	if err != nil {
		return transcript.Transcript{}, err
	}
	result.Source = input
	return result, nil
}

// resolveTranscript loads the transcript from -transcript, falls back to a
// sidecar JSON sitting next to the input, and only then runs recognition.
//
// The fallback is what makes an iteration loop bearable: transcribe once, then
// try five subtitle styles without waiting for the model each time.
func resolveTranscript(ctx context.Context, env *Env, input string, flags *asrFlags) (transcript.Transcript, string, error) {
	if path := strings.TrimSpace(flags.transcriptPath); path != "" {
		t, err := transcript.Load(path)
		return t, path, err
	}

	sidecar := strings.TrimSuffix(input, filepath.Ext(input)) + ".json"
	if info, err := os.Stat(sidecar); err == nil && info.Mode().IsRegular() {
		t, err := transcript.Load(sidecar)
		switch {
		case err != nil:
			// A malformed sidecar is worth mentioning but not worth failing
			// over: recognition can still produce the answer.
			env.Progress("ignoring unreadable transcript %s: %v\n", filepath.Base(sidecar), err)
		case t.Version == "" || len(t.Cues) == 0:
			// Nobody named this file; it was inferred from the video's name.
			// Some other tool's talk.json would otherwise be read as a
			// transcript with nothing in it, and the video would come back
			// unedited with no explanation.
			env.Progress("ignoring %s: it is not a vs transcript\n", filepath.Base(sidecar))
		default:
			env.Progress("using existing transcript %s\n", filepath.Base(sidecar))
			return t, sidecar, nil
		}
	}

	t, err := transcribeFile(ctx, env, input, flags.options(env.Config, env.wantsProgress()))
	return t, "", err
}

// wantsProgress reports whether a live progress line is worth printing.
//
// The recognizer's progress overwrites itself with a carriage return, which is
// only meaningful on a terminal. Piped into a file or a CI log it produces one
// unreadable line per segment, so it is suppressed there.
func (e *Env) wantsProgress() bool {
	return !e.Quiet && term.IsTerminal(int(os.Stderr.Fd()))
}

// lineOptions builds caption geometry from the config and any overrides.
func lineOptions(cfg config.Config, maxChars, maxLines int) transcript.LineOptions {
	return transcript.DefaultLineOptions(
		pickInt(maxChars, cfg.Subtitle.MaxChars),
		pickInt(maxLines, cfg.Subtitle.MaxLines),
	)
}

// encodeFromConfig builds the output encoding, with flag overrides applied.
func encodeFromConfig(cfg config.Config, crf int, preset string) ffmpeg.Encode {
	enc := ffmpeg.Encode{
		VideoCodec:   firstNonEmpty(cfg.Encode.VideoCodec, "libx264"),
		AudioCodec:   firstNonEmpty(cfg.Encode.AudioCodec, "aac"),
		CRF:          pickInt(crf, cfg.Encode.CRF),
		Preset:       firstNonEmpty(preset, cfg.Encode.Preset),
		AudioBitrate: cfg.Encode.AudioBitrate,
	}
	return enc
}

// preflight checks the external tools before any file is touched.
func preflight(env *Env) error {
	return env.FFmpeg.Check()
}

// errNoWork reports that a command found nothing to do. It is not a failure:
// a take with no filler words is a good take.
var errNoWork = errors.New("nothing to do")

// refuseExisting stops a command before it overwrites a file the user did not
// ask it to touch. The ffmpeg layer enforces the same rule for media outputs;
// this covers the ones written directly, such as transcripts and subtitles.
func refuseExisting(path string) error {
	if path == "" || path == "-" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists (pass -f to overwrite)", path)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func pickInt(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

// humanMS renders a duration the way a person reads a timeline.
func humanMS(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Millisecond).String()
}

// displayWidth is how many terminal columns a string occupies.
//
// Go's %-16s pads by bytes, so a column of Chinese labels comes out ragged: one
// character is three bytes and two columns. Everything printed in a column here
// goes through this instead.
func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		switch {
		case r >= 0x1100 && (r <= 0x115F || // Hangul Jamo
			r == 0x2329 || r == 0x232A ||
			(r >= 0x2E80 && r <= 0xA4CF && r != 0x303F) || // CJK radicals, Kana, Han
			(r >= 0xAC00 && r <= 0xD7A3) || // Hangul syllables
			(r >= 0xF900 && r <= 0xFAFF) || // CJK compatibility ideographs
			(r >= 0xFE30 && r <= 0xFE6F) || // CJK compatibility forms
			(r >= 0xFF00 && r <= 0xFF60) || // full-width forms
			(r >= 0xFFE0 && r <= 0xFFE6) ||
			(r >= 0x20000 && r <= 0x3FFFD)): // CJK extensions
			width += 2
		default:
			width++
		}
	}
	return width
}

// padDisplay right-pads s to width terminal columns.
func padDisplay(s string, width int) string {
	if pad := width - displayWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// percent renders a ratio for the summary lines.
func percent(part, whole int64) string {
	if whole <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", float64(part)/float64(whole)*100)
}
