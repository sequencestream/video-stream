package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sequencestream/video-stream/internal/hybrid"
	"github.com/sequencestream/video-stream/internal/model"
)

type hybridPlanRequest struct {
	Project model.Project `json:"project"`
}

type hybridPlanResponse struct {
	Plans    []hybrid.ShotPlan `json:"plans"`
	AIRoutes int               `json:"ai_routes"`
}

func (s *Server) handleHybridPlan(w http.ResponseWriter, r *http.Request) {
	if s.deps.Hybrid == nil {
		writeError(w, r, http.StatusServiceUnavailable, "hybrid_unavailable", "hybrid engine is not configured")
		return
	}

	var req hybridPlanRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error())
		return
	}
	if req.Project.ID == "" {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "field \"project.id\" is required")
		return
	}
	if len(req.Project.Segs) == 0 {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "project must contain at least one seg")
		return
	}

	plans, err := s.deps.Hybrid.PlanProject(r.Context(), req.Project)
	if errors.Is(err, hybrid.ErrNoStore) {
		writeError(w, r, http.StatusServiceUnavailable, "hybrid_unavailable", err.Error())
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "plan_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, hybridPlanResponse{
		Plans:    plans,
		AIRoutes: hybrid.CountAIRoutes(plans),
	})
}

type listHybridPlansResponse struct {
	Plans []hybrid.ShotPlan `json:"plans"`
}

func (s *Server) handleHybridPlans(w http.ResponseWriter, r *http.Request) {
	if s.deps.Hybrid == nil {
		writeJSON(w, r, http.StatusOK, listHybridPlansResponse{Plans: []hybrid.ShotPlan{}})
		return
	}

	projectID := r.PathValue("project_id")
	plans, err := s.deps.Hybrid.Plans(r.Context(), projectID)
	if errors.Is(err, hybrid.ErrNoStore) {
		writeError(w, r, http.StatusServiceUnavailable, "hybrid_unavailable", err.Error())
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if plans == nil {
		plans = []hybrid.ShotPlan{}
	}
	writeJSON(w, r, http.StatusOK, listHybridPlansResponse{Plans: plans})
}
