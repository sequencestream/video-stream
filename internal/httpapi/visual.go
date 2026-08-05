package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/visual"
)

func (s *Server) handleVisualPacks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListVisualPacks(w, r)
	case http.MethodPost:
		s.handleCreateVisualPack(w, r)
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or POST")
	}
}

type listVisualPacksResponse struct {
	Items []visual.StylePack `json:"items"`
}

func (s *Server) handleListVisualPacks(w http.ResponseWriter, r *http.Request) {
	if s.deps.Visual == nil {
		writeJSON(w, r, http.StatusOK, listVisualPacksResponse{Items: []visual.StylePack{}})
		return
	}
	items, err := s.deps.Visual.List(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if items == nil {
		items = []visual.StylePack{}
	}
	writeJSON(w, r, http.StatusOK, listVisualPacksResponse{Items: items})
}

func (s *Server) handleCreateVisualPack(w http.ResponseWriter, r *http.Request) {
	if s.deps.Visual == nil {
		writeError(w, r, http.StatusServiceUnavailable, "visual_unavailable", "visual engine is not configured")
		return
	}
	var pack visual.StylePack
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&pack); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	created, err := s.deps.Visual.Create(r.Context(), pack)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusCreated, created)
}

func (s *Server) handleVisualPackByID(w http.ResponseWriter, r *http.Request) {
	if s.deps.Visual == nil {
		writeError(w, r, http.StatusServiceUnavailable, "visual_unavailable", "visual engine is not configured")
		return
	}
	id := r.PathValue("id")
	pack, err := s.deps.Visual.Get(r.Context(), id)
	if errors.Is(err, visual.ErrPackNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "style pack not found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, pack)
}

func (s *Server) handleVisualPackExport(w http.ResponseWriter, r *http.Request) {
	if s.deps.Visual == nil {
		writeError(w, r, http.StatusServiceUnavailable, "visual_unavailable", "visual engine is not configured")
		return
	}
	data, err := s.deps.Visual.Export(r.Context(), r.PathValue("id"))
	if errors.Is(err, visual.ErrPackNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "style pack not found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "export_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleVisualPackImport(w http.ResponseWriter, r *http.Request) {
	if s.deps.Visual == nil {
		writeError(w, r, http.StatusServiceUnavailable, "visual_unavailable", "visual engine is not configured")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	pack, err := s.deps.Visual.Import(r.Context(), data)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "import_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusCreated, pack)
}

type applyVisualPackRequest struct {
	Project model.Project `json:"project"`
}

func (s *Server) handleVisualPackApply(w http.ResponseWriter, r *http.Request) {
	if s.deps.Visual == nil {
		writeError(w, r, http.StatusServiceUnavailable, "visual_unavailable", "visual engine is not configured")
		return
	}
	pack, err := s.deps.Visual.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not_found", "style pack not found")
		return
	}
	var req applyVisualPackRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, s.deps.Visual.Apply(r.Context(), pack, req.Project))
}
