package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var _ YouTubeStore = (*SQLiteStore)(nil)

func (s *SQLiteStore) PutYouTubeUpload(ctx context.Context, rec YouTubeUploadRecord) error {
	if rec.ID == "" {
		return fmt.Errorf("youtube upload id must not be empty")
	}
	if rec.ProjectID == "" {
		return fmt.Errorf("youtube upload project_id must not be empty")
	}
	created := rec.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO youtube_uploads (id, project_id, session_id, video_id, video_path, status, error, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   video_id=excluded.video_id, status=excluded.status, error=excluded.error`,
		rec.ID, rec.ProjectID, rec.SessionID, rec.VideoID, rec.VideoPath,
		rec.Status, rec.Error, created.UnixMilli())
	return err
}

func (s *SQLiteStore) GetYouTubeUpload(ctx context.Context, id string) (YouTubeUploadRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, session_id, video_id, video_path, status, error, created_at
		 FROM youtube_uploads WHERE id = ?`, id)
	var rec YouTubeUploadRecord
	var created int64
	if err := row.Scan(&rec.ID, &rec.ProjectID, &rec.SessionID, &rec.VideoID,
		&rec.VideoPath, &rec.Status, &rec.Error, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return YouTubeUploadRecord{}, ErrYouTubeUploadNotFound
		}
		return YouTubeUploadRecord{}, err
	}
	rec.CreatedAt = time.UnixMilli(created).UTC()
	return rec, nil
}
