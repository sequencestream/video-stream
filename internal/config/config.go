// Package config loads the unified configuration shared by the daemon and CLI.
//
// Precedence is: built-in defaults < YAML file < VS_-prefixed environment
// variables. Model provider API keys are deliberately never read from the YAML
// file; they are resolved from the environment variable named by the provider's
// api_key_env field so that credentials cannot be committed to the repository.
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
	Server    Server     `yaml:"server"`
	Sidecar   Sidecar    `yaml:"sidecar"`
	Storage   Storage    `yaml:"storage"`
	Budget    Budget     `yaml:"budget"`
	Logging   Logging    `yaml:"logging"`
	Telemetry Telemetry  `yaml:"telemetry"`
	Queue     Queue      `yaml:"queue"`
	Providers []Provider `yaml:"providers"`
}

// Server holds the HTTP listener settings of the main service.
type Server struct {
	Addr string `yaml:"addr"`
}

// Sidecar holds the connection settings for the Python sidecar process.
type Sidecar struct {
	BaseURL string        `yaml:"base_url"`
	Timeout time.Duration `yaml:"timeout"`
}

// Storage holds on-disk locations owned by the main service.
type Storage struct {
	// DataDir holds internal state such as the task database.
	DataDir string `yaml:"data_dir"`
	// OutputDir holds rendered artifacts handed back to the user.
	OutputDir string `yaml:"output_dir"`
}

// Budget caps spend so a runaway pipeline cannot bill indefinitely. It is
// serialised to JSON by the meta endpoint, hence the json tags.
type Budget struct {
	// MaxCostPerVideoUSD is the ceiling for a single video job.
	MaxCostPerVideoUSD float64 `yaml:"max_cost_per_video_usd" json:"max_cost_per_video_usd"`
	// DailyCapUSD is the ceiling across all jobs in a rolling day.
	DailyCapUSD float64 `yaml:"daily_cap_usd" json:"daily_cap_usd"`
}

// Logging controls the structured logger.
type Logging struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Telemetry controls the event reporting sink.
type Telemetry struct {
	// Enabled turns the log reporter on; when false a no-op reporter is used.
	Enabled bool `yaml:"enabled"`
	// Endpoint is reserved for a future remote sink. Unused in the MVP.
	Endpoint string `yaml:"endpoint"`
}

// Queue controls the in-process task queue.
type Queue struct {
	// Workers is the number of concurrent task workers.
	Workers int `yaml:"workers"`
	// Buffer is the dispatch channel depth.
	Buffer int `yaml:"buffer"`
}

// Provider describes one model provider. APIKey is never populated from YAML.
type Provider struct {
	Name      string `yaml:"name"`
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	APIKeyEnv string `yaml:"api_key_env"`

	// APIKey is resolved from the environment at load time and is not
	// serialised back out.
	APIKey string `yaml:"-"`
}

// HasCredential reports whether the provider's API key was found in the
// environment.
func (p Provider) HasCredential() bool { return p.APIKey != "" }

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Server:  Server{Addr: ":8080"},
		Sidecar: Sidecar{BaseURL: "http://127.0.0.1:8090", Timeout: 5 * time.Second},
		Storage: Storage{DataDir: "./data", OutputDir: "./output"},
		Budget:  Budget{MaxCostPerVideoUSD: 2.0, DailyCapUSD: 20.0},
		Logging: Logging{Level: "info", Format: "json"},
		Queue:   Queue{Workers: 2, Buffer: 64},
	}
}

// Load builds the configuration from defaults, an optional YAML file and the
// environment. A missing file at path is not an error, which keeps the
// zero-configuration path working.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
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
	}

	applyEnv(&cfg)
	resolveCredentials(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	setString(&cfg.Server.Addr, "VS_SERVER_ADDR")
	setString(&cfg.Sidecar.BaseURL, "VS_SIDECAR_BASE_URL")
	setDuration(&cfg.Sidecar.Timeout, "VS_SIDECAR_TIMEOUT")
	setString(&cfg.Storage.DataDir, "VS_DATA_DIR")
	setString(&cfg.Storage.OutputDir, "VS_OUTPUT_DIR")
	setFloat(&cfg.Budget.MaxCostPerVideoUSD, "VS_MAX_COST_PER_VIDEO_USD")
	setFloat(&cfg.Budget.DailyCapUSD, "VS_DAILY_CAP_USD")
	setString(&cfg.Logging.Level, "VS_LOG_LEVEL")
	setString(&cfg.Logging.Format, "VS_LOG_FORMAT")
	setBool(&cfg.Telemetry.Enabled, "VS_TELEMETRY_ENABLED")
	setString(&cfg.Telemetry.Endpoint, "VS_TELEMETRY_ENDPOINT")
	setInt(&cfg.Queue.Workers, "VS_QUEUE_WORKERS")
	setInt(&cfg.Queue.Buffer, "VS_QUEUE_BUFFER")
}

func resolveCredentials(cfg *Config) {
	for i := range cfg.Providers {
		if env := cfg.Providers[i].APIKeyEnv; env != "" {
			cfg.Providers[i].APIKey = os.Getenv(env)
		}
	}
}

// Validate rejects configurations that would fail later in confusing ways.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Server.Addr) == "" {
		return fmt.Errorf("server.addr must not be empty")
	}
	if strings.TrimSpace(c.Sidecar.BaseURL) == "" {
		return fmt.Errorf("sidecar.base_url must not be empty")
	}
	if c.Sidecar.Timeout <= 0 {
		return fmt.Errorf("sidecar.timeout must be positive, got %s", c.Sidecar.Timeout)
	}
	if c.Queue.Workers < 1 {
		return fmt.Errorf("queue.workers must be at least 1, got %d", c.Queue.Workers)
	}
	if c.Queue.Buffer < 1 {
		return fmt.Errorf("queue.buffer must be at least 1, got %d", c.Queue.Buffer)
	}
	if c.Budget.MaxCostPerVideoUSD < 0 || c.Budget.DailyCapUSD < 0 {
		return fmt.Errorf("budget caps must not be negative")
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
	}
	return nil
}

// DatabasePath is the SQLite file backing task persistence.
func (c Config) DatabasePath() string {
	return filepath.Join(c.Storage.DataDir, "video-stream.db")
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

func setFloat(dst *float64, key string) {
	if v, ok := os.LookupEnv(key); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			*dst = f
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
