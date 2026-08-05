package ideation_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/ideation"
)

func TestStructureCardValidateRequiresAllSixDimensions(t *testing.T) {
	card := ideation.StructureCard{HookType: "question-hook"}
	err := card.Validate()
	if err == nil {
		t.Fatal("expected incomplete card error")
	}
	if !strings.Contains(err.Error(), "opening_visual") {
		t.Fatalf("got %v", err)
	}
}

func TestRuleExtractorProducesCompleteCardWithoutDomainFacts(t *testing.T) {
	card, err := ideation.RuleExtractor{}.Extract(context.Background(), ideation.ExtractInput{
		PostID:          "post-1",
		Category:        "cooking",
		Title:           "Why does sourdough crack like that?",
		Description:     "A twist on fermentation timing",
		DurationSeconds: 45,
		ForbiddenTerms:  []string{"sourdough", "fermentation"},
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if err := card.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if card.ContainsForbiddenTerms("sourdough", "fermentation") {
		t.Fatalf("card leaked domain facts: %+v", card)
	}
	for _, field := range []string{card.HookType, card.OpeningVisual, card.BeatSequence,
		card.DensityCurve, card.EmotionArc, card.ControversyAnchor} {
		if strings.TrimSpace(field) == "" {
			t.Fatalf("empty dimension in %+v", card)
		}
	}
}

func TestRuleMigratorReturnsThreeToFiveTopicsWithMigrationSource(t *testing.T) {
	card := ideation.StructureCard{
		ID:                "card-1",
		HookType:          "question-hook",
		OpeningVisual:     "face-close-up",
		BeatSequence:      "setup→twist→payoff",
		DensityCurve:      "sparse→dense→sparse",
		EmotionArc:        "curiosity→tension→relief",
		ControversyAnchor: "expectation-violation",
	}
	topics, err := ideation.RuleMigrator{}.Migrate(context.Background(), ideation.MigrateRequest{
		Card:           card,
		UserTheme:      "home fitness",
		TargetCategory: "fitness",
	})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(topics) < ideation.MinTopics || len(topics) > ideation.MaxTopics {
		t.Fatalf("got %d topics, want %d–%d", len(topics), ideation.MinTopics, ideation.MaxTopics)
	}
	for _, topic := range topics {
		if topic.MigrationSource != card.ID {
			t.Fatalf("topic missing migration source: %+v", topic)
		}
		if topic.WhyFits == "" || topic.Title == "" {
			t.Fatalf("incomplete topic: %+v", topic)
		}
	}
}

func TestRecallTopKOrdersBySimilarity(t *testing.T) {
	query := ideation.EmbedFromCard("question-hook", "face-close-up", "setup→twist→payoff",
		"sparse→dense→sparse", "curiosity→tension→relief", "expectation-violation")
	near := ideation.StructureCard{ID: "near", Embedding: query}
	far := ideation.StructureCard{ID: "far", Embedding: ideation.EmbedFromCard(
		"number-hook", "fast-cut-montage", "setup→build→payoff",
		"dense→sparse→dense", "surprise→delight", "taboo-challenge")}
	matches := ideation.RecallTopK(query, []ideation.StructureCard{near, far}, 2)
	if len(matches) != 2 {
		t.Fatalf("got %d matches", len(matches))
	}
	if matches[0].Card.ID != "near" {
		t.Fatalf("got %+v, want near first", matches)
	}
	if matches[0].Similarity <= matches[1].Similarity {
		t.Fatalf("similarity order wrong: %+v", matches)
	}
}

func TestGraphNeighborsFindsLinkedCards(t *testing.T) {
	cards := map[string]ideation.StructureCard{
		"a": {ID: "a"},
		"b": {ID: "b"},
		"c": {ID: "c"},
	}
	edges := []ideation.Edge{
		{FromID: "a", ToID: "b", Rel: ideation.RelSimilar},
		{FromID: "a", ToID: "c", Rel: ideation.RelVariant},
	}
	neighbors := ideation.Neighbors("a", edges, cards, "")
	if len(neighbors) != 2 {
		t.Fatalf("got %d neighbors, want 2", len(neighbors))
	}
	similar := ideation.Neighbors("a", edges, cards, ideation.RelSimilar)
	if len(similar) != 1 || similar[0].ID != "b" {
		t.Fatalf("got %+v, want b", similar)
	}
}
