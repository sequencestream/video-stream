// Package render implements the FFmpeg-only render pipeline with staged execution
// and a shared visual context between 720p preview and 1080p delivery.
package render

import "errors"

// Resolution is the output tier. Both tiers share prompt/seed/ref; only the
// video model resolution differs on the 1080p pass.
type Resolution string

const (
	Resolution720p  Resolution = "720p"
	Resolution1080p Resolution = "1080p"
)

// Stage names follow the MVP pipeline order.
const (
	StageVisuals   = "visuals"
	StageAudio     = "audio"
	StageSubtitles = "subtitles"
	StageLoudness  = "loudness"
	StageMux       = "mux"
	StageBGMBeat   = "bgm_beat"
)

// StageOrder is the default pipeline. BGM beat is appended only when finalized.
var StageOrder = []string{
	StageVisuals, StageAudio, StageSubtitles, StageLoudness, StageMux,
}

var (
	// ErrNotFinalized is returned when BGM beat sync runs before script lock.
	ErrNotFinalized = errors.New("project must be finalized before BGM beat sync")
	// ErrUnknownStage is returned for an invalid resume stage name.
	ErrUnknownStage = errors.New("unknown render stage")
	// ErrNoStore is returned when persistence is not configured.
	ErrNoStore = errors.New("render has no store configured")
	// ErrPreviewRequired is returned when 1080p runs before a 720p preview stored shared context.
	ErrPreviewRequired = errors.New("run a 720p preview before 1080p delivery")
	// ErrLabelRejected means compliance label readback failed after mux.
	ErrLabelRejected = errors.New("compliance label injection or readback failed")
	// ErrOutputRejected means the muxed file failed delivery validation.
	ErrOutputRejected = errors.New("render output failed validation")
	// ErrRunNotFound is returned when a render run id is unknown.
	ErrRunNotFound = errors.New("render run not found")
	// ErrReusableArtifactUnavailable means an accepted recompile plan named a
	// seg as reusable, but the cached bytes can no longer be read. The executor
	// does not silently regenerate it because that would make the executed work
	// disagree with the recorded plan.
	ErrReusableArtifactUnavailable = errors.New("reusable render artifact is unavailable")
)

// Dimensions returns width and height for a resolution tier.
func (r Resolution) Dimensions() (width, height int) {
	switch r {
	case Resolution1080p:
		return 1920, 1080
	default:
		return 1280, 720
	}
}

// Validate checks the resolution string.
func (r Resolution) Validate() error {
	switch r {
	case Resolution720p, Resolution1080p:
		return nil
	default:
		return errors.New("resolution must be 720p or 1080p")
	}
}
