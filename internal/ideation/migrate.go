package ideation

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	// MinTopics is the lower bound on migrated topic cards per request.
	MinTopics = 3
	// MaxTopics is the upper bound. The product promises 3–5 ideas, not a list.
	MaxTopics = 5
)

var (
	// ErrTooFewTopics means the migrator returned fewer than MinTopics cards.
	ErrTooFewTopics = errors.New("migrator returned too few topic cards")
	// ErrTooManyTopics means the migrator exceeded MaxTopics.
	ErrTooManyTopics = errors.New("migrator returned too many topic cards")
)

// MigrateRequest asks for cross-category topics from one structure card.
type MigrateRequest struct {
	Card           StructureCard
	UserTheme      string
	TargetCategory string
}

// Migrator produces topic cards from a structure card.
type Migrator interface {
	Migrate(ctx context.Context, req MigrateRequest) ([]TopicCard, error)
}

// RuleMigrator is a deterministic migrator for tests.
type RuleMigrator struct{}

// Migrate implements Migrator.
func (RuleMigrator) Migrate(_ context.Context, req MigrateRequest) ([]TopicCard, error) {
	theme := strings.TrimSpace(req.UserTheme)
	if theme == "" {
		theme = "general audience"
	}
	category := strings.TrimSpace(req.TargetCategory)
	if category == "" {
		category = "cross-category"
	}

	angles := []struct {
		title string
		angle string
		why   string
	}{
		{
			title: fmt.Sprintf("Apply %s opening to %s", req.Card.HookType, category),
			angle: fmt.Sprintf("Reuse %s hook with %s beat pattern", req.Card.HookType, req.Card.BeatSequence),
			why:   fmt.Sprintf("Your theme %q fits the %s emotional arc without copying source facts", theme, req.Card.EmotionArc),
		},
		{
			title: fmt.Sprintf("%s pacing for %s creators", req.Card.DensityCurve, category),
			angle: fmt.Sprintf("Mirror %s density curve in %s niche", req.Card.DensityCurve, category),
			why:   fmt.Sprintf("The %s structure transfers because information rhythm is category-agnostic", req.Card.BeatSequence),
		},
		{
			title: fmt.Sprintf("Controversy-led %s angle", category),
			angle: fmt.Sprintf("Use %s controversy anchor on %s topic", req.Card.ControversyAnchor, theme),
			why:   fmt.Sprintf("Controversy type %s maps to your audience without reusing original claims", req.Card.ControversyAnchor),
		},
		{
			title: fmt.Sprintf("Visual %s hook variant", req.Card.OpeningVisual),
			angle: fmt.Sprintf("Open with %s then pivot to %s", req.Card.OpeningVisual, theme),
			why:   fmt.Sprintf("Opening pattern %s is proven; your %s theme supplies new substance", req.Card.OpeningVisual, theme),
		},
		{
			title: fmt.Sprintf("Emotional %s journey", req.Card.EmotionArc),
			angle: fmt.Sprintf("Follow %s arc for %s content", req.Card.EmotionArc, category),
			why:   fmt.Sprintf("Structure from card %s migrates; facts stay in your domain", req.Card.ID),
		},
	}

	topics := make([]TopicCard, 0, MaxTopics)
	for i, a := range angles {
		if i >= MaxTopics {
			break
		}
		topics = append(topics, TopicCard{
			StructureCardID: req.Card.ID,
			Title:           a.title,
			Angle:           a.angle,
			MigrationSource: req.Card.ID,
			WhyFits:         a.why,
			TargetCategory:  category,
			UserTheme:       theme,
		})
	}
	return validateTopicCount(topics)
}

func validateTopicCount(topics []TopicCard) ([]TopicCard, error) {
	if len(topics) < MinTopics {
		return nil, fmt.Errorf("%w: got %d", ErrTooFewTopics, len(topics))
	}
	if len(topics) > MaxTopics {
		return nil, fmt.Errorf("%w: got %d", ErrTooManyTopics, len(topics))
	}
	return topics, nil
}
