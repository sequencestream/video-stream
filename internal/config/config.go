// Package config loads the unified configuration shared by the daemon and CLI.
//
// Precedence is: built-in defaults < YAML file < VS_-prefixed environment
// variables.
//
// Nothing in this package holds a secret. Provider entries carry only metadata,
// and the key itself is fetched from internal/credential at the moment of use.
// That is deliberate: a Config is passed around widely and serialised by the
// meta endpoint, so a plaintext field here would be one careless log line away
// from leaking. See doc/arch/credentials.md.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sequencestream/video-stream/internal/compliance"
	"gopkg.in/yaml.v3"
)

// Config is the full runtime configuration.
type Config struct {
	Server      Server      `yaml:"server"`
	Sidecar     Sidecar     `yaml:"sidecar"`
	Storage     Storage     `yaml:"storage"`
	Budget      Budget      `yaml:"budget"`
	Logging     Logging     `yaml:"logging"`
	Telemetry   Telemetry   `yaml:"telemetry"`
	Queue       Queue       `yaml:"queue"`
	Credentials Credentials `yaml:"credentials"`
	Providers   []Provider  `yaml:"providers"`
	Radar       Radar       `yaml:"radar"`
	ScriptAgents ScriptAgents `yaml:"script_agents"`
	Compliance   Compliance   `yaml:"compliance"`
	Notifications Notifications `yaml:"notifications"`
}

// Radar controls the competitor radar polling schedule and rate limits.
type Radar struct {
	// Interval is how often the daemon polls watched accounts. Zero disables
	// background polling; readings can still be ingested over HTTP.
	Interval time.Duration `yaml:"interval"`
	// PerMinute caps outbound requests to each platform. Zero means unlimited,
	// which is appropriate for tests and local fixtures only.
	PerMinute float64 `yaml:"per_minute"`
	// Burst is how many requests may be sent back-to-back after an idle period.
	Burst int `yaml:"burst"`
}

// ScriptAgents controls the multi-agent script polish loop termination thresholds.
type ScriptAgents struct {
	MaxRounds            int     `yaml:"max_rounds"`
	MetricImprovementMin float64 `yaml:"metric_improvement_min"`
	MaxNewIssues         int     `yaml:"max_new_issues"`
	StagnantRounds       int     `yaml:"stagnant_rounds"`
	CostPer1KTokensMicros int64  `yaml:"cost_per_1k_tokens_micros"`
}

// Notifications configures completion webhook and email channels.
type Notifications struct {
	WebhookURL string `yaml:"webhook_url" json:"webhook_url"`
	EmailTo    string `yaml:"email_to" json:"email_to"`
	SMTPHost   string `yaml:"smtp_host" json:"smtp_host"`
	SMTPPort   int    `yaml:"smtp_port" json:"smtp_port"`
	SMTPFrom   string `yaml:"smtp_from" json:"smtp_from"`
	SMTPUser   string `yaml:"smtp_user" json:"smtp_user"`
}

// Compliance controls inauthentic-differentiation gate thresholds.
type Compliance struct {
	RejectSimilarity float64 `yaml:"reject_similarity"`
	PassSimilarity   float64 `yaml:"pass_similarity"`
	ReuseWindowDays  int     `yaml:"reuse_window_days"`
	MaxReuses        int     `yaml:"max_reuses"`
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

// Protocol identifiers for Provider.Protocol.
const (
	// ProtocolOpenAI is the OpenAI Chat Completions wire format, which most
	// vendors expose a compatible endpoint for.
	ProtocolOpenAI = "openai"
)

// Credentials selects where secrets are stored. It holds no secret itself.
type Credentials struct {
	// Backend is auto, keychain, vault or env. See internal/credential.
	Backend string `yaml:"backend"`
	// VaultPath overrides the encrypted vault location. Empty means a file
	// named credentials.vault inside Storage.DataDir.
	VaultPath string `yaml:"vault_path"`
}

// Provider describes one model provider.
//
// It carries no API key, by design. The key is read from the credential store
// under credential.ProviderKey(Name) at the moment a request is built.
type Provider struct {
	Name    string `yaml:"name"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
	// Protocol is the wire format to speak. Empty means ProtocolOpenAI.
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
		Server:      Server{Addr: ":8080"},
		Sidecar:     Sidecar{BaseURL: "http://127.0.0.1:8090", Timeout: 5 * time.Second},
		Storage:     Storage{DataDir: "./data", OutputDir: "./output"},
		Budget:      Budget{MaxCostPerVideoUSD: 2.0, DailyCapUSD: 20.0},
		Logging:     Logging{Level: "info", Format: "json"},
		Queue:       Queue{Workers: 2, Buffer: 64},
		Credentials: Credentials{Backend: "auto"},
		Radar: Radar{
			Interval:  6 * time.Hour,
			PerMinute: 6,
			Burst:     1,
		},
		ScriptAgents: ScriptAgents{
			MaxRounds:            3,
			MetricImprovementMin: 0.03,
			MaxNewIssues:         1,
			StagnantRounds:       2,
			CostPer1KTokensMicros: 500,
		},
		Compliance: Compliance{
			RejectSimilarity: 0.85,
			PassSimilarity:   0.70,
			ReuseWindowDays:  30,
			MaxReuses:        3,
		},
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
	setString(&cfg.Credentials.Backend, "VS_CREDENTIALS_BACKEND")
	setString(&cfg.Credentials.VaultPath, "VS_CREDENTIALS_VAULT_PATH")
	setDuration(&cfg.Radar.Interval, "VS_RADAR_INTERVAL")
	setFloat(&cfg.Radar.PerMinute, "VS_RADAR_PER_MINUTE")
	setInt(&cfg.Radar.Burst, "VS_RADAR_BURST")
	setInt(&cfg.ScriptAgents.MaxRounds, "VS_SCRIPT_MAX_ROUNDS")
	setFloat(&cfg.ScriptAgents.MetricImprovementMin, "VS_SCRIPT_METRIC_IMPROVEMENT_MIN")
	setInt(&cfg.ScriptAgents.MaxNewIssues, "VS_SCRIPT_MAX_NEW_ISSUES")
	setInt(&cfg.ScriptAgents.StagnantRounds, "VS_SCRIPT_STAGNANT_ROUNDS")
	setInt64(&cfg.ScriptAgents.CostPer1KTokensMicros, "VS_SCRIPT_COST_PER_1K_TOKENS_MICROS")
	setFloat(&cfg.Compliance.RejectSimilarity, "VS_COMPLIANCE_REJECT_SIMILARITY")
	setFloat(&cfg.Compliance.PassSimilarity, "VS_COMPLIANCE_PASS_SIMILARITY")
	setInt(&cfg.Compliance.ReuseWindowDays, "VS_COMPLIANCE_REUSE_WINDOW_DAYS")
	setInt(&cfg.Compliance.MaxReuses, "VS_COMPLIANCE_MAX_REUSES")
	setString(&cfg.Notifications.WebhookURL, "VS_NOTIFY_WEBHOOK_URL")
	setString(&cfg.Notifications.EmailTo, "VS_NOTIFY_EMAIL_TO")
	setString(&cfg.Notifications.SMTPHost, "VS_SMTP_HOST")
	setInt(&cfg.Notifications.SMTPPort, "VS_SMTP_PORT")
	setString(&cfg.Notifications.SMTPFrom, "VS_SMTP_FROM")
	setString(&cfg.Notifications.SMTPUser, "VS_SMTP_USER")
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
	if c.Radar.Interval < 0 {
		return fmt.Errorf("radar.interval must not be negative, got %s", c.Radar.Interval)
	}
	if c.Radar.PerMinute < 0 {
		return fmt.Errorf("radar.per_minute must not be negative, got %g", c.Radar.PerMinute)
	}
	if c.Radar.Burst < 0 {
		return fmt.Errorf("radar.burst must not be negative, got %d", c.Radar.Burst)
	}
	if c.ScriptAgents.MaxRounds < 1 {
		return fmt.Errorf("script_agents.max_rounds must be at least 1, got %d", c.ScriptAgents.MaxRounds)
	}
	if c.ScriptAgents.MetricImprovementMin < 0 {
		return fmt.Errorf("script_agents.metric_improvement_min must not be negative")
	}
	if c.ScriptAgents.StagnantRounds < 1 {
		return fmt.Errorf("script_agents.stagnant_rounds must be at least 1")
	}
	if err := (compliance.Config{
		RejectSimilarity: c.Compliance.RejectSimilarity,
		PassSimilarity:   c.Compliance.PassSimilarity,
		ReuseWindowDays:  c.Compliance.ReuseWindowDays,
		MaxReuses:        c.Compliance.MaxReuses,
	}).Effective().Validate(); err != nil {
		return fmt.Errorf("compliance: %w", err)
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

// DatabasePath is the SQLite file backing task persistence.
func (c Config) DatabasePath() string {
	return filepath.Join(c.Storage.DataDir, "video-stream.db")
}

// VaultPath is where the encrypted credential vault lives. It defaults to a
// file beside the task database so a user backing up the data directory takes
// their credentials with them.
func (c Config) VaultPath() string {
	if path := strings.TrimSpace(c.Credentials.VaultPath); path != "" {
		return path
	}
	return filepath.Join(c.Storage.DataDir, "credentials.vault")
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

func setInt64(dst *int64, key string) {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
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
