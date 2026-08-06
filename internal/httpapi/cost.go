package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sequencestream/video-stream/internal/costwarden"
)

type costPlanRequest struct {
	Project          json.RawMessage `json:"project"`
	BudgetMicros     int64           `json:"budget_micros"`
	ScriptCostMicros int64           `json:"script_cost_micros"`
}

func (s *Server) handleCostEstimate(w http.ResponseWriter, r *http.Request) {
	if s.deps.CostWarden == nil {
		writeError(w, r, http.StatusServiceUnavailable, "cost_unavailable", "costwarden is not configured")
		return
	}
	req, err := decodeCostRequest(w, r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	result, err := s.deps.CostWarden.Estimate(r.Context(), req)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "estimate_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, result)
}

func (s *Server) handleCostPlan(w http.ResponseWriter, r *http.Request) {
	if s.deps.CostWarden == nil {
		writeError(w, r, http.StatusServiceUnavailable, "cost_unavailable", "costwarden is not configured")
		return
	}
	req, err := decodeCostRequest(w, r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	result, err := s.deps.CostWarden.Plan(r.Context(), req)
	if errors.Is(err, costwarden.ErrBudgetExceeded) {
		writeError(w, r, http.StatusUnprocessableEntity, "budget_exceeded", err.Error())
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "plan_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, result)
}

func (s *Server) handleCostCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]any{
		"capabilities": costwarden.NewCatalog().Capabilities(),
		"ladder":       costwarden.LadderActions(),
	})
}

func decodeCostRequest(w http.ResponseWriter, r *http.Request) (costwarden.PlanRequest, error) {
	var body costPlanRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		return costwarden.PlanRequest{}, err
	}
	var req costwarden.PlanRequest
	if err := json.Unmarshal(body.Project, &req.Project); err != nil {
		return costwarden.PlanRequest{}, err
	}
	if req.Project.ID == "" {
		return costwarden.PlanRequest{}, errors.New("field \"project.id\" is required")
	}
	req.BudgetMicros = body.BudgetMicros
	req.ScriptCostMicros = body.ScriptCostMicros
	return req, nil
}
