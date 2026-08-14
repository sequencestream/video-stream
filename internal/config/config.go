// Package config loads vs's optional configuration.
//
// Precedence is: built-in defaults < YAML file < VS_-prefixed environment
// variables < command flags. Every field has a working default, and a missing
// config file is not an error: a fresh checkout must be able to run
// `vs subtitle clip.mp4` without anyone writing YAML first. The file exists to
// stop you retyping the same eight flags, not to gate the tool.
//
// Nothing here holds a secret. Provider entries carry only metadata, and the
// key itself is fetched from internal/credential at the moment of use.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the full runtime configuration.
type Config struct {
	Tools       Tools       `yaml:"tools"`
	ASR         ASR         `yaml:"asr"`
	Subtitle    Subtitle    `yaml:"subtitle"`
	Filler      Filler      `yaml:"filler"`
	Encode      Encode      `yaml:"encode"`
	Logging     Logging     `yaml:"logging"`
	Credentials Credentials `yaml:"credentials"`
	Providers   []Provider  `yaml:"providers"`
}

// Tools locates the external binaries vs shells out to. Bare names are resolved
// through PATH; absolute paths let a user pin a specific build.
type Tools struct {
	FFmpeg  string `yaml:"ffmpeg"`
	FFprobe string `yaml:"ffprobe"`
	Python  string `yaml:"python"`
}

// ASR configures speech recognition.
type ASR struct {
	// Backend selects the recognizer. Only faster-whisper is implemented.
	Backend string `yaml:"backend"`
	// Model is a faster-whisper model name (tiny/base/small/medium/large-v3) or
	// a path to a converted CTranslate2 model directory.
	Model string `yaml:"model"`
	// Language is a two-letter code. Empty means autodetect, which costs one
	// extra pass over the first 30 seconds.
	Language string `yaml:"language"`
	// Device is auto, cpu or cuda.
	Device string `yaml:"device"`
	// ComputeType is auto, int8, int8_float16, float16 or float32.
	ComputeType string `yaml:"compute_type"`
	// ModelDir overrides where downloaded models are cached.
	ModelDir string `yaml:"model_dir"`
	// Threads caps CPU threads. Zero lets the backend decide.
	Threads int `yaml:"threads"`
	// VAD drops silence before recognition. It is on by default because
	// whisper hallucinates text into long silences.
	VAD bool `yaml:"vad"`
}

// Subtitle holds the default look of rendered subtitles. Colors are ASS
// &HBBGGRR (blue-green-red), which is the order libass reads, not RGB.
type Subtitle struct {
	Font         string  `yaml:"font"`
	FontSize     int     `yaml:"font_size"`
	PrimaryColor string  `yaml:"primary_color"`
	OutlineColor string  `yaml:"outline_color"`
	Outline      float64 `yaml:"outline"`
	Shadow       float64 `yaml:"shadow"`
	// MarginV is the distance from the frame edge in pixels.
	MarginV int `yaml:"margin_v"`
	// Position is bottom, center or top.
	Position string `yaml:"position"`
	// MaxChars is the soft line-length cap used when splitting cues.
	MaxChars int `yaml:"max_chars"`
	// MaxLines caps how many lines one cue may occupy.
	MaxLines int `yaml:"max_lines"`
}

// Filler configures what counts as a disfluency worth cutting.
type Filler struct {
	// ExtraWords are added to the built-in disfluency list.
	ExtraWords []string `yaml:"extra_words"`
	// KeepWords are removed from the built-in list, for speakers whose "嗯"
	// carries meaning.
	KeepWords []string `yaml:"keep_words"`
	// MaxPause is the longest silence kept intact. Longer gaps are trimmed
	// down to it rather than removed outright, because a cut to zero makes
	// speech sound spliced.
	MaxPause time.Duration `yaml:"max_pause"`
	// PadHead and PadTail widen every kept range so a cut never clips the
	// attack of the following word or the release of the previous one.
	PadHead time.Duration `yaml:"pad_head"`
	PadTail time.Duration `yaml:"pad_tail"`
	// MinKeep drops surviving fragments shorter than this: a 40 ms island of
	// speech between two cuts reads as a glitch, not as a word.
	MinKeep time.Duration `yaml:"min_keep"`
}

// Encode holds the default output encoding. These apply whenever a command has
// to re-encode; commands that can pass the stream through untouched do so.
type Encode struct {
	VideoCodec string `yaml:"video_codec"`
	AudioCodec string `yaml:"audio_codec"`
	// CRF is the x264/x265 quality factor: lower is better, 18 is visually
	// lossless, 23 is the ffmpeg default.
	CRF int `yaml:"crf"`
	// Preset trades encode speed for file size.
	Preset string `yaml:"preset"`
	// AudioBitrate is an ffmpeg bitrate string such as 192k.
	AudioBitrate string `yaml:"audio_bitrate"`
}

// Logging controls diagnostic output. Normal command output does not go
// through the logger.
type Logging struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Credentials selects where secrets are stored. It holds no secret itself.
type Credentials struct {
	// Backend is auto, keychain, vault or env. See internal/credential.
	Backend string `yaml:"backend"`
	// VaultPath overrides the encrypted vault location. Empty means a file
	// named credentials.vault inside the user config directory.
	VaultPath string `yaml:"vault_path"`
}

// Protocol identifiers for Provider.Protocol.
const (
	// ProtocolOpenAI is the OpenAI Chat Completions wire format, which most
	// vendors expose a compatible endpoint for.
	ProtocolOpenAI = "openai"
)

// Provider describes one model provider, for the commands that call a hosted
// model rather than a local binary.
//
// It carries no API key, by design. The key is read from the credential store
// under credential.ProviderKey(Name) at the moment a request is built.
type Provider struct {
	Name     string `yaml:"name"`
	BaseURL  string `yaml:"base_url"`
	Model    string `yaml:"model"`
	Protocol string `yaml:"protocol"`
}

// WireProtocol returns the protocol to use, applying the default.
func (p Provider) WireProtocol() string {
	if strings.TrimSpace(p.Protocol) == "" {
		return ProtocolOpenAI
	}
	return strings.ToLower(strings.TrimSpace(p.Protocol))
}

// Provider returns the named provider.
func (c Config) Provider(name string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Tools: Tools{FFmpeg: "ffmpeg", FFprobe: "ffprobe", Python: "python3"},
		ASR: ASR{
			Backend: "faster-whisper", Model: "small", Device: "auto",
			ComputeType: "auto", VAD: true,
		},
		Subtitle: Subtitle{
			Font: "", FontSize: 42,
			PrimaryColor: "&H00FFFFFF", OutlineColor: "&H00000000",
			Outline: 2, Shadow: 0, MarginV: 60,
			Position: "bottom", MaxChars: 20, MaxLines: 2,
		},
		Filler: Filler{
			MaxPause: 700 * time.Millisecond,
			PadHead:  60 * time.Millisecond,
			PadTail:  80 * time.Millisecond,
			MinKeep:  200 * time.Millisecond,
		},
		Encode: Encode{
			VideoCodec: "libx264", AudioCodec: "aac",
			CRF: 20, Preset: "medium", AudioBitrate: "192k",
		},
		Logging:     Logging{Level: "warn", Format: "text"},
		Credentials: Credentials{Backend: "auto"},
	}
}

// Dir is the per-user configuration directory, honoring XDG_CONFIG_HOME.
func Dir() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "vs")
	}
	return ".vs"
}

// DefaultPath is where Load looks when given no explicit path.
func DefaultPath() string {
	if v := strings.TrimSpace(os.Getenv("VS_CONFIG")); v != "" {
		return v
	}
	return filepath.Join(Dir(), "config.yaml")
}

// Load builds the configuration from defaults, an optional YAML file and the
// environment. A missing file is not an error: that is the zero-configuration
// path, and it is the one most invocations take.
func Load(path string) (Config, error) {
	cfg := Default()

	if strings.TrimSpace(path) == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	case os.IsNotExist(err):
		// Defaults plus environment are a valid configuration.
	default:
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	applyEnv(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	setString(&cfg.Tools.FFmpeg, "VS_FFMPEG")
	setString(&cfg.Tools.FFprobe, "VS_FFPROBE")
	setString(&cfg.Tools.Python, "VS_PYTHON")
	setString(&cfg.ASR.Backend, "VS_ASR_BACKEND")
	setString(&cfg.ASR.Model, "VS_ASR_MODEL")
	setString(&cfg.ASR.Language, "VS_ASR_LANGUAGE")
	setString(&cfg.ASR.Device, "VS_ASR_DEVICE")
	setString(&cfg.ASR.ComputeType, "VS_ASR_COMPUTE_TYPE")
	setString(&cfg.ASR.ModelDir, "VS_ASR_MODEL_DIR")
	setInt(&cfg.ASR.Threads, "VS_ASR_THREADS")
	setBool(&cfg.ASR.VAD, "VS_ASR_VAD")
	setString(&cfg.Subtitle.Font, "VS_SUBTITLE_FONT")
	setInt(&cfg.Subtitle.FontSize, "VS_SUBTITLE_FONT_SIZE")
	setInt(&cfg.Subtitle.MaxChars, "VS_SUBTITLE_MAX_CHARS")
	setString(&cfg.Encode.VideoCodec, "VS_VIDEO_CODEC")
	setString(&cfg.Encode.AudioCodec, "VS_AUDIO_CODEC")
	setInt(&cfg.Encode.CRF, "VS_CRF")
	setString(&cfg.Encode.Preset, "VS_PRESET")
	setString(&cfg.Logging.Level, "VS_LOG_LEVEL")
	setString(&cfg.Logging.Format, "VS_LOG_FORMAT")
	setString(&cfg.Credentials.Backend, "VS_CREDENTIALS_BACKEND")
	setString(&cfg.Credentials.VaultPath, "VS_CREDENTIALS_VAULT_PATH")
	setDuration(&cfg.Filler.MaxPause, "VS_FILLER_MAX_PAUSE")
	setDuration(&cfg.Filler.MinKeep, "VS_FILLER_MIN_KEEP")
}

// Validate rejects configurations that would fail later in confusing ways.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Tools.FFmpeg) == "" {
		return fmt.Errorf("tools.ffmpeg must not be empty")
	}
	if strings.TrimSpace(c.Tools.FFprobe) == "" {
		return fmt.Errorf("tools.ffprobe must not be empty")
	}
	if c.ASR.Backend != "faster-whisper" {
		return fmt.Errorf("asr.backend must be faster-whisper, got %q", c.ASR.Backend)
	}
	if strings.TrimSpace(c.ASR.Model) == "" {
		return fmt.Errorf("asr.model must not be empty")
	}
	switch strings.ToLower(strings.TrimSpace(c.ASR.Device)) {
	case "auto", "cpu", "cuda":
	default:
		return fmt.Errorf("asr.device must be auto, cpu or cuda, got %q", c.ASR.Device)
	}
	if c.ASR.Threads < 0 {
		return fmt.Errorf("asr.threads must not be negative, got %d", c.ASR.Threads)
	}
	if c.Subtitle.FontSize <= 0 {
		return fmt.Errorf("subtitle.font_size must be positive, got %d", c.Subtitle.FontSize)
	}
	if c.Subtitle.MaxChars <= 0 {
		return fmt.Errorf("subtitle.max_chars must be positive, got %d", c.Subtitle.MaxChars)
	}
	if c.Subtitle.MaxLines <= 0 {
		return fmt.Errorf("subtitle.max_lines must be positive, got %d", c.Subtitle.MaxLines)
	}
	switch strings.ToLower(strings.TrimSpace(c.Subtitle.Position)) {
	case "bottom", "center", "top":
	default:
		return fmt.Errorf("subtitle.position must be bottom, center or top, got %q", c.Subtitle.Position)
	}
	if c.Encode.CRF < 0 || c.Encode.CRF > 51 {
		return fmt.Errorf("encode.crf must be within 0..51, got %d", c.Encode.CRF)
	}
	if c.Filler.MaxPause < 0 || c.Filler.PadHead < 0 || c.Filler.PadTail < 0 || c.Filler.MinKeep < 0 {
		return fmt.Errorf("filler durations must not be negative")
	}

	seen := make(map[string]bool, len(c.Providers))
	for _, p := range c.Providers {
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("provider name must not be empty")
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate provider name %q", p.Name)
		}
		seen[p.Name] = true

		if p.WireProtocol() != ProtocolOpenAI {
			return fmt.Errorf("provider %q uses unsupported protocol %q: only %q is implemented",
				p.Name, p.Protocol, ProtocolOpenAI)
		}
	}
	return nil
}

// VaultPath is where the encrypted credential vault lives.
func (c Config) VaultPath() string {
	if path := strings.TrimSpace(c.Credentials.VaultPath); path != "" {
		return path
	}
	return filepath.Join(Dir(), "credentials.vault")
}

func setString(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		*dst = v
	}
}

func setInt(dst *int, key string) {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

func setBool(dst *bool, key string) {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			*dst = b
		}
	}
}

func setDuration(dst *time.Duration, key string) {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			*dst = d
		}
	}
}
