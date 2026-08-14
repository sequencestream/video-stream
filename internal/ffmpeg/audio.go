package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/sequencestream/video-stream/internal/timespan"
)

// ASRSampleRate is 16 kHz mono, which is what every speech model wants.
// Feeding a 48 kHz stereo track in means the model resamples it anyway, at the
// cost of moving four times the bytes across the process boundary first.
const ASRSampleRate = 16000

// ExtractAudio writes the input's audio as a WAV file.
//
// sampleRate and channels of zero mean the source's own. Recognition should
// pass ASRSampleRate and 1.
func (t Tool) ExtractAudio(ctx context.Context, input, output string, sampleRate, channels int) error {
	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(t.BaseArgs(), "-i", input, "-vn", "-map", "0:a:0")
		if channels > 0 {
			args = append(args, "-ac", strconv.Itoa(channels))
		}
		if sampleRate > 0 {
			args = append(args, "-ar", strconv.Itoa(sampleRate))
		}
		return append(args, "-c:a", "pcm_s16le", "-f", "wav", tmp)
	})
}

// ReplaceAudio muxes a new audio track over the input's video, copying the
// video untouched and ending at whichever track runs out first.
func (t Tool) ReplaceAudio(ctx context.Context, input, audio, output string, enc Encode) error {
	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(t.BaseArgs(),
			"-i", input, "-i", audio,
			"-map", "0:v:0", "-map", "1:a:0",
			"-c:v", "copy", "-shortest",
		)
		args = append(args, enc.AudioArgs()...)
		return append(args, faststart(output, tmp)...)
	})
}

// Normalize applies ffmpeg's loudnorm filter to hit a target integrated
// loudness in LUFS.
//
// This is a single-pass normalization: fast, and accurate to about ±1 LU. The
// two-pass form measures first and is tighter, but a second full decode to buy
// a fraction of a decibel is not a trade this tool makes by default.
func (t Tool) Normalize(ctx context.Context, input, output string, targetLUFS, truePeak float64, enc Encode) error {
	filter := fmt.Sprintf("loudnorm=I=%s:TP=%s:LRA=11",
		trimFloat(targetLUFS), trimFloat(truePeak))
	return t.RunAtomic(ctx, output, func(tmp string) []string {
		args := append(t.BaseArgs(), "-i", input, "-af", filter, "-map", "0")
		if strings.TrimSpace(enc.VideoCodec) != "" {
			args = append(args, "-c:v", "copy")
		}
		args = append(args, enc.AudioArgs()...)
		return append(args, faststart(output, tmp)...)
	})
}

// DetectSilence reports the spans ffmpeg's silencedetect filter considers
// silent.
//
// thresholdDB is the level below which audio counts as silence, as a negative
// number of decibels: -30 catches room tone, -50 only catches true digital
// silence. minDurationMS ignores anything briefer, which is what stops every
// gap between two words from being reported.
func (t Tool) DetectSilence(ctx context.Context, input string, thresholdDB float64, minDurationMS int64, media Media) (timespan.Ranges, error) {
	filter := fmt.Sprintf("silencedetect=noise=%sdB:d=%s",
		trimFloat(thresholdDB), timespan.FormatSeconds(minDurationMS))

	args := []string{
		"-hide_banner", "-nostdin", "-i", input,
		"-af", filter, "-f", "null", "-",
	}
	cmd := exec.CommandContext(ctx, t.ffmpegBin(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = nil
	if err := cmd.Run(); err != nil {
		if detail := tail(stderr.String(), 2048); detail != "" {
			return nil, fmt.Errorf("silence detection failed: %w\n%s", err, detail)
		}
		return nil, fmt.Errorf("silence detection failed: %w", err)
	}
	return parseSilence(stderr.String(), media.DurationMS), nil
}

// parseSilence reads silencedetect's log lines.
//
// The filter reports a start and, later, an end. A silence still open when the
// file ends never gets its end line — that is the trailing silence at the end
// of a take, which is exactly the one worth trimming, so it is closed against
// the media duration rather than dropped.
func parseSilence(log string, totalMS int64) timespan.Ranges {
	var (
		out     timespan.Ranges
		start   int64
		pending bool
	)
	scanner := bufio.NewScanner(strings.NewReader(log))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if idx := strings.Index(line, "silence_start:"); idx >= 0 {
			if ms, ok := parseSeconds(line[idx+len("silence_start:"):]); ok {
				start, pending = ms, true
			}
			continue
		}
		if idx := strings.Index(line, "silence_end:"); idx >= 0 && pending {
			if ms, ok := parseSeconds(line[idx+len("silence_end:"):]); ok {
				out = append(out, timespan.Range{StartMS: start, EndMS: ms})
				pending = false
			}
		}
	}
	if pending && totalMS > start {
		out = append(out, timespan.Range{StartMS: start, EndMS: totalMS})
	}
	return out.Normalize()
}

// parseSeconds reads the first float in a log fragment, which may be followed
// by " | silence_duration: 2.2".
func parseSeconds(s string) (int64, bool) {
	field := strings.TrimSpace(s)
	if i := strings.IndexAny(field, " |"); i > 0 {
		field = field[:i]
	}
	f, err := strconv.ParseFloat(field, 64)
	if err != nil {
		return 0, false
	}
	if f < 0 {
		f = 0
	}
	return int64(f*1000 + 0.5), true
}

// TempWAV creates a temp path for an intermediate audio file and returns it
// with a cleanup func.
func TempWAV() (string, func(), error) {
	f, err := os.CreateTemp("", "vs-audio-*.wav")
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	f.Close()
	// ffmpeg refuses to overwrite through the atomic-publish path unless the
	// target is absent, and an empty placeholder is still a file.
	os.Remove(path)
	return path, func() { os.Remove(path) }, nil
}
