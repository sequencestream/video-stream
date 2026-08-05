package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

var _ HybridStore = (*SQLiteStore)(nil)

func (s *SQLiteStore) PutHybridPlan(ctx context.Context, projectID string, plans []HybridShotRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM hybrid_shots WHERE project_id = ?`, projectID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO hybrid_shots (project_id, seg_id, route, reason, stock_query, ken_burns_json, stock_json, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().UTC().UnixMilli()
	for _, p := range plans {
		if _, err := stmt.ExecContext(ctx, projectID, p.SegID, p.Route, p.Reason,
			p.StockQuery, p.KenBurnsJSON, p.StockJSON, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) HybridPlans(ctx context.Context, projectID string) ([]HybridShotRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT project_id, seg_id, route, reason, stock_query, ken_burns_json, stock_json, updated_at
		 FROM hybrid_shots WHERE project_id = ? ORDER BY seg_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HybridShotRecord
	for rows.Next() {
		var r HybridShotRecord
		var updated int64
		if err := rows.Scan(&r.ProjectID, &r.SegID, &r.Route, &r.Reason, &r.StockQuery,
			&r.KenBurnsJSON, &r.StockJSON, &updated); err != nil {
			return nil, err
		}
		r.UpdatedAt = time.UnixMilli(updated).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) AttachHybridStock(ctx context.Context, projectID, segID, stockJSON string) error {
	if strings.TrimSpace(stockJSON) == "" {
		return fmt.Errorf("stock json empty")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE hybrid_shots SET stock_json = ?, updated_at = ? WHERE project_id = ? AND seg_id = ?`,
		stockJSON, time.Now().UTC().UnixMilli(), projectID, segID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("hybrid shot %s/%s not found", projectID, segID)
	}
	return nil
}
