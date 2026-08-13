package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sequencestream/video-stream/internal/audio"
	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/render"
)

type renderRunRequest struct {
	Project      model.Project      `json:"project"`
	Resolution   string             `json:"resolution"`
	Finalized    bool               `json:"finalized"`
	IncludeBGM   bool               `json:"include_bgm"`
	BGM          render.BGMConfig   `json:"bgm,omitempty"`
	ResumeFrom   string             `json:"resume_from,omitempty"`
	RunID        string             `json:"run_id,omitempty"`
	Platform     string             `json:"platform,omitempty"`
	SubtitleMode audio.SubtitleMode `json:"subtitle_mode,omitempty"`
}

func (s *Server) handleRenderRun(w http.ResponseWriter, r *http.Request) {
	if s.deps.Render == nil {
		writeError(w, r, http.StatusServiceUnavailable, "render_unavailable", "render engine is not configured")
		return
	}

	var req renderRunRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error())
		return
	}
	if req.Project.ID == "" {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "field \"project.id\" is required")
		return
	}
	res := render.Resolution(req.Resolution)
	if res == "" {
		res = render.Resolution1080p
	}

	result, err := s.deps.Render.Run(r.Context(), render.RunRequest{
		RunID: req.RunID, Project: req.Project, Resolution: res,
		Finalized: req.Finalized, IncludeBGM: req.IncludeBGM || req.BGM.URI != "", BGM: req.BGM, ResumeFrom: req.ResumeFrom,
		Platform: req.Platform, SubtitleMode: req.SubtitleMode,
	})
	if errors.Is(err, render.ErrNotFinalized) || errors.Is(err, render.ErrPreviewRequired) {
		writeError(w, r, http.StatusUnprocessableEntity, "render_rejected", err.Error())
		return
	}
	if errors.Is(err, render.ErrNoStore) {
		writeError(w, r, http.StatusServiceUnavailable, "render_unavailable", err.Error())
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "render_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, result)
}

func (s *Server) handleRenderRunByID(w http.ResponseWriter, r *http.Request) {
	if s.deps.Render == nil {
		writeError(w, r, http.StatusServiceUnavailable, "render_unavailable", "render engine is not configured")
		return
	}
	run, arts, err := s.deps.Render.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, render.ErrRunNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "render run not found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"run": run, "seg_artifacts": arts})
}
