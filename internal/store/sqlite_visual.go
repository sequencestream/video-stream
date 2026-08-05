package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var _ VisualStore = (*SQLiteStore)(nil)

func (s *SQLiteStore) PutStylePack(ctx context.Context, p StylePackRecord) error {
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("style pack id must not be empty")
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO style_packs (id, name, schema_version, document, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name=excluded.name, schema_version=excluded.schema_version,
		   document=excluded.document`,
		p.ID, p.Name, p.SchemaVersion, p.Document, p.CreatedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("put style pack %s: %w", p.ID, err)
	}
	return nil
}

func (s *SQLiteStore) StylePack(ctx context.Context, id string) (StylePackRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, schema_version, document, created_at FROM style_packs WHERE id = ?`, id)
	var r StylePackRecord
	var created int64
	if err := row.Scan(&r.ID, &r.Name, &r.SchemaVersion, &r.Document, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StylePackRecord{}, ErrStylePackNotFound
		}
		return StylePackRecord{}, err
	}
	r.CreatedAt = time.UnixMilli(created).UTC()
	return r, nil
}

func (s *SQLiteStore) StylePacks(ctx context.Context) ([]StylePackRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, schema_version, document, created_at FROM style_packs ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StylePackRecord
	for rows.Next() {
		var r StylePackRecord
		var created int64
		if err := rows.Scan(&r.ID, &r.Name, &r.SchemaVersion, &r.Document, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = time.UnixMilli(created).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
