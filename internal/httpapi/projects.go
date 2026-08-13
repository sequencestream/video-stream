package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/sequencestream/video-stream/internal/intake"
	"github.com/sequencestream/video-stream/internal/media"
	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/render"
	"github.com/sequencestream/video-stream/internal/store"
)

// defaultProjectLimit bounds an unfiltered listing. Agents call this to find a
// project id, not to page through history.
const defaultProjectLimit = 50

type createProjectRequest struct {
	ProjectID string `json:"project_id,omitempty"`
	Title     string `json:"title"`
	Script    string `json:"script"`
	Voice     string `json:"voice,omitempty"`
	MaxRunes  int    `json:"max_runes,omitempty"`
	MinRunes  int    `json:"min_runes,omitempty"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if s.deps.Intake == nil {
		writeError(w, r, http.StatusServiceUnavailable, "intake_unavailable", "intake engine is not configured")
		return
	}

	var req createProjectRequest
	// Scripts are prose, so the cap is larger than the 1MiB used elsewhere.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error())
		return
	}
	if req.Title == "" {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "field \"title\" is required")
		return
	}
	if req.Script == "" {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "field \"script\" is required")
		return
	}

	result, err := s.deps.Intake.Import(r.Context(), intake.Request{
		ProjectID: req.ProjectID, Title: req.Title, Script: req.Script,
		Voice: req.Voice, MaxRunes: req.MaxRunes, MinRunes: req.MinRunes,
	})
	if errors.Is(err, intake.ErrEmptyScript) {
		writeError(w, r, http.StatusBadRequest, "empty_script", err.Error())
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "import_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusCreated, result)
}

type listProjectsResponse struct {
	Projects []store.ProjectSummary `json:"projects"`
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	if s.deps.Projects == nil {
		writeJSON(w, r, http.StatusOK, listProjectsResponse{Projects: []store.ProjectSummary{}})
		return
	}

	limit := defaultProjectLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_query", "limit must be a positive integer")
			return
		}
		limit = parsed
	}

	projects, err := s.deps.Projects.ListProjects(r.Context(), limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if projects == nil {
		projects = []store.ProjectSummary{}
	}
	writeJSON(w, r, http.StatusOK, listProjectsResponse{Projects: projects})
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	project, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	writeJSON(w, r, http.StatusOK, project)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	if s.deps.Projects == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "project store is not configured")
		return
	}
	if err := s.deps.Projects.DeleteProject(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, r, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type projectBackgroundRequest struct {
	// Image is a path on the machine running vsd, not an upload. The CLI and
	// the service share a filesystem in every supported deployment.
	Image      string `json:"image"`
	Anchor     string `json:"anchor,omitempty"`
	Resolution string `json:"resolution,omitempty"`
}

func (s *Server) handleProjectBackground(w http.ResponseWriter, r *http.Request) {
	if s.deps.Media == nil {
		writeError(w, r, http.StatusServiceUnavailable, "media_unavailable", "media preparer is not configured")
		return
	}

	var req projectBackgroundRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error())
		return
	}
	if req.Image == "" {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "field \"image\" is required")
		return
	}

	project, ok := s.loadProject(w, r)
	if !ok {
		return
	}

	resolution := render.Resolution(req.Resolution)
	if resolution == "" {
		resolution = render.Resolution1080p
	}
	if err := resolution.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_resolution", err.Error())
		return
	}
	width, height := resolution.Dimensions()

	segIDs := make([]string, 0, len(project.Segs))
	for _, seg := range project.Segs {
		segIDs = append(segIDs, seg.SegID)
	}

	result, err := s.deps.Media.PlaceBackground(r.Context(), media.Request{
		ProjectID: project.ID, SegIDs: segIDs, Source: req.Image,
		Width: width, Height: height, Anchor: media.Anchor(req.Anchor),
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "background_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, result)
}

// loadProject resolves the {id} path value, writing the error response itself.
func (s *Server) loadProject(w http.ResponseWriter, r *http.Request) (model.Project, bool) {
	if s.deps.Projects == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "project store is not configured")
		return model.Project{}, false
	}
	project, err := s.deps.Projects.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not_found", err.Error())
		return model.Project{}, false
	}
	return project, true
}
