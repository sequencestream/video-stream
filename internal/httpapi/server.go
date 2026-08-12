// Package httpapi exposes the main service's HTTP surface: health probes, the
// task API used by both the CLI and the WebUI, and a redacted metadata view of
// the loaded configuration.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/sequencestream/video-stream/internal/audio"
	"github.com/sequencestream/video-stream/internal/compliance"
	"github.com/sequencestream/video-stream/internal/config"
	"github.com/sequencestream/video-stream/internal/costwarden"
	"github.com/sequencestream/video-stream/internal/credential"
	"github.com/sequencestream/video-stream/internal/hybrid"
	"github.com/sequencestream/video-stream/internal/ideation"
	"github.com/sequencestream/video-stream/internal/queue"
	"github.com/sequencestream/video-stream/internal/radar"
	"github.com/sequencestream/video-stream/internal/recompile"
	"github.com/sequencestream/video-stream/internal/render"
	"github.com/sequencestream/video-stream/internal/scriptagents"
	"github.com/sequencestream/video-stream/internal/sidecar"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/visual"
	"github.com/sequencestream/video-stream/internal/wizard"
	"github.com/sequencestream/video-stream/internal/youtube"
)

// Deps are the collaborators the HTTP handlers need.
type Deps struct {
	Config      config.Config
	Store       store.TaskStore
	Queue       queue.Queue
	Sidecar     *sidecar.Client
	Credentials *credential.Chain
	// Recompile backs the invalidation rate report. Nil leaves the route
	// registered and answering with an empty report, because "no runs
	// recorded" is the honest answer and a missing route would look like a
	// deployment fault.
	Recompile *recompile.Engine
	// Radar backs the competitor watch list and hot-post signals. Nil leaves the
	// routes registered and answering with empty collections, because "nothing
	// watched yet" is the honest state rather than a missing route.
	Radar *radar.Engine
	// Ideation backs structure card extraction and cross-category topic migration.
	// Nil leaves routes registered with empty collections for the same reason.
	Ideation *ideation.Engine
	// ScriptAgents backs the multi-agent script polish loop.
	ScriptAgents *scriptagents.Engine
	// Compliance backs the three inauthentic-differentiation gates before render.
	Compliance *compliance.Engine
	// Visual backs L2 style packs and the visual identity stack.
	Visual *visual.Engine
	// Hybrid backs per-seg visual route planning (AI / stock / Ken Burns / motion graphics).
	Hybrid *hybrid.Engine
	// Render backs the FFmpeg staged pipeline (720p preview / 1080p delivery).
	Render *render.Engine
	// Audio backs TTS synthesis, subtitles, and loudness normalization.
	Audio *audio.Engine
	// CostWarden backs script-stage cost estimation and degradation planning.
	CostWarden *costwarden.Engine
	// YouTube backs publish and delivery download.
	YouTube *youtube.Engine
	// Wizard backs the end-to-end seven-step product flow.
	Wizard *wizard.Engine
	// WebUI serves the embedded interface at "/". Nil leaves the root
	// unrouted, which is what the API tests want.
	WebUI   http.Handler
	Logger  *slog.Logger
	Version string
}

// Server owns the HTTP routes.
type Server struct {
	deps Deps
}

// NewServer builds the API server.
func NewServer(deps Deps) *Server {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Server{deps: deps}
}

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /v1/meta", s.handleMeta)
	mux.HandleFunc("POST /v1/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /v1/tasks", s.handleListTasks)
	mux.HandleFunc("GET /v1/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("GET /v1/recompile/report", s.handleRecompileReport)
	mux.HandleFunc("GET /v1/radar/accounts", s.handleRadarAccounts)
	mux.HandleFunc("POST /v1/radar/accounts", s.handleRadarAccounts)
	mux.HandleFunc("GET /v1/radar/signals", s.handleRadarSignals)
	mux.HandleFunc("POST /v1/radar/ingest", s.handleRadarIngest)
	mux.HandleFunc("POST /v1/radar/poll", s.handleRadarPoll)
	mux.HandleFunc("POST /v1/ideation/extract", s.handleIdeationExtract)
	mux.HandleFunc("GET /v1/ideation/cards", s.handleIdeationCards)
	mux.HandleFunc("GET /v1/ideation/cards/{id}", s.handleIdeationCardByID)
	mux.HandleFunc("POST /v1/ideation/migrate", s.handleIdeationMigrate)
	mux.HandleFunc("GET /v1/ideation/topics", s.handleIdeationTopics)
	mux.HandleFunc("POST /v1/ideation/recall", s.handleIdeationRecall)
	mux.HandleFunc("POST /v1/script/polish", s.handleScriptPolish)
	mux.HandleFunc("POST /v1/compliance/check", s.handleComplianceCheck)
	mux.HandleFunc("GET /v1/visual/packs", s.handleVisualPacks)
	mux.HandleFunc("POST /v1/visual/packs", s.handleVisualPacks)
	mux.HandleFunc("GET /v1/visual/packs/{id}", s.handleVisualPackByID)
	mux.HandleFunc("GET /v1/visual/packs/{id}/export", s.handleVisualPackExport)
	mux.HandleFunc("POST /v1/visual/packs/import", s.handleVisualPackImport)
	mux.HandleFunc("POST /v1/visual/packs/{id}/apply", s.handleVisualPackApply)
	mux.HandleFunc("POST /v1/hybrid/plan", s.handleHybridPlan)
	mux.HandleFunc("GET /v1/hybrid/plans/{project_id}", s.handleHybridPlans)
	mux.HandleFunc("POST /v1/render/run", s.handleRenderRun)
	mux.HandleFunc("GET /v1/render/runs/{id}", s.handleRenderRunByID)
	mux.HandleFunc("POST /v1/audio/synthesize", s.handleAudioSynthesize)
	mux.HandleFunc("GET /v1/audio/platforms", s.handleAudioPlatforms)
	mux.HandleFunc("POST /v1/cost/estimate", s.handleCostEstimate)
	mux.HandleFunc("POST /v1/cost/plan", s.handleCostPlan)
	mux.HandleFunc("GET /v1/cost/capabilities", s.handleCostCapabilities)
	mux.HandleFunc("POST /v1/youtube/publish", s.handleYouTubePublish)
	mux.HandleFunc("GET /v1/youtube/uploads/{id}", s.handleYouTubeUploadByID)
	mux.HandleFunc("GET /v1/delivery/download", s.handleDeliveryDownload)
	mux.HandleFunc("POST /v1/wizard/sessions", s.handleWizardCreate)
	mux.HandleFunc("GET /v1/wizard/sessions/{id}", s.handleWizardGet)
	mux.HandleFunc("POST /v1/wizard/sessions/{id}/advance", s.handleWizardAdvance)

	// The embedded UI takes the bare "/" pattern, which in net/http is the
	// catch-all. It is registered last so every API route above wins, and the
	// UI only sees what is left over.
	if s.deps.WebUI != nil {
		mux.Handle("/", s.deps.WebUI)
	}

	return s.withLogging(mux)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// Health probes fire constantly; logging them at info would bury
		// everything else.
		level := slog.LevelInfo
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			level = slog.LevelDebug
		}
		s.deps.Logger.Log(r.Context(), level, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Duration("duration", time.Since(started)))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// healthResponse is the self-report of the main service.
type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Version string `json:"version"`
}

// handleHealthz reports only on this process. It must stay independent of the
// sidecar, otherwise a sidecar outage would make the main service look dead and
// container orchestration would restart a healthy process.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, healthResponse{
		Status:  "ok",
		Service: "video-stream",
		Version: s.deps.Version,
	})
}

// readyResponse adds downstream dependency state.
type readyResponse struct {
	Status    string          `json:"status"`
	Version   string          `json:"version"`
	Sidecar   dependencyState `json:"sidecar"`
	TaskTypes []string        `json:"task_types"`
	CheckedAt time.Time       `json:"checked_at"`
}

type dependencyState struct {
	Reachable bool   `json:"reachable"`
	BaseURL   string `json:"base_url"`
	Status    string `json:"status,omitempty"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

// handleReadyz probes the sidecar. An unreachable sidecar downgrades readiness
// to "degraded" with HTTP 200: the main service still serves the task API, so
// reporting hard-unready would be misleading.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.deps.Config.Sidecar.Timeout)
	defer cancel()

	state := dependencyState{BaseURL: s.deps.Sidecar.BaseURL()}
	status := "ready"

	if health, err := s.deps.Sidecar.Health(ctx); err != nil {
		state.Error = err.Error()
		status = "degraded"
	} else {
		state.Reachable = true
		state.Status = health.Status
		state.Version = health.Version
	}

	writeJSON(w, r, http.StatusOK, readyResponse{
		Status:    status,
		Version:   s.deps.Version,
		Sidecar:   state,
		TaskTypes: s.deps.Queue.Types(),
		CheckedAt: time.Now().UTC(),
	})
}

// metaResponse is a redacted view of the loaded configuration.
type metaResponse struct {
	Version   string           `json:"version"`
	OutputDir string           `json:"output_dir"`
	Budget    config.Budget    `json:"budget"`
	TaskTypes []string         `json:"task_types"`
	Providers []providerStatus `json:"providers"`
}

// providerStatus never carries the key itself, only whether one resolved and
// which backend supplied it. The source is what makes "I set my key but it is
// not being used" diagnosable: it distinguishes a stale environment variable
// shadowing the keychain from no credential at all.
type providerStatus struct {
	Name           string `json:"name"`
	Model          string `json:"model,omitempty"`
	BaseURL        string `json:"base_url,omitempty"`
	Protocol       string `json:"protocol"`
	HasCredential  bool   `json:"has_credential"`
	CredentialFrom string `json:"credential_from,omitempty"`
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	providers := make([]providerStatus, 0, len(s.deps.Config.Providers))
	for _, p := range s.deps.Config.Providers {
		status := providerStatus{
			Name:     p.Name,
			Model:    p.Model,
			BaseURL:  p.BaseURL,
			Protocol: p.WireProtocol(),
		}
		if s.deps.Credentials != nil {
			status.CredentialFrom, status.HasCredential =
				s.deps.Credentials.Source(r.Context(), credential.ProviderKey(p.Name))
		}
		providers = append(providers, status)
	}

	writeJSON(w, r, http.StatusOK, metaResponse{
		Version:   s.deps.Version,
		OutputDir: s.deps.Config.Storage.OutputDir,
		Budget:    s.deps.Config.Budget,
		TaskTypes: s.deps.Queue.Types(),
		Providers: providers,
	})
}

// createTaskRequest is the body of POST /v1/tasks.
type createTaskRequest struct {
	Type    string         `json:"type"`
	Title   string         `json:"title"`
	Payload map[string]any `json:"payload,omitempty"`
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error())
		return
	}
	if req.Type == "" {
		writeError(w, r, http.StatusBadRequest, "missing_type", "field \"type\" is required")
		return
	}

	task, err := s.deps.Queue.Submit(r.Context(), queue.Submission{
		Type:    req.Type,
		Title:   req.Title,
		Payload: req.Payload,
	})
	if errors.Is(err, queue.ErrUnknownType) {
		writeError(w, r, http.StatusBadRequest, "unknown_task_type", err.Error())
		return
	}
	if err != nil {
		s.deps.Logger.Error("submit task", slog.String("error", err.Error()))
		writeError(w, r, http.StatusInternalServerError, "submit_failed", err.Error())
		return
	}

	writeJSON(w, r, http.StatusAccepted, task)
}

// listTasksResponse wraps the collection so the payload can gain fields such as
// pagination cursors without breaking clients.
type listTasksResponse struct {
	Tasks []store.Task `json:"tasks"`
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.deps.Store.List(r.Context(), 50)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if tasks == nil {
		tasks = []store.Task{}
	}
	writeJSON(w, r, http.StatusOK, listTasksResponse{Tasks: tasks})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.deps.Store.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "task_not_found", "no task with that id")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, task)
}

// recompileReportResponse is the invalidation rate view.
//
// The rates and the verdict are computed server-side and shipped alongside the
// raw counters. They exist so that everyone reading this number reads it the
// same way: a client dividing the counters itself would sooner or later average
// per run instead of per seg, and quietly report a friendlier figure than the
// one the scrap threshold is defined against.
type recompileReportResponse struct {
	Runs              int                        `json:"runs"`
	TotalSegs         int                        `json:"total_segs"`
	InvalidatedSegs   int                        `json:"invalidated_segs"`
	ReusedSegs        int                        `json:"reused_segs"`
	InvalidationRate  float64                    `json:"invalidation_rate"`
	ReuseRate         float64                    `json:"reuse_rate"`
	FullRerunRuns     int                        `json:"full_rerun_runs"`
	FullRerunRate     float64                    `json:"full_rerun_rate"`
	CostSavedMicros   int64                      `json:"cost_saved_micros"`
	ByBoundary        map[recompile.Boundary]int `json:"by_boundary,omitempty"`
	Verdict           recompile.Verdict          `json:"verdict"`
	ScrapThresholdPct int                        `json:"scrap_threshold_percent"`
	MinRunsForVerdict int                        `json:"min_runs_for_verdict"`
}

// handleRecompileReport answers with the invalidation rate to date.
//
// This is the only route the recompile engine gets. A report nobody can read
// without writing Go is not a measurement of the project's largest technical
// risk, which is what this engine was built to provide. Nothing here writes,
// and no project entry point is added: the shape of /v1/projects belongs to
// whichever intent actually edits projects over HTTP.
func (s *Server) handleRecompileReport(w http.ResponseWriter, r *http.Request) {
	if s.deps.Recompile == nil {
		writeJSON(w, r, http.StatusOK, recompileReportResponse{
			Verdict:           recompile.VerdictInsufficientData,
			ScrapThresholdPct: recompile.ScrapThresholdPercent,
			MinRunsForVerdict: recompile.MinRunsForVerdict,
		})
		return
	}

	report, err := s.deps.Recompile.Report(r.Context(), r.URL.Query().Get("project"))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "report_failed", err.Error())
		return
	}

	writeJSON(w, r, http.StatusOK, recompileReportResponse{
		Runs:              report.Runs,
		TotalSegs:         report.TotalSegs,
		InvalidatedSegs:   report.InvalidatedSegs,
		ReusedSegs:        report.ReusedSegs,
		InvalidationRate:  report.InvalidationRate(),
		ReuseRate:         report.ReuseRate(),
		FullRerunRuns:     report.FullRerunRuns,
		FullRerunRate:     report.FullRerunRate(),
		CostSavedMicros:   report.CostSavedMicros,
		ByBoundary:        report.ByBoundary,
		Verdict:           report.Verdict(),
		ScrapThresholdPct: recompile.ScrapThresholdPercent,
		MinRunsForVerdict: recompile.MinRunsForVerdict,
	})
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Session any    `json:"session,omitempty"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, r, status, errorResponse{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.ErrorContext(r.Context(), "write response", slog.String("error", err.Error()))
	}
}
