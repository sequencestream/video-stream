package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sequencestream/video-stream/internal/wizard"
)

func (s *Server) handleWizardCreate(w http.ResponseWriter, r *http.Request) {
	if s.deps.Wizard == nil {
		writeError(w, r, http.StatusServiceUnavailable, "wizard_unavailable", "wizard engine is not configured")
		return
	}
	var req wizard.CreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	sess, err := s.deps.Wizard.Create(r.Context(), req)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusCreated, sess)
}

func (s *Server) handleWizardGet(w http.ResponseWriter, r *http.Request) {
	if s.deps.Wizard == nil {
		writeError(w, r, http.StatusServiceUnavailable, "wizard_unavailable", "wizard engine is not configured")
		return
	}
	sess, err := s.deps.Wizard.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, wizard.ErrSessionNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "wizard session not found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, sess)
}

func (s *Server) handleWizardAdvance(w http.ResponseWriter, r *http.Request) {
	if s.deps.Wizard == nil {
		writeError(w, r, http.StatusServiceUnavailable, "wizard_unavailable", "wizard engine is not configured")
		return
	}
	var req wizard.AdvanceRequest
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	}
	sess, err := s.deps.Wizard.Advance(r.Context(), r.PathValue("id"), req)
	if errors.Is(err, wizard.ErrSessionNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "wizard session not found")
		return
	}
	if errors.Is(err, wizard.ErrBudgetExceeded) || errors.Is(err, wizard.ErrSessionFailed) {
		writeError(w, r, http.StatusUnprocessableEntity, "advance_rejected", err.Error())
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "advance_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, sess)
}
