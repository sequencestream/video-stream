package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sequencestream/video-stream/internal/store"
)

func TestSQLiteIdeationRoundTrip(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "ideation.db")
	s, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	card := store.StructureCardRecord{
		ID:                "card-1",
		SourcePostID:      "post-1",
		SourceCategory:    "cooking",
		HookType:          "question-hook",
		OpeningVisual:     "face-close-up",
		BeatSequence:      "setup→twist→payoff",
		DensityCurve:      "sparse→dense→sparse",
		EmotionArc:        "curiosity→tension→relief",
		ControversyAnchor: "expectation-violation",
		Embedding:         []float64{0.1, 0.2, 0.3},
	}
	if err := s.PutStructureCard(ctx, card); err != nil {
		t.Fatalf("put card: %v", err)
	}
	got, err := s.StructureCard(ctx, "card-1")
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	if got.HookType != card.HookType || len(got.Embedding) != 3 {
		t.Fatalf("got %+v", got)
	}

	card2 := card
	card2.ID = "card-2"
	if err := s.PutStructureCard(ctx, card2); err != nil {
		t.Fatalf("put card2: %v", err)
	}
	if err := s.PutStructureEdge(ctx, store.StructureEdgeRecord{
		FromID: "card-1", ToID: "card-2", Rel: "similar",
	}); err != nil {
		t.Fatalf("put edge: %v", err)
	}

	edges, err := s.StructureEdges(ctx)
	if err != nil {
		t.Fatalf("list edges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges", len(edges))
	}

	topics := []store.TopicCardRecord{{
		ID: "topic-1", StructureCardID: "card-1", Title: "t", Angle: "a",
		MigrationSource: "card-1", WhyFits: "why", TargetCategory: "fitness",
	}}
	if err := s.PutTopicCards(ctx, topics); err != nil {
		t.Fatalf("put topics: %v", err)
	}
	listed, err := s.TopicCards(ctx, "card-1", 0)
	if err != nil {
		t.Fatalf("list topics: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("got %d topics", len(listed))
	}
}
