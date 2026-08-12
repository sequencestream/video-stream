package config

import (
	"strings"
	"testing"
)

func TestDefaultUsesRealEdgeTTS(t *testing.T) {
	cfg := Default()
	if cfg.Audio.Provider != "edge" {
		t.Fatalf("audio provider=%q want edge", cfg.Audio.Provider)
	}
	if cfg.Audio.DefaultVoice == "" || cfg.Audio.PythonBinary == "" || cfg.Audio.FFmpegBinary == "" {
		t.Fatalf("edge TTS defaults are incomplete: %+v", cfg.Audio)
	}
}

func TestValidateRejectsUnknownTTSProvider(t *testing.T) {
	cfg := Default()
	cfg.Audio.Provider = "unknown"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "audio.provider") {
		t.Fatalf("err=%v", err)
	}
}
