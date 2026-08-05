package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/sequencestream/video-stream/internal/radar"
	"github.com/sequencestream/video-stream/internal/store"
)

func (s *Server) handleRadarAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListRadarAccounts(w, r)
	case http.MethodPost:
		s.handleImportRadarAccount(w, r)
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or POST")
	}
}

// listRadarAccountsResponse wraps the watch list so pagination can be added later.
type listRadarAccountsResponse struct {
	Items []store.RadarAccount `json:"items"`
}

func (s *Server) handleListRadarAccounts(w http.ResponseWriter, r *http.Request) {
	if s.deps.Radar == nil {
		writeJSON(w, r, http.StatusOK, listRadarAccountsResponse{Items: []store.RadarAccount{}})
		return
	}

	accounts, err := s.deps.Radar.Accounts(r.Context(), r.URL.Query().Get("platform"))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if accounts == nil {
		accounts = []store.RadarAccount{}
	}
	writeJSON(w, r, http.StatusOK, listRadarAccountsResponse{Items: accounts})
}

// importRadarAccountRequest is the body of POST /v1/radar/accounts.
type importRadarAccountRequest struct {
	Platform    string `json:"platform"`
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name,omitempty"`
	Category    string `json:"category,omitempty"`
	Followers   int64  `json:"followers,omitempty"`
	Owned       bool   `json:"owned,omitempty"`
}

func (s *Server) handleImportRadarAccount(w http.ResponseWriter, r *http.Request) {
	if s.deps.Radar == nil {
		writeError(w, r, http.StatusServiceUnavailable, "radar_unavailable", "radar engine is not configured")
		return
	}

	var req importRadarAccountRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error())
		return
	}
	if req.Platform == "" || req.Handle == "" {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "fields \"platform\" and \"handle\" are required")
		return
	}

	account, err := s.deps.Radar.ImportAccount(r.Context(), radar.Account{
		Platform:    req.Platform,
		Handle:      req.Handle,
		DisplayName: req.DisplayName,
		Category:    req.Category,
		Followers:   req.Followers,
		Owned:       req.Owned,
	})
	if errors.Is(err, radar.ErrTooManyAccounts) {
		writeError(w, r, http.StatusConflict, "watch_list_full", err.Error())
		return
	}
	if errors.Is(err, store.ErrAccountExists) {
		writeError(w, r, http.StatusConflict, "account_exists", "this account is already on the watch list")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "import_failed", err.Error())
		return
	}

	writeJSON(w, r, http.StatusCreated, account)
}

// radarSignalsResponse ships scored posts with every derived measure attached.
type radarSignalsResponse struct {
	Items []radar.Signal `json:"items"`
}

func (s *Server) handleRadarSignals(w http.ResponseWriter, r *http.Request) {
	if s.deps.Radar == nil {
		writeJSON(w, r, http.StatusOK, radarSignalsResponse{Items: []radar.Signal{}})
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

	signals, err := s.deps.Radar.Signals(r.Context(), radar.Query{
		Platform: r.URL.Query().Get("platform"),
		Category: r.URL.Query().Get("category"),
		HotOnly:  r.URL.Query().Get("hot") == "1" || r.URL.Query().Get("hot") == "true",
		Limit:    limit,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "signals_failed", err.Error())
		return
	}
	if signals == nil {
		signals = []radar.Signal{}
	}
	writeJSON(w, r, http.StatusOK, radarSignalsResponse{Items: signals})
}

// ingestRadarRequest is the body of POST /v1/radar/ingest.
type ingestRadarRequest struct {
	Readings []radar.Reading `json:"readings"`
}

func (s *Server) handleRadarIngest(w http.ResponseWriter, r *http.Request) {
	if s.deps.Radar == nil {
		writeError(w, r, http.StatusServiceUnavailable, "radar_unavailable", "radar engine is not configured")
		return
	}

	var req ingestRadarRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error())
		return
	}

	n, err := s.deps.Radar.Ingest(r.Context(), req.Readings)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "ingest_failed", err.Error())
		return
	}

	writeJSON(w, r, http.StatusOK, map[string]int{"ingested": n})
}

func (s *Server) handleRadarPoll(w http.ResponseWriter, r *http.Request) {
	if s.deps.Radar == nil {
		writeError(w, r, http.StatusServiceUnavailable, "radar_unavailable", "radar engine is not configured")
		return
	}

	result, err := s.deps.Radar.PollOnce(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "poll_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, result)
}
