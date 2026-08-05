package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sequencestream/video-stream/internal/compliance"
)

func (s *Server) handleComplianceCheck(w http.ResponseWriter, r *http.Request) {
	if s.deps.Compliance == nil {
		writeError(w, r, http.StatusServiceUnavailable, "compliance_unavailable", "compliance engine is not configured")
		return
	}

	var req compliance.CheckRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error())
		return
	}

	result, err := s.deps.Compliance.Check(r.Context(), req)
	if errors.Is(err, compliance.ErrGateBlocked) {
		writeJSON(w, r, http.StatusUnprocessableEntity, result)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "check_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, result)
}
