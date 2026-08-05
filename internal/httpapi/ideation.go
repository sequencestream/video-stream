package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/sequencestream/video-stream/internal/ideation"
	"github.com/sequencestream/video-stream/internal/store"
)

func (s *Server) handleIdeationExtract(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ideation == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ideation_unavailable", "ideation engine is not configured")
		return
	}

	var req ideation.ExtractInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error())
		return
	}
	if req.PostID == "" {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "field \"post_id\" is required")
		return
	}

	card, err := s.deps.Ideation.Extract(r.Context(), req)
	if errors.Is(err, ideation.ErrDomainFacts) || errors.Is(err, ideation.ErrIncompleteCard) {
		writeError(w, r, http.StatusUnprocessableEntity, "extract_rejected", err.Error())
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "extract_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusCreated, card)
}

type listStructureCardsResponse struct {
	Items []ideation.StructureCard `json:"items"`
}

func (s *Server) handleIdeationCards(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ideation == nil {
		writeJSON(w, r, http.StatusOK, listStructureCardsResponse{Items: []ideation.StructureCard{}})
		return
	}

	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_limit", "limit must be a non-negative integer")
			return
		}
		limit = n
	}

	cards, err := s.deps.Ideation.Cards(r.Context(), r.URL.Query().Get("category"), limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if cards == nil {
		cards = []ideation.StructureCard{}
	}
	writeJSON(w, r, http.StatusOK, listStructureCardsResponse{Items: cards})
}

type structureCardDetailResponse struct {
	Card      ideation.StructureCard   `json:"card"`
	Neighbors []ideation.StructureCard `json:"neighbors"`
}

func (s *Server) handleIdeationCardByID(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ideation == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ideation_unavailable", "ideation engine is not configured")
		return
	}

	id := r.PathValue("id")
	card, err := s.deps.Ideation.Card(r.Context(), id)
	if errors.Is(err, store.ErrStructureCardNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "structure card not found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}

	neighbors, err := s.deps.Ideation.GraphNeighbors(r.Context(), id, "")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "neighbors_failed", err.Error())
		return
	}
	if neighbors == nil {
		neighbors = []ideation.StructureCard{}
	}
	writeJSON(w, r, http.StatusOK, structureCardDetailResponse{Card: card, Neighbors: neighbors})
}

type migrateTopicsRequest struct {
	StructureCardID string `json:"structure_card_id"`
	UserTheme       string `json:"user_theme"`
	TargetCategory  string `json:"target_category"`
}

type listTopicCardsResponse struct {
	Items []ideation.TopicCard `json:"items"`
}

func (s *Server) handleIdeationMigrate(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ideation == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ideation_unavailable", "ideation engine is not configured")
		return
	}

	var req migrateTopicsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error())
		return
	}
	if req.StructureCardID == "" {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "field \"structure_card_id\" is required")
		return
	}

	card, err := s.deps.Ideation.Card(r.Context(), req.StructureCardID)
	if errors.Is(err, store.ErrStructureCardNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "structure card not found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}

	topics, err := s.deps.Ideation.MigrateTopics(r.Context(), ideation.MigrateRequest{
		Card:           card,
		UserTheme:      req.UserTheme,
		TargetCategory: req.TargetCategory,
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "migrate_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusCreated, listTopicCardsResponse{Items: topics})
}

func (s *Server) handleIdeationTopics(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ideation == nil {
		writeJSON(w, r, http.StatusOK, listTopicCardsResponse{Items: []ideation.TopicCard{}})
		return
	}

	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_limit", "limit must be a non-negative integer")
			return
		}
		limit = n
	}

	topics, err := s.deps.Ideation.Topics(r.Context(), r.URL.Query().Get("card_id"), limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if topics == nil {
		topics = []ideation.TopicCard{}
	}
	writeJSON(w, r, http.StatusOK, listTopicCardsResponse{Items: topics})
}

type recallRequest struct {
	Embedding []float64 `json:"embedding"`
	Limit     int       `json:"limit,omitempty"`
}

type recallResponse struct {
	Items []ideation.RecallMatch `json:"items"`
}

func (s *Server) handleIdeationRecall(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ideation == nil {
		writeJSON(w, r, http.StatusOK, recallResponse{Items: []ideation.RecallMatch{}})
		return
	}

	var req recallRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error())
		return
	}
	if len(req.Embedding) == 0 {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "field \"embedding\" is required")
		return
	}

	matches, err := s.deps.Ideation.Recall(r.Context(), req.Embedding, req.Limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "recall_failed", err.Error())
		return
	}
	if matches == nil {
		matches = []ideation.RecallMatch{}
	}
	writeJSON(w, r, http.StatusOK, recallResponse{Items: matches})
}
