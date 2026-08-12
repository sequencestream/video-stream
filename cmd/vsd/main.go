// Command vsd is the video-stream main service: HTTP API plus the in-process
// task queue, shipped as a single binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sequencestream/video-stream/internal/audio"
	"github.com/sequencestream/video-stream/internal/compliance"
	"github.com/sequencestream/video-stream/internal/config"
	"github.com/sequencestream/video-stream/internal/costwarden"
	"github.com/sequencestream/video-stream/internal/credential"
	"github.com/sequencestream/video-stream/internal/httpapi"
	"github.com/sequencestream/video-stream/internal/hybrid"
	"github.com/sequencestream/video-stream/internal/ideation"
	"github.com/sequencestream/video-stream/internal/logging"
	"github.com/sequencestream/video-stream/internal/provider"
	"github.com/sequencestream/video-stream/internal/queue"
	"github.com/sequencestream/video-stream/internal/radar"
	"github.com/sequencestream/video-stream/internal/recompile"
	"github.com/sequencestream/video-stream/internal/render"
	"github.com/sequencestream/video-stream/internal/scriptagents"
	"github.com/sequencestream/video-stream/internal/sidecar"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/tasks"
	"github.com/sequencestream/video-stream/internal/telemetry"
	"github.com/sequencestream/video-stream/internal/visual"
	"github.com/sequencestream/video-stream/internal/webui"
	"github.com/sequencestream/video-stream/internal/wizard"
	"github.com/sequencestream/video-stream/internal/youtube"
	"github.com/sequencestream/video-stream/internal/youtube/notify"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "vsd: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", envOr("VS_CONFIG", "config.yaml"), "path to the YAML config file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	logger := logging.New(os.Stdout, logging.Options{Level: cfg.Logging.Level, Format: cfg.Logging.Format})
	slog.SetDefault(logger)

	reporter := telemetry.Reporter(telemetry.Nop())
	if cfg.Telemetry.Enabled {
		reporter = telemetry.NewLogReporter(logger)
	}

	if err := os.MkdirAll(cfg.Storage.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir %s: %w", cfg.Storage.OutputDir, err)
	}

	taskStore, err := store.OpenSQLite(cfg.DatabasePath())
	if err != nil {
		return err
	}
	defer taskStore.Close()

	sidecarClient := sidecar.New(cfg.Sidecar.BaseURL, cfg.Sidecar.Timeout)

	credentials, err := credential.Open(credential.Options{
		Backend:   cfg.Credentials.Backend,
		VaultPath: cfg.VaultPath(),
		// The daemon has no terminal to prompt on, so an encrypted vault can
		// only be unlocked through the environment here. A locked vault is not
		// fatal: the affected providers simply report no credential, which is
		// better than refusing to start the whole service over one optional key.
		VaultPassphrase: os.Getenv("VS_VAULT_PASSPHRASE"),
	})
	if err != nil {
		return err
	}
	logger.Info("credential store ready", slog.String("backend", credentials.Name()))

	providers := provider.New(provider.Options{
		Providers:   cfg.Providers,
		Credentials: credentials,
	})

	registry := queue.NewRegistry()
	var tts audio.TTS
	switch cfg.Audio.Provider {
	case "edge":
		tts = audio.EdgeTTS{
			OutputDir: cfg.Storage.OutputDir, DefaultVoice: cfg.Audio.DefaultVoice,
			PythonBinary: cfg.Audio.PythonBinary, FFmpegBinary: cfg.Audio.FFmpegBinary,
		}
	case "stub":
		tts = audio.StubTTS{MSPerWord: 180}
	}
	audioEngine := audio.New(audio.Options{
		OutputDir: cfg.Storage.OutputDir, Reporter: reporter, TTS: tts,
		FFmpegBinary: cfg.Audio.FFmpegBinary,
	})
	costEngine := costwarden.New(costwarden.Options{Projects: taskStore, Reporter: reporter})
	youtubeEngine := youtube.New(youtube.Options{
		Store: taskStore, Credentials: credentials,
		Notifier:  notify.FromConfig(cfg.Notifications),
		OutputDir: cfg.Storage.OutputDir, Reporter: reporter,
	})
	renderEngine := render.New(render.Options{
		Store: taskStore, Artifacts: taskStore, OutputDir: cfg.Storage.OutputDir,
		Reporter: reporter, Audio: audioEngine,
	})

	tasks.Register(registry, tasks.Deps{
		Sidecar:   sidecarClient,
		Providers: providers,
		Projects:  taskStore,
		Render:    renderEngine,
	})

	// Wired from day one so the invalidation rate report is reachable from the
	// first render, not after someone remembers to add the route.
	recompiler := recompile.New(recompile.Options{
		Cache:    taskStore,
		Runs:     taskStore,
		Reporter: reporter,
		Logger:   logger,
	})

	radarPoller := radar.NewPoller(cfg.Radar.PerMinute, cfg.Radar.Burst, logger)
	radarEngine := radar.New(radar.Options{
		Store:    taskStore,
		Poller:   radarPoller,
		Reporter: reporter,
		Logger:   logger,
	})

	ideationEngine := ideation.New(ideation.Options{
		Store:    taskStore,
		Reporter: reporter,
		Logger:   logger,
	})

	scriptEngine := scriptagents.New(scriptagents.Options{
		Store: taskStore,
		Termination: scriptagents.TerminationConfig{
			MaxRounds:             cfg.ScriptAgents.MaxRounds,
			MetricImprovementMin:  cfg.ScriptAgents.MetricImprovementMin,
			MaxNewIssues:          cfg.ScriptAgents.MaxNewIssues,
			StagnantRounds:        cfg.ScriptAgents.StagnantRounds,
			CostPer1KTokensMicros: cfg.ScriptAgents.CostPer1KTokensMicros,
		},
		Reporter: reporter,
		Logger:   logger,
	})

	complianceEngine, err := compliance.New(compliance.Options{
		Store: taskStore,
		Config: compliance.Config{
			RejectSimilarity: cfg.Compliance.RejectSimilarity,
			PassSimilarity:   cfg.Compliance.PassSimilarity,
			ReuseWindowDays:  cfg.Compliance.ReuseWindowDays,
			MaxReuses:        cfg.Compliance.MaxReuses,
		},
		Reporter: reporter,
		Logger:   logger,
	})
	if err != nil {
		return err
	}

	visualEngine := visual.New(visual.Options{Store: taskStore, Reporter: reporter, Logger: logger})
	hybridEngine := hybrid.New(hybrid.Options{Store: taskStore, Reporter: reporter, Logger: logger})
	wizardEngine := wizard.New(wizard.Options{
		Store: taskStore, Projects: taskStore,
		Radar: radarEngine, Ideation: ideationEngine, Script: scriptEngine,
		Hybrid: hybridEngine, Compliance: complianceEngine, Render: renderEngine,
		Recompile: recompiler, CostWarden: costEngine, YouTube: youtubeEngine, Reporter: reporter,
	})
	if recovered, err := wizardEngine.RecoverInterrupted(context.Background()); err != nil {
		return fmt.Errorf("recover interrupted wizard operations: %w", err)
	} else if recovered > 0 {
		logger.Warn("recovered interrupted wizard operations", slog.Int("count", recovered))
	}

	q := queue.NewInProcess(queue.Options{
		Store:    taskStore,
		Registry: registry,
		Logger:   logger,
		Reporter: reporter,
		Workers:  cfg.Queue.Workers,
		Buffer:   cfg.Queue.Buffer,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := q.Start(ctx); err != nil {
		return err
	}

	if cfg.Radar.Interval > 0 {
		go runRadarPolling(ctx, logger, radarEngine, cfg.Radar.Interval)
	}

	logger.Info("webui", slog.Bool("embedded", webui.Built()))

	api := httpapi.NewServer(httpapi.Deps{
		Config:       cfg,
		Store:        taskStore,
		Queue:        q,
		Sidecar:      sidecarClient,
		Credentials:  credentials,
		Recompile:    recompiler,
		Radar:        radarEngine,
		Ideation:     ideationEngine,
		ScriptAgents: scriptEngine,
		Compliance:   complianceEngine,
		Visual:       visualEngine,
		Hybrid:       hybridEngine,
		Render:       renderEngine,
		Audio:        audioEngine,
		CostWarden:   costEngine,
		YouTube:      youtubeEngine,
		Wizard:       wizardEngine,
		WebUI:        webui.Handler(),
		Logger:       logger,
		Version:      version,
	})

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("main service listening",
			slog.String("addr", cfg.Server.Addr),
			slog.String("version", version),
			slog.String("sidecar", cfg.Sidecar.BaseURL),
			slog.String("database", cfg.DatabasePath()),
			slog.String("output_dir", cfg.Storage.OutputDir))

		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown", slog.String("error", err.Error()))
	}
	if err := q.Stop(shutdownCtx); err != nil {
		logger.Error("queue shutdown", slog.String("error", err.Error()))
	}
	if err := reporter.Flush(shutdownCtx); err != nil {
		logger.Error("telemetry flush", slog.String("error", err.Error()))
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func runRadarPolling(ctx context.Context, logger *slog.Logger, engine *radar.Engine, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := engine.PollOnce(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("radar poll failed", slog.String("error", err.Error()))
				continue
			}
			logger.Info("radar poll finished",
				slog.Int("polled", result.Polled),
				slog.Int("readings", len(result.Readings)),
				slog.Int("no_source", result.NoSource),
				slog.Int("rate_limited", result.RateLimited),
				slog.Int("failed", result.Failed))
		}
	}
}
