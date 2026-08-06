// Package audio synthesizes TTS tracks, aligns word-level timestamps, normalizes
// loudness, and renders soft or burned-in subtitles for the render pipeline.
package audio

import "errors"

const (
	// MaxStretchPercent matches model.MaxTolerancePercent for time-stretch.
	MaxStretchPercent = 8
	// DefaultLUFS is the MVP normalization target (YouTube-ish).
	DefaultLUFS = -14.0
)

var (
	// ErrNeedsWordCountChange means TTS duration cannot fit the budget even with stretch.
	ErrNeedsWordCountChange = errors.New("tts duration outside budget; revise word count")
	// ErrNoStore is returned when persistence is not configured.
	ErrNoStore = errors.New("audio has no store configured")
)

// SubtitleMode selects soft captions vs burned-in fallback.
type SubtitleMode string

const (
	SubtitleSoft   SubtitleMode = "soft"
	SubtitleBurnIn SubtitleMode = "burn_in"
)
