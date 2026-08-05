// Package sidecar is the single Go-side entry point to the Python sidecar.
//
// The sidecar isolates the Python ecosystem (audio/ASR, editor drafts, browser
// automation) behind loopback HTTP. In the MVP every capability endpoint is a
// declared-but-unimplemented placeholder: it answers 501 with a structured
// reason, never fabricated data, so callers can tell "not built yet" apart from
// "broken".
package sidecar

// Capability names the three placeholder surfaces reserved for later intents.
type Capability string

// The reserved capabilities.
const (
	CapabilityAudio    Capability = "audio"
	CapabilityJianYing Capability = "jianying"
	CapabilityBrowser  Capability = "browser"
)

// Health is the sidecar's self-report.
type Health struct {
	Status       string   `json:"status"`
	Service      string   `json:"service"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

// TranscribeRequest asks the sidecar to transcribe an audio file into
// word-level timestamps.
type TranscribeRequest struct {
	AudioPath string `json:"audio_path"`
	Language  string `json:"language,omitempty"`
}

// TranscribeResponse carries word-level timing once implemented.
type TranscribeResponse struct {
	Text  string `json:"text"`
	Words []Word `json:"words"`
}

// Word is one recognised token with its timing in seconds.
type Word struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// DraftRequest asks the sidecar to emit an editor draft project.
type DraftRequest struct {
	ProjectID string `json:"project_id"`
	OutputDir string `json:"output_dir"`
}

// DraftResponse points at the generated draft once implemented.
type DraftResponse struct {
	DraftPath string `json:"draft_path"`
}

// AutomateRequest asks the sidecar to drive a browser session.
type AutomateRequest struct {
	Target  string         `json:"target"`
	Action  string         `json:"action"`
	Payload map[string]any `json:"payload,omitempty"`
}

// AutomateResponse carries the automation outcome once implemented.
type AutomateResponse struct {
	Succeeded bool           `json:"succeeded"`
	Data      map[string]any `json:"data,omitempty"`
}
