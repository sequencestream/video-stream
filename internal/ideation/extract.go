package ideation

import (
	"context"
	"fmt"
	"strings"
)

// ExtractInput is the metadata available when extracting a structure card.
//
// Title and description carry domain facts that must not appear in the output;
// the extractor reads them to infer form and then discards the specifics.
type ExtractInput struct {
	PostID      string `json:"post_id"`
	Category    string `json:"category,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// ForbiddenTerms are source-specific words the card must not contain. Tests
	// populate this from the input title; production extractors derive terms
	// from the title automatically.
	ForbiddenTerms  []string `json:"forbidden_terms,omitempty"`
	DurationSeconds int64    `json:"duration_seconds,omitempty"`
}

// Extractor turns a viral work's metadata into a domain-neutral structure card.
type Extractor interface {
	Extract(ctx context.Context, in ExtractInput) (StructureCard, error)
}

// RuleExtractor is a deterministic extractor for tests and offline fixtures.
//
// It maps observable signals — title shape, duration — to abstract structure
// labels without calling a model. The mapping is crude but reproducible,
// which is what unit tests need.
type RuleExtractor struct{}

// Extract implements Extractor.
func (RuleExtractor) Extract(_ context.Context, in ExtractInput) (StructureCard, error) {
	hook := "statement-hook"
	if strings.Contains(in.Title, "?") || strings.Contains(in.Title, "？") {
		hook = "question-hook"
	} else if strings.ContainsAny(in.Title, "0123456789") {
		hook = "number-hook"
	}

	opening := "face-close-up"
	if in.DurationSeconds > 0 && in.DurationSeconds < 30 {
		opening = "fast-cut-montage"
	}

	beats := "setup→build→payoff"
	if strings.Contains(strings.ToLower(in.Description), "twist") {
		beats = "setup→twist→payoff"
	}

	density := "sparse→dense→sparse"
	if in.DurationSeconds > 120 {
		density = "dense→sparse→dense"
	}

	emotion := "curiosity→tension→relief"
	controversy := "expectation-violation"

	card := StructureCard{
		SourcePostID:      in.PostID,
		SourceCategory:    in.Category,
		HookType:          hook,
		OpeningVisual:     opening,
		BeatSequence:      beats,
		DensityCurve:      density,
		EmotionArc:        emotion,
		ControversyAnchor: controversy,
		Embedding:         EmbedFromCard(hook, opening, beats, density, emotion, controversy),
	}
	if err := card.Validate(); err != nil {
		return StructureCard{}, err
	}
	terms := in.ForbiddenTerms
	if len(terms) == 0 {
		terms = deriveForbiddenTerms(in.Title, in.Description)
	}
	if card.ContainsForbiddenTerms(terms...) {
		return StructureCard{}, fmt.Errorf("%w: card still references source content", ErrDomainFacts)
	}
	return card, nil
}

// deriveForbiddenTerms pulls likely domain nouns from the source text so the
// extractor can assert they were stripped. Words longer than three runes that
// are not common structure vocabulary become forbidden.
func deriveForbiddenTerms(title, description string) []string {
	skip := map[string]struct{}{
		"the": {}, "and": {}, "how": {}, "why": {}, "what": {},
		"this": {}, "that": {}, "with": {}, "from": {}, "your": {},
	}
	seen := map[string]struct{}{}
	var terms []string
	for _, word := range strings.Fields(strings.ToLower(title + " " + description)) {
		word = strings.Trim(word, ".,!?;:\"'()[]")
		if len(word) <= 3 {
			continue
		}
		if _, ok := skip[word]; ok {
			continue
		}
		if _, dup := seen[word]; dup {
			continue
		}
		seen[word] = struct{}{}
		terms = append(terms, word)
	}
	return terms
}
