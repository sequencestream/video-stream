package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var _ IdeationStore = (*SQLiteStore)(nil)

// PutStructureCard inserts one structure card.
func (s *SQLiteStore) PutStructureCard(ctx context.Context, c StructureCardRecord) error {
	if strings.TrimSpace(c.ID) == "" {
		return errors.New("structure card id must not be empty")
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	emb, err := encodeEmbedding(c.Embedding)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO structure_cards (id, source_post_id, source_category, hook_type,
		 opening_visual, beat_sequence, density_curve, emotion_arc, controversy_anchor,
		 embedding, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.SourcePostID, c.SourceCategory, c.HookType, c.OpeningVisual,
		c.BeatSequence, c.DensityCurve, c.EmotionArc, c.ControversyAnchor,
		emb, c.CreatedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("put structure card %s: %w", c.ID, err)
	}
	return nil
}

// StructureCard returns one card by id.
func (s *SQLiteStore) StructureCard(ctx context.Context, id string) (StructureCardRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, source_post_id, source_category, hook_type, opening_visual,
		        beat_sequence, density_curve, emotion_arc, controversy_anchor,
		        embedding, created_at
		 FROM structure_cards WHERE id = ?`, id)
	c, err := scanStructureCard(row)
	if errors.Is(err, sql.ErrNoRows) {
		return StructureCardRecord{}, ErrStructureCardNotFound
	}
	if err != nil {
		return StructureCardRecord{}, fmt.Errorf("get structure card %s: %w", id, err)
	}
	return c, nil
}

// StructureCards lists cards, optionally filtered by source category.
func (s *SQLiteStore) StructureCards(ctx context.Context, category string, limit int) ([]StructureCardRecord, error) {
	q := `SELECT id, source_post_id, source_category, hook_type, opening_visual,
	             beat_sequence, density_curve, emotion_arc, controversy_anchor,
	             embedding, created_at
	      FROM structure_cards
	      WHERE ? = '' OR source_category = ?
	      ORDER BY created_at ASC, id ASC`
	args := []any{category, category}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list structure cards: %w", err)
	}
	defer rows.Close()
	return collectStructureCards(rows)
}

// PutStructureEdge inserts one graph edge.
func (s *SQLiteStore) PutStructureEdge(ctx context.Context, e StructureEdgeRecord) error {
	if strings.TrimSpace(e.FromID) == "" || strings.TrimSpace(e.ToID) == "" {
		return errors.New("structure edge from_id and to_id must not be empty")
	}
	if e.Rel == "" {
		e.Rel = "similar"
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO structure_edges (from_id, to_id, rel, created_at)
		 VALUES (?, ?, ?, ?)`,
		e.FromID, e.ToID, e.Rel, e.CreatedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("put structure edge %s→%s: %w", e.FromID, e.ToID, err)
	}
	return nil
}

// StructureEdges returns all graph edges.
func (s *SQLiteStore) StructureEdges(ctx context.Context) ([]StructureEdgeRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT from_id, to_id, rel, created_at FROM structure_edges ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list structure edges: %w", err)
	}
	defer rows.Close()
	var out []StructureEdgeRecord
	for rows.Next() {
		var e StructureEdgeRecord
		var created int64
		if err := rows.Scan(&e.FromID, &e.ToID, &e.Rel, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = time.UnixMilli(created).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// PutTopicCards inserts topic cards in one batch.
func (s *SQLiteStore) PutTopicCards(ctx context.Context, cards []TopicCardRecord) error {
	if len(cards) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO topic_cards (id, structure_card_id, title, angle, migration_source,
		 why_fits, target_category, user_theme, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, c := range cards {
		if strings.TrimSpace(c.ID) == "" {
			return errors.New("topic card id must not be empty")
		}
		if c.CreatedAt.IsZero() {
			c.CreatedAt = time.Now().UTC()
		}
		if _, err := stmt.ExecContext(ctx, c.ID, c.StructureCardID, c.Title, c.Angle,
			c.MigrationSource, c.WhyFits, c.TargetCategory, c.UserTheme,
			c.CreatedAt.UTC().UnixMilli()); err != nil {
			return fmt.Errorf("put topic card %s: %w", c.ID, err)
		}
	}
	return tx.Commit()
}

// TopicCards lists topic cards, optionally filtered by structure card id.
func (s *SQLiteStore) TopicCards(ctx context.Context, structureCardID string, limit int) ([]TopicCardRecord, error) {
	q := `SELECT id, structure_card_id, title, angle, migration_source, why_fits,
	             target_category, user_theme, created_at
	      FROM topic_cards
	      WHERE ? = '' OR structure_card_id = ?
	      ORDER BY created_at ASC, id ASC`
	args := []any{structureCardID, structureCardID}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list topic cards: %w", err)
	}
	defer rows.Close()
	return collectTopicCards(rows)
}

func scanStructureCard(sc scanner) (StructureCardRecord, error) {
	var c StructureCardRecord
	var emb string
	var created int64
	if err := sc.Scan(&c.ID, &c.SourcePostID, &c.SourceCategory, &c.HookType,
		&c.OpeningVisual, &c.BeatSequence, &c.DensityCurve, &c.EmotionArc,
		&c.ControversyAnchor, &emb, &created); err != nil {
		return StructureCardRecord{}, err
	}
	c.CreatedAt = time.UnixMilli(created).UTC()
	var err error
	if c.Embedding, err = decodeEmbedding(emb); err != nil {
		return StructureCardRecord{}, err
	}
	return c, nil
}

func collectStructureCards(rows *sql.Rows) ([]StructureCardRecord, error) {
	var out []StructureCardRecord
	for rows.Next() {
		c, err := scanStructureCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func collectTopicCards(rows *sql.Rows) ([]TopicCardRecord, error) {
	var out []TopicCardRecord
	for rows.Next() {
		var c TopicCardRecord
		var created int64
		if err := rows.Scan(&c.ID, &c.StructureCardID, &c.Title, &c.Angle,
			&c.MigrationSource, &c.WhyFits, &c.TargetCategory, &c.UserTheme, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = time.UnixMilli(created).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func encodeEmbedding(v []float64) (string, error) {
	if len(v) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode embedding: %w", err)
	}
	return string(b), nil
}

func decodeEmbedding(s string) ([]float64, error) {
	if s == "" || s == "[]" {
		return nil, nil
	}
	var v []float64
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, fmt.Errorf("decode embedding: %w", err)
	}
	return v, nil
}
