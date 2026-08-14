package asr

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sequencestream/video-stream/internal/transcript"
)

//go:embed faster_whisper.py
var fasterWhisperScript string

// FasterWhisper runs recognition through the faster-whisper Python package.
//
// The helper script is embedded in the binary and written to a temp file per
// run rather than installed anywhere. There is nothing to keep in sync, nothing
// to put on PATH, and no way for the script to drift from the Go code that
// parses its output.
type FasterWhisper struct {
	// Python is the interpreter to run. Empty means python3.
	Python string
	// Stderr receives the backend's progress output. Nil means os.Stderr.
	Stderr *os.File
	// script is a test seam; production uses the embedded helper.
	script string
}

// Name identifies the backend.
func (f FasterWhisper) Name() string { return "faster-whisper" }

func (f FasterWhisper) python() string {
	if s := strings.TrimSpace(f.Python); s != "" {
		return s
	}
	return "python3"
}

// Check reports whether the interpreter and the package are both present.
//
// It is worth the extra process: recognition is preceded by an audio extraction
// that can take a minute on a long file, and discovering the dependency is
// missing after that minute is a bad trade for a 200 ms check.
func (f FasterWhisper) Check(ctx context.Context) error {
	if _, err := exec.LookPath(f.python()); err != nil {
		return fmt.Errorf("python interpreter %q not found: install Python 3, or set tools.python in the config", f.python())
	}
	cmd := exec.CommandContext(ctx, f.python(), "-c", "import faster_whisper")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("faster-whisper is not installed for %s\n  install it with: %s -m pip install faster-whisper",
			f.python(), f.python())
	}
	return nil
}

// Transcribe recognizes the audio file at path.
func (f FasterWhisper) Transcribe(ctx context.Context, audioPath string, opts Options) (transcript.Transcript, error) {
	if strings.TrimSpace(audioPath) == "" {
		return transcript.Transcript{}, errors.New("audio path is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		opts.Model = "small"
	}

	script := f.script
	if script == "" {
		script = fasterWhisperScript
	}
	scriptPath, cleanup, err := writeTemp("vs-asr-*.py", script)
	if err != nil {
		return transcript.Transcript{}, err
	}
	defer cleanup()

	request, err := json.Marshal(map[string]any{
		"audio":        audioPath,
		"model":        opts.Model,
		"language":     opts.Language,
		"device":       opts.Device,
		"compute_type": opts.ComputeType,
		"model_dir":    opts.ModelDir,
		"threads":      opts.Threads,
		"vad":          opts.VAD,
		"prompt":       opts.Prompt,
		"beam_size":    opts.BeamSize,
		"progress":     opts.Progress,
	})
	if err != nil {
		return transcript.Transcript{}, err
	}

	cmd := exec.CommandContext(ctx, f.python(), scriptPath)
	cmd.Stdin = bytes.NewReader(request)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if opts.Progress {
		cmd.Stderr = f.errWriter()
	} else {
		cmd.Stderr = &stderr
	}

	runErr := cmd.Run()

	// The helper reports its own failures as JSON on stdout, so parse before
	// judging the exit status: a structured error is far more useful than
	// "exit status 2".
	var reply fasterWhisperReply
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &reply); err == nil && reply.Error != "" {
			if reply.Hint != "" {
				return transcript.Transcript{}, fmt.Errorf("%s\n  try: %s", reply.Error, reply.Hint)
			}
			return transcript.Transcript{}, errors.New(reply.Error)
		}
	}
	if runErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return transcript.Transcript{}, fmt.Errorf("recognition interrupted: %w", ctxErr)
		}
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return transcript.Transcript{}, fmt.Errorf("faster-whisper failed: %w\n%s", runErr, detail)
		}
		return transcript.Transcript{}, fmt.Errorf("faster-whisper failed: %w", runErr)
	}
	if stdout.Len() == 0 {
		return transcript.Transcript{}, errors.New("faster-whisper returned no output")
	}
	if err := json.Unmarshal(stdout.Bytes(), &reply); err != nil {
		return transcript.Transcript{}, fmt.Errorf("parse recognizer output: %w", err)
	}

	out := transcript.Transcript{
		Version:    transcript.Version,
		Language:   reply.Language,
		Model:      f.Name() + ":" + opts.Model,
		DurationMS: reply.DurationMS,
		Cues:       reply.Cues,
	}
	out.Sort()
	return out, out.Validate()
}

type fasterWhisperReply struct {
	Error      string           `json:"error"`
	Hint       string           `json:"hint"`
	Language   string           `json:"language"`
	DurationMS int64            `json:"duration_ms"`
	Cues       []transcript.Cue `json:"cues"`
}

func (f FasterWhisper) errWriter() *os.File {
	if f.Stderr != nil {
		return f.Stderr
	}
	return os.Stderr
}

func writeTemp(pattern, content string) (string, func(), error) {
	file, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", func() {}, err
	}
	path := file.Name()
	cleanup := func() { os.Remove(path) }
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}
