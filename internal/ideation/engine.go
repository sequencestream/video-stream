package ideation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"

	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

// Store is the persistence the engine needs.
type Store interface {
	PutStructureCard(ctx context.Context, c store.StructureCardRecord) error
	StructureCard(ctx context.Context, id string) (store.StructureCardRecord, error)
	StructureCards(ctx context.Context, category string, limit int) ([]store.StructureCardRecord, error)
	PutStructureEdge(ctx context.Context, e store.StructureEdgeRecord) error
	StructureEdges(ctx context.Context) ([]store.StructureEdgeRecord, error)
	PutTopicCards(ctx context.Context, cards []store.TopicCardRecord) error
	TopicCards(ctx context.Context, structureCardID string, limit int) ([]store.TopicCardRecord, error)
}

// Options configures an Engine.
type Options struct {
	Store     Store
	Extractor Extractor
	Migrator  Migrator
	Reporter  telemetry.Reporter
	Logger    *slog.Logger
}

// Engine extracts structure cards and migrates them into topic ideas.
type Engine struct {
	store     Store
	extractor Extractor
	migrator  Migrator
	reporter  telemetry.Reporter
	logger    *slog.Logger
}

// ErrNoStore means the engine was built without persistence.
var ErrNoStore = errors.New("ideation has no store configured")

// New builds an Engine.
func New(opts Options) *Engine {
	e := &Engine{
		store:     opts.Store,
		extractor: opts.Extractor,
		migrator:  opts.Migrator,
		reporter:  opts.Reporter,
		logger:    opts.Logger,
	}
	if e.extractor == nil {
		e.extractor = RuleExtractor{}
	}
	if e.migrator == nil {
		e.migrator = RuleMigrator{}
	}
	if e.reporter == nil {
		e.reporter = telemetry.Nop()
	}
	if e.logger == nil {
		e.logger = slog.New(slog.DiscardHandler)
	}
	return e
}

// Extract extracts and persists one structure card.
func (e *Engine) Extract(ctx context.Context, in ExtractInput) (StructureCard, error) {
	if e.store == nil {
		return StructureCard{}, ErrNoStore
	}
	card, err := e.extractor.Extract(ctx, in)
	if err != nil {
		return StructureCard{}, err
	}
	card.ID = newID()
	if err := e.store.PutStructureCard(ctx, recordFromCard(card)); err != nil {
		return StructureCard{}, err
	}
	e.report(ctx, "ideation.extracted", map[string]any{"card_id": card.ID})
	return card, nil
}

// Cards lists stored structure cards.
func (e *Engine) Cards(ctx context.Context, category string, limit int) ([]StructureCard, error) {
	if e.store == nil {
		return nil, ErrNoStore
	}
	records, err := e.store.StructureCards(ctx, category, limit)
	if err != nil {
		return nil, err
	}
	out := make([]StructureCard, 0, len(records))
	for _, r := range records {
		out = append(out, cardFromRecord(r))
	}
	return out, nil
}

// Card returns one structure card by id.
func (e *Engine) Card(ctx context.Context, id string) (StructureCard, error) {
	if e.store == nil {
		return StructureCard{}, ErrNoStore
	}
	r, err := e.store.StructureCard(ctx, id)
	if err != nil {
		return StructureCard{}, err
	}
	return cardFromRecord(r), nil
}

// LinkCards adds a graph edge between two cards.
func (e *Engine) LinkCards(ctx context.Context, edge Edge) error {
	if e.store == nil {
		return ErrNoStore
	}
	edge = edge.normalise()
	return e.store.PutStructureEdge(ctx, store.StructureEdgeRecord{
		FromID: edge.FromID,
		ToID:   edge.ToID,
		Rel:    string(edge.Rel),
	})
}

// GraphNeighbors returns cards linked to cardID.
func (e *Engine) GraphNeighbors(ctx context.Context, cardID string, rel EdgeRel) ([]StructureCard, error) {
	if e.store == nil {
		return nil, ErrNoStore
	}
	edgeRecords, err := e.store.StructureEdges(ctx)
	if err != nil {
		return nil, err
	}
	records, err := e.store.StructureCards(ctx, "", 0)
	if err != nil {
		return nil, err
	}
	cards := make(map[string]StructureCard, len(records))
	for _, r := range records {
		c := cardFromRecord(r)
		cards[c.ID] = c
	}
	edges := make([]Edge, 0, len(edgeRecords))
	for _, er := range edgeRecords {
		edges = append(edges, Edge{FromID: er.FromID, ToID: er.ToID, Rel: EdgeRel(er.Rel)})
	}
	return Neighbors(cardID, edges, cards, rel), nil
}

// Recall finds top-k similar cards by embedding.
func (e *Engine) Recall(ctx context.Context, query []float64, k int) ([]RecallMatch, error) {
	if e.store == nil {
		return nil, ErrNoStore
	}
	records, err := e.store.StructureCards(ctx, "", 0)
	if err != nil {
		return nil, err
	}
	cards := make([]StructureCard, 0, len(records))
	for _, r := range records {
		cards = append(cards, cardFromRecord(r))
	}
	return RecallTopK(query, cards, k), nil
}

// MigrateTopics produces and persists cross-category topic cards.
func (e *Engine) MigrateTopics(ctx context.Context, req MigrateRequest) ([]TopicCard, error) {
	if e.store == nil {
		return nil, ErrNoStore
	}
	if req.Card.ID == "" {
		return nil, errors.New("structure card id is required")
	}
	topics, err := e.migrator.Migrate(ctx, req)
	if err != nil {
		return nil, err
	}
	records := make([]store.TopicCardRecord, 0, len(topics))
	for i := range topics {
		topics[i].ID = newID()
		records = append(records, topicRecordFromCard(topics[i]))
	}
	if err := e.store.PutTopicCards(ctx, records); err != nil {
		return nil, err
	}
	e.report(ctx, "ideation.migrated", map[string]any{
		"card_id": req.Card.ID,
		"topics":  len(topics),
	})
	return topics, nil
}

// Topics lists topic cards, optionally filtered by structure card id.
func (e *Engine) Topics(ctx context.Context, structureCardID string, limit int) ([]TopicCard, error) {
	if e.store == nil {
		return nil, ErrNoStore
	}
	records, err := e.store.TopicCards(ctx, structureCardID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]TopicCard, 0, len(records))
	for _, r := range records {
		out = append(out, topicFromRecord(r))
	}
	return out, nil
}

func (e *Engine) report(ctx context.Context, name string, fields map[string]any) {
	err := telemetry.Report(ctx, e.reporter, name, fields)
	if err != nil && !errors.Is(err, context.Canceled) {
		e.logger.WarnContext(ctx, "reporting ideation event failed", slog.String("error", err.Error()))
	}
}

func recordFromCard(c StructureCard) store.StructureCardRecord {
	return store.StructureCardRecord{
		ID:                c.ID,
		SourcePostID:      c.SourcePostID,
		SourceCategory:    c.SourceCategory,
		HookType:          c.HookType,
		OpeningVisual:     c.OpeningVisual,
		BeatSequence:      c.BeatSequence,
		DensityCurve:      c.DensityCurve,
		EmotionArc:        c.EmotionArc,
		ControversyAnchor: c.ControversyAnchor,
		Embedding:         c.Embedding,
	}
}

func cardFromRecord(r store.StructureCardRecord) StructureCard {
	return StructureCard{
		ID:                r.ID,
		SourcePostID:      r.SourcePostID,
		SourceCategory:    r.SourceCategory,
		HookType:          r.HookType,
		OpeningVisual:     r.OpeningVisual,
		BeatSequence:      r.BeatSequence,
		DensityCurve:      r.DensityCurve,
		EmotionArc:        r.EmotionArc,
		ControversyAnchor: r.ControversyAnchor,
		Embedding:         r.Embedding,
	}
}

func topicRecordFromCard(t TopicCard) store.TopicCardRecord {
	return store.TopicCardRecord{
		ID:              t.ID,
		StructureCardID: t.StructureCardID,
		Title:           t.Title,
		Angle:           t.Angle,
		MigrationSource: t.MigrationSource,
		WhyFits:         t.WhyFits,
		TargetCategory:  t.TargetCategory,
		UserTheme:       t.UserTheme,
	}
}

func topicFromRecord(r store.TopicCardRecord) TopicCard {
	return TopicCard{
		ID:              r.ID,
		StructureCardID: r.StructureCardID,
		Title:           r.Title,
		Angle:           r.Angle,
		MigrationSource: r.MigrationSource,
		WhyFits:         r.WhyFits,
		TargetCategory:  r.TargetCategory,
		UserTheme:       r.UserTheme,
	}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
