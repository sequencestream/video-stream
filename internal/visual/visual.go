// Package visual implements the L2 visual style pack and identity stack.
//
// Consistency across shots comes from a reusable style pack, not from hoping
// each generation call remembers the last frame. StyleAnchor on RenderProfile
// ties packs to render_cache_key; changing packs is a style_anchor boundary
// and forces a full rerun.
package visual

const (
	// SchemaVersion is the style pack document version.
	SchemaVersion = 1
	// CoherenceShotCount is how many consecutive shots coherence tests cover.
	CoherenceShotCount = 5
	// MaxPaletteDistance is the L1 palette distance threshold for coherence.
	MaxPaletteDistance = 0.15
	// MinCompositionSimilarity is the cosine threshold for composition grammar.
	MinCompositionSimilarity = 0.85
)

// FullRerunWarning is returned when applying a different pack to a project.
const FullRerunWarning = "Switching the visual style pack re-renders every shot. Cross-vendor lighting will match global mood, not pixel-perfect continuity."
