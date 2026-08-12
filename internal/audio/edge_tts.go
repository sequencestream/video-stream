package audio

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sequencestream/video-stream/internal/model"
)

//go:embed edge_tts_provider.py
var edgeTTSProviderScript string

// EdgeTTS invokes the edge-tts Python package and converts its MP3 stream to a
// mono, 48 kHz, signed 16-bit PCM WAV. Word timings come from the provider's
// WordBoundary messages and are scaled by the same bounded playback rate as
// the audio.
type EdgeTTS struct {
	OutputDir      string
	DefaultVoice   string
	PythonBinary   string
	FFmpegBinary   string
	providerScript string // test seam; production uses the embedded helper
}

type edgeWordBoundary struct {
	Text    string `json:"text"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
}

func (e EdgeTTS) Synthesize(ctx context.Context, seg model.Seg, voice string) (SegResult, error) {
	if err := ctx.Err(); err != nil {
		return SegResult{}, err
	}
	if strings.TrimSpace(seg.Text) == "" {
		return SegResult{}, errors.New("edge-tts text must not be empty")
	}
	voice = strings.TrimSpace(voice)
	if voice == "" {
		voice = strings.TrimSpace(e.DefaultVoice)
	}
	if voice == "" {
		return SegResult{}, errors.New("edge-tts voice is required")
	}

	base := e.OutputDir
	if strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	workDir, err := os.MkdirTemp(base, ".edge-tts-*")
	if err != nil {
		return SegResult{}, fmt.Errorf("create edge-tts work directory: %w", err)
	}
	defer os.RemoveAll(workDir)

	mp3Path := filepath.Join(workDir, "speech.mp3")
	timingPath := filepath.Join(workDir, "words.json")
	python := strings.TrimSpace(e.PythonBinary)
	if python == "" {
		python = "python3"
	}
	script := e.providerScript
	if script == "" {
		script = edgeTTSProviderScript
	}
	cmd := exec.CommandContext(ctx, python, "-c", script,
		"--voice", voice, "--media", mp3Path, "--timings", timingPath)
	cmd.Stdin = strings.NewReader(seg.Text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return SegResult{}, commandError(ctx, "edge-tts", err, stderr.String())
	}

	timingData, err := os.ReadFile(timingPath)
	if err != nil {
		return SegResult{}, fmt.Errorf("read edge-tts word timings: %w", err)
	}
	var words []edgeWordBoundary
	if err := json.Unmarshal(timingData, &words); err != nil {
		return SegResult{}, fmt.Errorf("decode edge-tts word timings: %w", err)
	}
	if err := validateEdgeWords(words); err != nil {
		return SegResult{}, err
	}

	rawWAV := filepath.Join(workDir, "speech-raw.wav")
	if err := convertToPCM(ctx, e.FFmpegBinary, mp3Path, rawWAV, 1); err != nil {
		return SegResult{}, err
	}
	rawMS, err := wavDurationMS(rawWAV)
	if err != nil {
		return SegResult{}, fmt.Errorf("inspect edge-tts WAV: %w", err)
	}
	rate, err := PlaybackRate(rawMS, seg.DurationBudget)
	if err != nil {
		return SegResult{}, err
	}

	segmentDir := filepath.Join(base, ".tts-segments")
	if err := os.MkdirAll(segmentDir, 0o755); err != nil {
		return SegResult{}, fmt.Errorf("create TTS segment directory: %w", err)
	}
	out, err := os.CreateTemp(segmentDir, ".segment-*.wav")
	if err != nil {
		return SegResult{}, fmt.Errorf("create TTS WAV: %w", err)
	}
	outPath := out.Name()
	if err := out.Close(); err != nil {
		os.Remove(outPath)
		return SegResult{}, err
	}
	if err := os.Remove(outPath); err != nil {
		return SegResult{}, err
	}
	if err := convertToPCM(ctx, e.FFmpegBinary, rawWAV, outPath, rate); err != nil {
		os.Remove(outPath)
		return SegResult{}, err
	}
	actualMS, err := wavDurationMS(outPath)
	if err != nil {
		os.Remove(outPath)
		return SegResult{}, fmt.Errorf("inspect final TTS WAV: %w", err)
	}
	if !seg.DurationBudget.Contains(actualMS) {
		os.Remove(outPath)
		return SegResult{}, fmt.Errorf("%w: converted duration %dms is outside [%d,%d]",
			ErrNeedsWordCountChange, actualMS, seg.DurationBudget.MinMS, seg.DurationBudget.MaxMS)
	}

	tokens := make([]model.Token, len(words))
	for i, word := range words {
		start := int64(math.Round(float64(word.StartMS) / rate))
		end := int64(math.Round(float64(word.EndMS) / rate))
		if end > actualMS {
			end = actualMS
		}
		if end <= start {
			end = start + 1
		}
		tokens[i] = model.Token{
			ID: fmt.Sprintf("tok-%s-%d", seg.SegID, i), Text: word.Text,
			StartMS: start, EndMS: end, Confidence: 1,
			Source: model.SourceTTSAlign, EditState: model.EditKept,
		}
	}

	return SegResult{
		SegID: seg.SegID, ActualMS: actualMS, Rate: rate,
		Tokens: tokens, AudioURI: outPath,
	}, nil
}

func validateEdgeWords(words []edgeWordBoundary) error {
	if len(words) == 0 {
		return errors.New("edge-tts returned no word timings")
	}
	var previousEnd int64
	for i, word := range words {
		if strings.TrimSpace(word.Text) == "" {
			return fmt.Errorf("edge-tts word timing %d has empty text", i)
		}
		if word.StartMS < previousEnd || word.EndMS <= word.StartMS {
			return fmt.Errorf("edge-tts word timing %d is invalid or overlaps its predecessor", i)
		}
		previousEnd = word.EndMS
	}
	return nil
}

func convertToPCM(ctx context.Context, binary, input, output string, rate float64) error {
	if strings.TrimSpace(binary) == "" {
		binary = "ffmpeg"
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", input}
	if math.Abs(rate-1) > 0.000001 {
		args = append(args, "-filter:a", fmt.Sprintf("atempo=%.9f", rate))
	}
	args = append(args, "-ar", "48000", "-ac", "1", "-c:a", "pcm_s16le", output)
	cmd := exec.CommandContext(ctx, binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return commandError(ctx, "ffmpeg TTS conversion", err, stderr.String())
	}
	return nil
}

func commandError(ctx context.Context, name string, err error, stderr string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s interrupted: %w", name, ctxErr)
	}
	message := strings.TrimSpace(stderr)
	if len(message) > 8*1024 {
		message = message[len(message)-8*1024:]
	}
	if message == "" {
		return fmt.Errorf("run %s: %w", name, err)
	}
	return fmt.Errorf("run %s: %w: %s", name, err, message)
}

// wavDurationMS reads the RIFF fmt/data chunks instead of trusting metadata
// copied from the compressed source.
func wavDurationMS(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	header := make([]byte, 12)
	if _, err := io.ReadFull(f, header); err != nil {
		return 0, err
	}
	if string(header[:4]) != "RIFF" || string(header[8:]) != "WAVE" {
		return 0, errors.New("not a RIFF/WAVE file")
	}
	var byteRate, dataSize uint32
	for {
		chunk := make([]byte, 8)
		if _, err := io.ReadFull(f, chunk); err != nil {
			return 0, err
		}
		size := binary.LittleEndian.Uint32(chunk[4:])
		switch string(chunk[:4]) {
		case "fmt ":
			body := make([]byte, size)
			if _, err := io.ReadFull(f, body); err != nil {
				return 0, err
			}
			if len(body) < 12 {
				return 0, errors.New("short WAV fmt chunk")
			}
			byteRate = binary.LittleEndian.Uint32(body[8:12])
		case "data":
			dataSize = size
			if byteRate == 0 {
				return 0, errors.New("WAV data precedes a valid fmt chunk")
			}
			return int64(math.Round(float64(dataSize) * 1000 / float64(byteRate))), nil
		default:
			if _, err := f.Seek(int64(size+(size&1)), io.SeekCurrent); err != nil {
				return 0, err
			}
		}
	}
}
