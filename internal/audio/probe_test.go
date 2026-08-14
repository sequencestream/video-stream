package audio_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sequencestream/video-stream/internal/audio"
)

type recordingTTS struct {
	gotText  string
	gotVoice string
	actualMS int64
	err      error
}

func (r *recordingTTS) RawDurationMS(_ context.Context, text, voice string) (int64, error) {
	r.gotText, r.gotVoice = text, voice
	if r.err != nil {
		return 0, r.err
	}
	return r.actualMS, nil
}

func TestTTSProbeReportsTheSynthesizedDuration(t *testing.T) {
	tts := &recordingTTS{actualMS: 8832}
	got, err := audio.TTSProbe{TTS: tts}.ProbeMS(context.Background(), "  可能之前各种视频号已经把很多东西剧透了  ", "zh-CN-XiaoxiaoNeural")
	if err != nil {
		t.Fatal(err)
	}
	if got != 8832 {
		t.Errorf("got %dms, want 8832ms", got)
	}
	if tts.gotText != "可能之前各种视频号已经把很多东西剧透了" {
		t.Errorf("probe text was not trimmed: %q", tts.gotText)
	}
	if tts.gotVoice != "zh-CN-XiaoxiaoNeural" {
		t.Errorf("probe voice=%q, want the requested voice", tts.gotVoice)
	}
}

func TestTTSProbeSurfacesSynthesisFailures(t *testing.T) {
	want := errors.New("edge-tts exited 1")
	_, err := audio.TTSProbe{TTS: &recordingTTS{err: want}}.ProbeMS(context.Background(), "text", "v")
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want the synthesis error", err)
	}
}

func TestEstimateProbeScalesWithSpokenContent(t *testing.T) {
	probe := audio.EstimateProbe{}
	tests := []struct {
		name string
		text string
		want int64
	}{
		{name: "CJK is billed per rune", text: "今天带着小孩", want: 6 * 220},
		{name: "punctuation is not spoken", text: "今天带着小孩，", want: 6 * 220},
		{name: "ASCII is billed per word", text: "hello wide world", want: 3 * 300},
		{name: "mixed script bills each part once", text: "今天看了 Dragon 餐厅", want: 6*220 + 1*300},
		{name: "blank text costs nothing", text: "   ", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := probe.ProbeMS(context.Background(), tt.text, "")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %dms, want %dms", got, tt.want)
			}
		})
	}
}

func TestEstimateProbeHonoursConfiguredRates(t *testing.T) {
	got, err := audio.EstimateProbe{MSPerRune: 100, MSPerWord: 500}.ProbeMS(context.Background(), "今天 ok", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(2*100 + 500); got != want {
		t.Errorf("got %dms, want %dms", got, want)
	}
}
