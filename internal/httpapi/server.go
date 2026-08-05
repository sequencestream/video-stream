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

	"github.com/sequencestream/video-stream/internal/config"
	"github.com/sequencestream/video-stream/internal/queue"
	"github.com/sequencestream/video-stream/internal/sidecar"
	"github.com/sequencestream/video-stream/internal/store"
)

// Deps are the collaborators the HTTP handlers need.
type Deps struct {
	Config  config.Config
	Store   store.TaskStore
	Queue   queue.Queue
	Sidecar *sidecar.Client
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

// providerStatus never carries the key itself, only whether it resolved.
type providerStatus struct {
	Name          string `json:"name"`
	Model         string `json:"model,omitempty"`
	BaseURL       string `json:"base_url,omitempty"`
	APIKeyEnv     string `json:"api_key_env,omitempty"`
	HasCredential bool   `json:"has_credential"`
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	providers := make([]providerStatus, 0, len(s.deps.Config.Providers))
	for _, p := range s.deps.Config.Providers {
		providers = append(providers, providerStatus{
			Name:          p.Name,
			Model:         p.Model,
			BaseURL:       p.BaseURL,
			APIKeyEnv:     p.APIKeyEnv,
			HasCredential: p.HasCredential(),
		})
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

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
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
