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
		if writeWizardRequestError(w, r, err) {
			return
		}
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
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_body", err.Error())
			return
		}
	}
	sess, err := s.deps.Wizard.Advance(r.Context(), r.PathValue("id"), req)
	if errors.Is(err, wizard.ErrSessionNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "wizard session not found")
		return
	}
	if writeWizardRequestError(w, r, err) {
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

func writeWizardRequestError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var reqErr *wizard.RequestError
	if errors.As(err, &reqErr) {
		status := http.StatusBadRequest
		switch reqErr.Code {
		case "idempotency_conflict", "operation_in_progress", "stale_session", "session_completed":
			status = http.StatusConflict
		}
		writeJSON(w, r, status, errorResponse{Code: reqErr.Code, Message: reqErr.Message, Session: reqErr.Session})
		return true
	}
	if errors.Is(err, wizard.ErrOperationRequired) {
		writeError(w, r, http.StatusBadRequest, "operation_id_required", err.Error())
		return true
	}
	if errors.Is(err, wizard.ErrVersionRequired) {
		writeError(w, r, http.StatusBadRequest, "expected_version_required", err.Error())
		return true
	}
	return false
}
