package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/sequencestream/video-stream/internal/model"
)

// SharedVisual holds prompt/seed/ref for one render_cache_key. Both 720p and
// 1080p reads use the same values; only the downstream video API tier changes.
type SharedVisual struct {
	RenderCacheKey string `json:"render_cache_key"`
	Prompt         string `json:"prompt"`
	Seed           string `json:"seed"`
	RefURI         string `json:"ref_uri,omitempty"`
}

// BuildSharedContext derives one SharedVisual per seg from the project. The
// seed is deterministic from render_cache_key so preview and delivery match.
func BuildSharedContext(project model.Project) []SharedVisual {
	out := make([]SharedVisual, 0, len(project.Segs))
	seen := make(map[string]struct{}, len(project.Segs))
	for _, s := range project.Segs {
		key := s.RenderCacheKey
		if key == "" {
			key = model.ComputeRenderCacheKey(s, project.RenderProfile)
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		prompt := s.VisualPromptSlot
		if prompt == "" {
			prompt = s.Text
		}
		out = append(out, SharedVisual{
			RenderCacheKey: key,
			Prompt:         prompt,
			Seed:           seedFromKey(key),
			RefURI:         fmt.Sprintf("ref://%s/%s", project.ID, s.SegID),
		})
	}
	return out
}

func seedFromKey(renderCacheKey string) string {
	sum := sha256.Sum256([]byte(renderCacheKey))
	return hex.EncodeToString(sum[:8])
}

// SameSharedContext reports whether two context slices carry identical
// prompt/seed/ref for every render_cache_key.
func SameSharedContext(a, b []SharedVisual) bool {
	if len(a) != len(b) {
		return false
	}
	index := make(map[string]SharedVisual, len(a))
	for _, v := range a {
		index[v.RenderCacheKey] = v
	}
	for _, v := range b {
		other, ok := index[v.RenderCacheKey]
		if !ok {
			return false
		}
		if other.Prompt != v.Prompt || other.Seed != v.Seed || other.RefURI != v.RefURI {
			return false
		}
	}
	return true
}
