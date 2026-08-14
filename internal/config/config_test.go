package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultIsUsableWithoutAFile(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the built-in defaults must validate: %v", err)
	}
	if cfg.Tools.FFmpeg == "" || cfg.Tools.FFprobe == "" || cfg.Tools.Python == "" {
		t.Fatalf("every external tool needs a default: %+v", cfg.Tools)
	}
	if cfg.ASR.Model == "" || !cfg.ASR.VAD {
		t.Fatalf("recognition defaults are incomplete: %+v", cfg.ASR)
	}
}

// A missing config file is the normal case, not an error: a fresh install has
// to be able to run without anyone writing YAML first.
func TestLoadTreatsAMissingFileAsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("a missing config must not fail: %v", err)
	}
	if cfg.ASR.Model != Default().ASR.Model {
		t.Fatalf("model=%q want the default", cfg.ASR.Model)
	}
}

func TestLoadAppliesFileThenEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "asr:\n  model: medium\n  language: zh\nsubtitle:\n  font_size: 64\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VS_ASR_MODEL", "large-v3")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ASR.Model != "large-v3" {
		t.Fatalf("model=%q want the environment to win over the file", cfg.ASR.Model)
	}
	if cfg.ASR.Language != "zh" {
		t.Fatalf("language=%q want the file value to survive", cfg.ASR.Language)
	}
	if cfg.Subtitle.FontSize != 64 {
		t.Fatalf("font size=%d want 64 from the file", cfg.Subtitle.FontSize)
	}
	// A partially specified section must keep the rest of its defaults rather
	// than zeroing them, or a two-line config breaks subtitle rendering.
	if cfg.Subtitle.MaxChars != Default().Subtitle.MaxChars {
		t.Fatalf("max chars=%d want the default to survive a partial section", cfg.Subtitle.MaxChars)
	}
}

func TestValidateRejectsUnusableValues(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"unknown backend", func(c *Config) { c.ASR.Backend = "whisper.cpp" }, "asr.backend"},
		{"unknown device", func(c *Config) { c.ASR.Device = "tpu" }, "asr.device"},
		{"bad position", func(c *Config) { c.Subtitle.Position = "middle-left" }, "subtitle.position"},
		{"crf out of range", func(c *Config) { c.Encode.CRF = 99 }, "encode.crf"},
		{"no ffmpeg", func(c *Config) { c.Tools.FFmpeg = "" }, "tools.ffmpeg"},
		{"duplicate provider", func(c *Config) {
			c.Providers = []Provider{{Name: "a"}, {Name: "a"}}
		}, "duplicate provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err=%v want one mentioning %q", err, tt.wantErr)
			}
		})
	}
}

func TestVaultPathFallsBackToTheConfigDirectory(t *testing.T) {
	cfg := Default()
	if got := cfg.VaultPath(); filepath.Base(got) != "credentials.vault" {
		t.Fatalf("vault path=%q want it to end in credentials.vault", got)
	}
	cfg.Credentials.VaultPath = "/tmp/explicit.vault"
	if got := cfg.VaultPath(); got != "/tmp/explicit.vault" {
		t.Fatalf("vault path=%q want the explicit override", got)
	}
}
