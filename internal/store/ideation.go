package store

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrStructureCardNotFound is returned when no structure card has the id.
	ErrStructureCardNotFound = errors.New("structure card not found")
)

// StructureCardRecord is one persisted structure card.
type StructureCardRecord struct {
	ID                string    `json:"id"`
	SourcePostID      string    `json:"source_post_id"`
	SourceCategory    string    `json:"source_category,omitempty"`
	HookType          string    `json:"hook_type"`
	OpeningVisual     string    `json:"opening_visual"`
	BeatSequence      string    `json:"beat_sequence"`
	DensityCurve      string    `json:"density_curve"`
	EmotionArc        string    `json:"emotion_arc"`
	ControversyAnchor string    `json:"controversy_anchor"`
	Embedding         []float64 `json:"embedding,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// StructureEdgeRecord is one graph edge between structure cards.
type StructureEdgeRecord struct {
	FromID    string    `json:"from_id"`
	ToID      string    `json:"to_id"`
	Rel       string    `json:"rel"`
	CreatedAt time.Time `json:"created_at"`
}

// TopicCardRecord is one cross-category topic idea.
type TopicCardRecord struct {
	ID              string    `json:"id"`
	StructureCardID string    `json:"structure_card_id"`
	Title           string    `json:"title"`
	Angle           string    `json:"angle"`
	MigrationSource string    `json:"migration_source"`
	WhyFits         string    `json:"why_fits"`
	TargetCategory  string    `json:"target_category,omitempty"`
	UserTheme       string    `json:"user_theme,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// IdeationStore persists structure cards, graph edges and topic cards.
type IdeationStore interface {
	PutStructureCard(ctx context.Context, c StructureCardRecord) error
	StructureCard(ctx context.Context, id string) (StructureCardRecord, error)
	StructureCards(ctx context.Context, category string, limit int) ([]StructureCardRecord, error)
	PutStructureEdge(ctx context.Context, e StructureEdgeRecord) error
	StructureEdges(ctx context.Context) ([]StructureEdgeRecord, error)
	PutTopicCards(ctx context.Context, cards []TopicCardRecord) error
	TopicCards(ctx context.Context, structureCardID string, limit int) ([]TopicCardRecord, error)
}
