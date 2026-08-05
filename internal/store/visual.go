package store

import (
	"context"
	"errors"
	"time"
)

var ErrStylePackNotFound = errors.New("style pack not found")

// StylePackRecord is one persisted L2 visual style pack.
type StylePackRecord struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	SchemaVersion int       `json:"schema_version"`
	Document      string    `json:"document"`
	CreatedAt     time.Time `json:"created_at"`
}

// VisualStore persists style packs.
type VisualStore interface {
	PutStylePack(ctx context.Context, p StylePackRecord) error
	StylePack(ctx context.Context, id string) (StylePackRecord, error)
	StylePacks(ctx context.Context) ([]StylePackRecord, error)
}
