package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sequencestream/video-stream/internal/scriptagents"
)

func (s *Server) handleScriptPolish(w http.ResponseWriter, r *http.Request) {
	if s.deps.ScriptAgents == nil {
		writeError(w, r, http.StatusServiceUnavailable, "script_unavailable", "script agents engine is not configured")
		return
	}

	var req scriptagents.PolishRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error())
		return
	}
	if req.Topic == "" {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "field \"topic\" is required")
		return
	}

	result, err := s.deps.ScriptAgents.Polish(r.Context(), req)
	if errors.Is(err, scriptagents.ErrCriticRewrote) || errors.Is(err, scriptagents.ErrAudienceJudged) {
		writeError(w, r, http.StatusUnprocessableEntity, "constraint_violation", err.Error())
		return
	}
	if errors.Is(err, scriptagents.ErrSpikeLost) {
		writeError(w, r, http.StatusUnprocessableEntity, "spike_lost", err.Error())
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "polish_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusCreated, result)
}

func (s *Server) handleScriptPolishRun(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotImplemented, "not_implemented", "polish run lookup is not implemented yet")
}
