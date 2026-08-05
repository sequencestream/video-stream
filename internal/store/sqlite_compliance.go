package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

var _ ComplianceStore = (*SQLiteStore)(nil)

// PriorFingerprints returns recent fingerprint vectors for an account.
func (s *SQLiteStore) PriorFingerprints(ctx context.Context, accountID string, limit int) ([][]float64, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT fingerprint FROM compliance_passes
		 WHERE account_id = ? ORDER BY created_at DESC LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("list compliance fingerprints: %w", err)
	}
	defer rows.Close()
	var out [][]float64
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		v, err := decodeEmbedding(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReuseCount counts structure card uses since the given time.
func (s *SQLiteStore) ReuseCount(ctx context.Context, accountID, structureCardID string, since time.Time) (int, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM compliance_passes
		 WHERE account_id = ? AND structure_card_id = ? AND created_at >= ?`,
		accountID, structureCardID, since.UTC().UnixMilli())
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count compliance reuses: %w", err)
	}
	return n, nil
}

// RecordPass stores a successful compliance check.
func (s *SQLiteStore) RecordPass(ctx context.Context, r ComplianceFingerprintRecord) error {
	if strings.TrimSpace(r.ID) == "" {
		r.ID = newComplianceID()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	emb, err := encodeEmbedding(r.Fingerprint)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO compliance_passes (id, account_id, structure_card_id, project_id, fingerprint, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.AccountID, r.StructureCardID, r.ProjectID, emb, r.CreatedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("record compliance pass: %w", err)
	}
	return nil
}

func newComplianceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
