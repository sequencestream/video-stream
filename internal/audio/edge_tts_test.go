package audio

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/model"
)

const fakeEdgeTTSProvider = `
import argparse, json, sys, wave
p = argparse.ArgumentParser()
p.add_argument("--voice", required=True)
p.add_argument("--media", required=True)
p.add_argument("--timings", required=True)
a = p.parse_args()
assert sys.stdin.read() == "one two"
with wave.open(a.media, "wb") as w:
    w.setnchannels(1); w.setsampwidth(2); w.setframerate(48000)
    w.writeframes(b"\0\0" * 48000)
with open(a.timings, "w") as f:
    json.dump([
        {"text":"one", "start_ms":100, "end_ms":400},
        {"text":"two", "start_ms":500, "end_ms":900}
    ], f)
`

func TestEdgeTTSSynthesizesPCMWithProviderWordTimings(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	provider := EdgeTTS{
		OutputDir: dir, DefaultVoice: "test-voice", PythonBinary: python,
		FFmpegBinary: ffmpeg, providerScript: fakeEdgeTTSProvider,
	}
	seg := model.NewSeg("s1", "one two", 1000)
	result, err := provider.Synthesize(t.Context(), seg, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(result.AudioURI) })
	if result.ActualMS != 1000 || result.Rate != 1 {
		t.Fatalf("duration=%d rate=%f", result.ActualMS, result.Rate)
	}
	if len(result.Tokens) != 2 || result.Tokens[0].Text != "one" || result.Tokens[1].StartMS != 500 {
		t.Fatalf("provider word timings were not preserved: %+v", result.Tokens)
	}
	data, err := os.ReadFile(result.AudioURI)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		t.Fatalf("output is not WAV: %q", data[:min(len(data), 12)])
	}
}

func TestEdgeTTSSmoke(t *testing.T) {
	if os.Getenv("VS_EDGE_TTS_SMOKE") != "1" {
		t.Skip("set VS_EDGE_TTS_SMOKE=1 to call the Edge speech service")
	}
	for _, binary := range []string{"python3", "ffmpeg"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is not installed", binary)
		}
	}
	result, err := (EdgeTTS{
		OutputDir: t.TempDir(), DefaultVoice: "zh-CN-XiaoxiaoNeural",
	}).Synthesize(t.Context(), model.NewSeg("smoke", "真实语音合成需要准确的词级时间戳。", 4000), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tokens) < 2 {
		t.Fatalf("expected provider word boundaries, got %+v", result.Tokens)
	}
	if filepath.Ext(result.AudioURI) != ".wav" || !strings.Contains(result.AudioURI, ".tts-segments") {
		t.Fatalf("unexpected audio artifact %q", result.AudioURI)
	}
}
