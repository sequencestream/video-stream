package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sequencestream/video-stream/internal/audio"
)

type audioSynthesizeRequest struct {
	Project  json.RawMessage `json:"project"`
	Platform string          `json:"platform"`
	Mode     audio.SubtitleMode `json:"mode"`
	Voice    string          `json:"voice"`
}

func (s *Server) handleAudioSynthesize(w http.ResponseWriter, r *http.Request) {
	if s.deps.Audio == nil {
		writeError(w, r, http.StatusServiceUnavailable, "audio_unavailable", "audio engine is not configured")
		return
	}

	var req audioSynthesizeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error())
		return
	}

	var synth audio.SynthesizeRequest
	if err := json.Unmarshal(req.Project, &synth.Project); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_project", err.Error())
		return
	}
	if synth.Project.ID == "" {
		writeError(w, r, http.StatusBadRequest, "missing_fields", "field \"project.id\" is required")
		return
	}
	synth.Platform = req.Platform
	synth.Mode = req.Mode
	synth.Voice = req.Voice

	result, err := s.deps.Audio.Synthesize(r.Context(), synth)
	if errors.Is(err, audio.ErrNeedsWordCountChange) {
		writeError(w, r, http.StatusUnprocessableEntity, "word_count_change", "需改字数")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "synthesize_failed", err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, result)
}

func (s *Server) handleAudioPlatforms(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]any{"platforms": audio.DefaultPlatformSpecs()})
}
