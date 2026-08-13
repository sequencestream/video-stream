package render

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBestBeatPhaseFitsLockedCuts(t *testing.T) {
	period := 500 * time.Millisecond
	phase := bestBeatPhase([]time.Duration{1100 * time.Millisecond, 2100 * time.Millisecond}, period)
	if phase != 100*time.Millisecond {
		t.Fatalf("phase=%s want 100ms", phase)
	}
}

func TestExecBGMMixerProducesVerifiedDuckMix(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	dir := t.TempDir()
	speech := filepath.Join(dir, "speech.wav")
	music := filepath.Join(dir, "music.wav")
	output := filepath.Join(dir, "mixed.wav")
	fixture := func(filter, path string) {
		t.Helper()
		cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", filter,
			"-ar", "48000", "-ac", "1", "-c:a", "pcm_s16le", "-y", path)
		if body, runErr := cmd.CombinedOutput(); runErr != nil {
			t.Fatalf("create audio fixture: %v: %s", runErr, body)
		}
	}
	// Narration is deliberately shorter than the locked video timeline; the
	// mixer must keep BGM playing through the remaining half second.
	fixture("sine=frequency=440:duration=2", speech)
	fixture("sine=frequency=880:duration=0.5", music)

	result, err := (ExecBGMMixer{Binary: ffmpeg}).Mix(context.Background(), BGMMixPlan{
		SpeechPath: speech, OutputPath: output,
		Config:        BGMConfig{URI: music, BPM: 120, BeatOffsetMS: 250, GainDB: -18},
		CutPoints:     []time.Duration{1100 * time.Millisecond, 2100 * time.Millisecond},
		TotalDuration: 2500 * time.Millisecond, TargetLUFS: -14, ToleranceLUFS: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TimelineBeatPhase != 100*time.Millisecond || result.SourceStart != 150*time.Millisecond {
		t.Fatalf("alignment=%+v want phase=100ms source=150ms", result)
	}
	if result.OutputLUFS < -14.5 || result.OutputLUFS > -13.5 {
		t.Fatalf("output loudness=%f", result.OutputLUFS)
	}

	body, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", output).Output()
	if err != nil {
		t.Fatal(err)
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(body)), 64)
	if err != nil || duration < 2.45 || duration > 2.55 {
		t.Fatalf("duration=%q parsed=%f err=%v", body, duration, err)
	}

	// The band around the music tone must remain present after ducking and
	// normalization; this catches accidentally publishing speech alone.
	probe, err := exec.Command(ffmpeg, "-hide_banner", "-nostdin", "-i", output,
		"-af", "bandpass=f=880:w=80,volumedetect", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(probe), "mean_volume:") || strings.Contains(string(probe), "mean_volume: -inf") {
		t.Fatalf("music band is absent: %s", probe)
	}
}
