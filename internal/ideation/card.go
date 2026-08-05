// Package ideation turns radar hits into domain-neutral structure cards and
// cross-category topic ideas.
//
// The product bet is that the moat lives in topic + script, not in rendering.
// Structure cards capture how a video works — hook, pacing, emotional arc —
// without copying what it is about. Migrating structure across categories is
// the core loop; migrating facts would be plagiarism and produce wrong content.
package ideation

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrIncompleteCard means one or more of the six structure dimensions is empty.
	ErrIncompleteCard = errors.New("structure card is missing required dimensions")
	// ErrDomainFacts means the card still carries source-specific factual content.
	ErrDomainFacts = errors.New("structure card contains domain-specific facts")
)

// StructureCard is a domain-neutral decomposition of one viral work.
//
// Every field describes form, not content. "question-hook + face-close-up +
// setup→twist→payoff" is portable; "how to make sourdough" is not.
type StructureCard struct {
	ID             string    `json:"id"`
	SourcePostID   string    `json:"source_post_id"`
	SourceCategory string    `json:"source_category,omitempty"`
	HookType       string    `json:"hook_type"`
	OpeningVisual  string    `json:"opening_visual"`
	BeatSequence   string    `json:"beat_sequence"`
	DensityCurve   string    `json:"density_curve"`
	EmotionArc     string    `json:"emotion_arc"`
	ControversyAnchor string `json:"controversy_anchor"`
	// Embedding is a serialised float vector used only for recall, not ranking
	// topics. Vectors find candidates; structure migration picks among them.
	Embedding []float64 `json:"embedding,omitempty"`
}

// Validate checks that all six dimensions are present.
func (c StructureCard) Validate() error {
	missing := make([]string, 0, 6)
	if strings.TrimSpace(c.HookType) == "" {
		missing = append(missing, "hook_type")
	}
	if strings.TrimSpace(c.OpeningVisual) == "" {
		missing = append(missing, "opening_visual")
	}
	if strings.TrimSpace(c.BeatSequence) == "" {
		missing = append(missing, "beat_sequence")
	}
	if strings.TrimSpace(c.DensityCurve) == "" {
		missing = append(missing, "density_curve")
	}
	if strings.TrimSpace(c.EmotionArc) == "" {
		missing = append(missing, "emotion_arc")
	}
	if strings.TrimSpace(c.ControversyAnchor) == "" {
		missing = append(missing, "controversy_anchor")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: missing %s", ErrIncompleteCard, strings.Join(missing, ", "))
	}
	return nil
}

// ContainsForbiddenTerms reports whether any forbidden domain term appears in the
// card's text fields. Extractors must strip source-specific nouns; this is the
// assertion hook for tests.
func (c StructureCard) ContainsForbiddenTerms(terms ...string) bool {
	text := strings.ToLower(strings.Join([]string{
		c.HookType, c.OpeningVisual, c.BeatSequence,
		c.DensityCurve, c.EmotionArc, c.ControversyAnchor,
	}, " "))
	for _, term := range terms {
		t := strings.ToLower(strings.TrimSpace(term))
		if t != "" && strings.Contains(text, t) {
			return true
		}
	}
	return false
}

// TopicCard is one cross-category idea derived from a structure card.
type TopicCard struct {
	ID              string `json:"id"`
	StructureCardID string `json:"structure_card_id"`
	Title           string `json:"title"`
	Angle           string `json:"angle"`
	MigrationSource string `json:"migration_source"`
	WhyFits         string `json:"why_fits"`
	TargetCategory  string `json:"target_category,omitempty"`
	UserTheme       string `json:"user_theme,omitempty"`
}
