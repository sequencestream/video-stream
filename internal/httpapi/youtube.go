package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/sequencestream/video-stream/internal/youtube"
)

type youtubePublishRequest struct {
	ProjectID   string   `json:"project_id"`
	SessionID   string   `json:"session_id,omitempty"`
	VideoPath   string   `json:"video_path,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Visibility  string   `json:"visibility,omitempty"`
	Notify      bool     `json:"notify"`
}

func (s *Server) handleYouTubePublish(w http.ResponseWriter, r *http.Request) {
	if s.deps.YouTube == nil {
		writeError(w, r, http.StatusServiceUnavailable, "youtube_unavailable", "youtube engine is not configured")
		return
	}
	var req youtubePublishRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.ProjectID == "" || req.Title == "" {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "project_id and title are required")
		return
	}
	result, err := s.deps.YouTube.Publish(r.Context(), youtube.PublishRequest{
		ProjectID: req.ProjectID, SessionID: req.SessionID, VideoPath: req.VideoPath,
		Title: req.Title, Description: req.Description, Tags: req.Tags,
		Visibility: req.Visibility, Notify: req.Notify,
	})
	if errors.Is(err, youtube.ErrNoCredential) {
		writeError(w, r, http.StatusUnprocessableEntity, "no_credential", err.Error())
		return
	}
	if errors.Is(err, youtube.ErrQuotaExceeded) {
		writeError(w, r, http.StatusTooManyRequests, "quota_exceeded", err.Error())
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "publish_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, result)
}

func (s *Server) handleYouTubeUploadByID(w http.ResponseWriter, r *http.Request) {
	if s.deps.YouTube == nil {
		writeError(w, r, http.StatusServiceUnavailable, "youtube_unavailable", "youtube engine is not configured")
		return
	}
	rec, err := s.deps.YouTube.GetUpload(r.Context(), r.PathValue("id"))
	if errors.Is(err, youtube.ErrUploadNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "upload not found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, rec)
}

func (s *Server) handleDeliveryDownload(w http.ResponseWriter, r *http.Request) {
	if s.deps.YouTube == nil {
		writeError(w, r, http.StatusServiceUnavailable, "youtube_unavailable", "delivery is not configured")
		return
	}
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "project_id query param is required")
		return
	}
	path, err := s.deps.YouTube.DownloadPath(projectID)
	if errors.Is(err, youtube.ErrVideoMissing) {
		writeError(w, r, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "download_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(path))
	http.ServeFile(w, r, path)
}
