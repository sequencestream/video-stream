package audio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultTruePeakDB = -1.5
	defaultLoudnessRA = 11.0
)

// LoudnessResult describes the measured input and the verified normalized
// output. Values are integrated loudness in LUFS and gain in dB.
type LoudnessResult struct {
	InputLUFS  float64
	OutputLUFS float64
	GainDB     float64
}

type loudnormMeasurement struct {
	InputI      string `json:"input_i"`
	InputTP     string `json:"input_tp"`
	InputLRA    string `json:"input_lra"`
	InputThresh string `json:"input_thresh"`
	TargetOff   string `json:"target_offset"`
}

// NormalizeFileLUFS measures input with FFmpeg's EBU R128 loudnorm filter,
// applies a measured second pass, then measures the output again. The output is
// published atomically only when its integrated loudness is within tolerance.
// Input and output may name the same file.
func NormalizeFileLUFS(ctx context.Context, ffmpegBinary, inputPath, outputPath string, target, tolerance float64) (LoudnessResult, error) {
	if strings.TrimSpace(inputPath) == "" || strings.TrimSpace(outputPath) == "" {
		return LoudnessResult{}, errors.New("loudness input and output paths are required")
	}
	if target < -70 || target > -5 {
		return LoudnessResult{}, fmt.Errorf("loudness target %.2f must be between -70 and -5 LUFS", target)
	}
	if tolerance < 0 {
		return LoudnessResult{}, errors.New("loudness tolerance must not be negative")
	}
	if info, err := os.Stat(inputPath); err != nil {
		return LoudnessResult{}, fmt.Errorf("stat loudness input: %w", err)
	} else if !info.Mode().IsRegular() {
		return LoudnessResult{}, fmt.Errorf("loudness input %s is not a regular file", inputPath)
	}
	if strings.TrimSpace(ffmpegBinary) == "" {
		ffmpegBinary = "ffmpeg"
	}

	first, inputLUFS, err := measureLUFS(ctx, ffmpegBinary, inputPath, target)
	if err != nil {
		return LoudnessResult{}, fmt.Errorf("measure input loudness: %w", err)
	}
	outDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return LoudnessResult{}, fmt.Errorf("create loudness output directory: %w", err)
	}
	tmp, err := os.CreateTemp(outDir, ".loudness-*.wav")
	if err != nil {
		return LoudnessResult{}, fmt.Errorf("create temporary loudness output: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return LoudnessResult{}, fmt.Errorf("close temporary loudness output: %w", err)
	}
	if err := os.Remove(tmpPath); err != nil {
		return LoudnessResult{}, fmt.Errorf("prepare temporary loudness output: %w", err)
	}
	defer os.Remove(tmpPath)

	filter := fmt.Sprintf(
		"loudnorm=I=%s:TP=%s:LRA=%s:measured_I=%s:measured_TP=%s:measured_LRA=%s:measured_thresh=%s:offset=%s:linear=true:print_format=summary",
		formatLoudness(target), formatLoudness(defaultTruePeakDB), formatLoudness(defaultLoudnessRA),
		first.InputI, first.InputTP, first.InputLRA, first.InputThresh, first.TargetOff,
	)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", inputPath,
		"-af", filter, "-ar", "48000", "-ac", "1", "-c:a", "pcm_s16le", "-f", "wav", tmpPath,
	}
	if stderr, err := runFFmpeg(ctx, ffmpegBinary, args...); err != nil {
		return LoudnessResult{}, commandError(ctx, "ffmpeg loudness normalization", err, stderr)
	}
	_, outputLUFS, err := measureLUFS(ctx, ffmpegBinary, tmpPath, target)
	if err != nil {
		return LoudnessResult{}, fmt.Errorf("verify normalized loudness: %w", err)
	}
	if math.Abs(outputLUFS-target) > tolerance {
		return LoudnessResult{}, fmt.Errorf("normalized loudness %.2f LUFS is outside target %.2f ± %.2f", outputLUFS, target, tolerance)
	}
	if err := os.Rename(tmpPath, outputPath); err != nil {
		return LoudnessResult{}, fmt.Errorf("publish normalized loudness: %w", err)
	}
	return LoudnessResult{InputLUFS: inputLUFS, OutputLUFS: outputLUFS, GainDB: target - inputLUFS}, nil
}

func measureLUFS(ctx context.Context, binary, inputPath string, target float64) (loudnormMeasurement, float64, error) {
	filter := fmt.Sprintf("loudnorm=I=%s:TP=%s:LRA=%s:print_format=json", formatLoudness(target), formatLoudness(defaultTruePeakDB), formatLoudness(defaultLoudnessRA))
	stderr, err := runFFmpeg(ctx, binary,
		"-hide_banner", "-nostats", "-nostdin", "-i", inputPath,
		"-af", filter, "-f", "null", "-",
	)
	if err != nil {
		return loudnormMeasurement{}, 0, commandError(ctx, "ffmpeg loudness measurement", err, stderr)
	}
	start, end := strings.LastIndex(stderr, "{"), strings.LastIndex(stderr, "}")
	if start < 0 || end <= start {
		return loudnormMeasurement{}, 0, errors.New("ffmpeg loudness measurement did not return JSON")
	}
	var measurement loudnormMeasurement
	if err := json.Unmarshal([]byte(stderr[start:end+1]), &measurement); err != nil {
		return loudnormMeasurement{}, 0, fmt.Errorf("decode ffmpeg loudness measurement: %w", err)
	}
	integrated, err := strconv.ParseFloat(measurement.InputI, 64)
	if err != nil || math.IsInf(integrated, 0) || math.IsNaN(integrated) {
		return loudnormMeasurement{}, 0, fmt.Errorf("audio has no measurable integrated loudness: %q", measurement.InputI)
	}
	for name, value := range map[string]string{
		"input_tp": measurement.InputTP, "input_lra": measurement.InputLRA,
		"input_thresh": measurement.InputThresh, "target_offset": measurement.TargetOff,
	} {
		parsed, parseErr := strconv.ParseFloat(value, 64)
		if parseErr != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
			return loudnormMeasurement{}, 0, fmt.Errorf("invalid ffmpeg loudness %s: %q", name, value)
		}
	}
	return measurement, integrated, nil
}

func runFFmpeg(ctx context.Context, binary string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.String(), err
}

func formatLoudness(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64)
}

// NormalizeLUFS returns the gain needed when the measurement is outside the
// accepted range. ok reports whether no adjustment is necessary.
func NormalizeLUFS(measured, target, tolerance float64) (gainDB float64, ok bool) {
	delta := target - measured
	if math.Abs(delta) <= tolerance {
		return 0, true
	}
	return delta, false
}

// MeasureStub returns a deterministic pseudo-LUFS for stub audio paths.
func MeasureStub(uri string, target float64) float64 {
	if uri == "" {
		return target
	}
	n := float64(len(uri)%5) - 2
	return target + n*0.3
}
