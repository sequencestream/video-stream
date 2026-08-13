package audio

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

type fixtureTTS struct{ path string }

func (f fixtureTTS) Synthesize(_ context.Context, seg model.Seg, _ string) (SegResult, error) {
	return SegResult{
		SegID: seg.SegID, ActualMS: 3000, Rate: 1, AudioURI: f.path,
		Tokens: allocateTokens(seg.SegID, []string{seg.Text}, 3000),
	}, nil
}

func TestNormalizeFileLUFSMeasuresNormalizesAndVerifies(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	path := filepath.Join(t.TempDir(), "quiet.wav")
	makeLoudnessFixture(t, ffmpeg, "sine=frequency=440:duration=3,volume=0.03", path)

	result, err := NormalizeFileLUFS(t.Context(), ffmpeg, path, path, -14, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if result.InputLUFS >= -30 {
		t.Fatalf("fixture input loudness %.2f LUFS was not quiet", result.InputLUFS)
	}
	if result.OutputLUFS < -14.5 || result.OutputLUFS > -13.5 {
		t.Fatalf("output loudness %.2f LUFS is outside target", result.OutputLUFS)
	}
	if result.GainDB <= 0 {
		t.Fatalf("gain %.2f dB must raise the quiet fixture", result.GainDB)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		t.Fatalf("normalized artifact is not WAV")
	}
}

func TestNormalizeFileLUFSRejectsSilenceWithoutReplacingOutput(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "silence.wav")
	output := filepath.Join(dir, "normalized.wav")
	makeLoudnessFixture(t, ffmpeg, "anullsrc=r=48000:cl=mono:d=1", input)
	const previous = "keep-existing-output"
	if err := os.WriteFile(output, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = NormalizeFileLUFS(t.Context(), ffmpeg, input, output, -14, 0.5)
	if err == nil || !strings.Contains(err.Error(), "no measurable integrated loudness") {
		t.Fatalf("err=%v, want unmeasurable loudness", err)
	}
	data, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != previous {
		t.Fatalf("failed normalization replaced existing output with %q", data)
	}
}

func TestEngineNormalizesRealTTSMix(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "tts.wav")
	makeLoudnessFixture(t, ffmpeg, "sine=frequency=600:duration=3,volume=0.03", fixture)
	project := model.NewProject("real-loudness", "test", testNow())
	project.Segs = []model.Seg{model.NewSeg("seg-1", "speech", 3000)}
	project.Seal()
	reporter := telemetry.NewMemoryReporter()
	engine := New(Options{
		TTS: fixtureTTS{path: fixture}, OutputDir: filepath.Join(dir, "out"),
		FFmpegBinary: ffmpeg, Reporter: reporter,
	})

	result, err := engine.Synthesize(t.Context(), SynthesizeRequest{Project: project, Platform: "youtube"})
	if err != nil {
		t.Fatal(err)
	}
	if result.MeasuredLUFS < -14.5 || result.MeasuredLUFS > -13.5 || result.GainDB <= 0 {
		t.Fatalf("engine loudness result=%+v", result)
	}
	found := false
	for _, event := range reporter.Events() {
		if event.Name == "audio.loudness_normalized" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing audio.loudness_normalized telemetry")
	}
}

func makeLoudnessFixture(t *testing.T, ffmpeg, source, output string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), ffmpeg,
		"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", source,
		"-ar", "48000", "-ac", "1", "-c:a", "pcm_s16le", "-y", output,
	)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make loudness fixture: %v: %s", err, data)
	}
}
