// Package asr turns speech into a timestamped transcript.
//
// Recognition is the one capability vs cannot delegate to ffmpeg, so it is the
// one place the tool grows a second runtime. It stays behind this interface and
// out of process: a missing Python package must degrade into one clear sentence
// on stderr, never into a broken build or a Go binary that will not start.
package asr

import (
	"context"

	"github.com/sequencestream/video-stream/internal/transcript"
)

// Options configures one recognition run.
type Options struct {
	// Model is a model name (tiny/base/small/medium/large-v3) or a path to a
	// converted model directory.
	Model string
	// Language is a two-letter code. Empty autodetects, which costs an extra
	// pass over the first 30 seconds and occasionally guesses wrong on a video
	// that opens with music.
	Language string
	// Device is auto, cpu or cuda.
	Device string
	// ComputeType is auto, int8, int8_float16, float16 or float32.
	ComputeType string
	// ModelDir overrides where downloaded models are cached.
	ModelDir string
	// Threads caps CPU threads. Zero lets the backend decide.
	Threads int
	// VAD drops silence before recognition. Leave it on: whisper invents
	// sentences to fill long silences, and the invented ones are fluent.
	VAD bool
	// Prompt biases the vocabulary. Feeding it the names and jargon the video
	// uses is the cheapest accuracy win available.
	Prompt string
	// BeamSize trades speed for accuracy. Zero means the backend default.
	BeamSize int
	// Progress asks the backend to report how far it has got, on stderr.
	Progress bool
}

// Recognizer transcribes an audio file.
//
// The input is always audio, never video: extracting the audio track is
// ffmpeg's job, and doing it first means the recognizer never has to know what
// a container is.
type Recognizer interface {
	// Name identifies the backend for the transcript's model field.
	Name() string
	// Transcribe recognizes the audio at path.
	Transcribe(ctx context.Context, audioPath string, opts Options) (transcript.Transcript, error)
}
