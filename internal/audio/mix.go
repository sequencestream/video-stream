package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func writeAudioMix(ctx context.Context, ffmpegBinary, outputPath string, segments []SegResult) error {
	if len(segments) == 0 {
		return errors.New("cannot mix zero TTS segments")
	}
	inputs := make([]string, 0, len(segments))
	for _, segment := range segments {
		if strings.HasPrefix(segment.AudioURI, "stub-audio://") {
			return writeStub(outputPath, "audio")
		}
		if _, err := os.Stat(segment.AudioURI); err != nil {
			return fmt.Errorf("stat TTS segment %s: %w", segment.SegID, err)
		}
		inputs = append(inputs, segment.AudioURI)
	}
	if strings.TrimSpace(ffmpegBinary) == "" {
		ffmpegBinary = "ffmpeg"
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(outputPath), ".mix-*.wav")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temporary TTS mix: %w", err)
	}
	if err := os.Remove(tmpPath); err != nil {
		return fmt.Errorf("prepare temporary TTS mix: %w", err)
	}
	defer os.Remove(tmpPath)

	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}
	for _, input := range inputs {
		args = append(args, "-i", input)
	}
	if len(inputs) > 1 {
		var filter strings.Builder
		for i := range inputs {
			fmt.Fprintf(&filter, "[%d:a:0]", i)
		}
		fmt.Fprintf(&filter, "concat=n=%d:v=0:a=1[out]", len(inputs))
		args = append(args, "-filter_complex", filter.String(), "-map", "[out]")
	} else {
		args = append(args, "-map", "0:a:0")
	}
	args = append(args, "-ar", "48000", "-ac", "1", "-c:a", "pcm_s16le", tmpPath)
	cmd := exec.CommandContext(ctx, ffmpegBinary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return commandError(ctx, "ffmpeg TTS mix", err, stderr.String())
	}
	if err := os.Rename(tmpPath, outputPath); err != nil {
		return fmt.Errorf("publish TTS mix: %w", err)
	}
	return nil
}

func segmentsAreStub(segments []SegResult) bool {
	return len(segments) > 0 && strings.HasPrefix(segments[0].AudioURI, "stub-audio://")
}
