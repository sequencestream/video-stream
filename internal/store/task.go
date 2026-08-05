// Package store persists tasks. The MVP is backed by SQLite through a pure-Go
// driver so the main service stays a single dependency-free binary.
package store

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a task id has no matching row.
var ErrNotFound = errors.New("task not found")

// Status is the lifecycle state of a task.
type Status string

// The task lifecycle is pending -> running -> succeeded|failed.
const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Terminal reports whether no further transition is possible.
func (s Status) Terminal() bool {
	return s == StatusSucceeded || s == StatusFailed
}

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusRunning, StatusSucceeded, StatusFailed:
		return true
	default:
		return false
	}
}

// Task is a unit of work tracked by the queue.
type Task struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Status    Status         `json:"status"`
	Payload   map[string]any `json:"payload,omitempty"`
	Result    map[string]any `json:"result,omitempty"`
	Error     string         `json:"error,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// TaskStore is the persistence contract the queue depends on. Keeping the queue
// behind this interface is what allows the SQLite backing to be swapped for a
// durable workflow engine later without touching queue call sites.
type TaskStore interface {
	// Create inserts a new task.
	Create(ctx context.Context, t Task) error
	// Get returns a task by id, or ErrNotFound.
	Get(ctx context.Context, id string) (Task, error)
	// List returns tasks newest first, capped at limit.
	List(ctx context.Context, limit int) ([]Task, error)
	// MarkRunning transitions a task to running.
	MarkRunning(ctx context.Context, id string) error
	// Finish transitions a task to a terminal status with its result or error.
	Finish(ctx context.Context, id string, status Status, result map[string]any, taskErr string) error
	// Unfinished returns tasks left in a non-terminal state, oldest first, so a
	// restarted daemon can requeue work that was in flight when it stopped.
	Unfinished(ctx context.Context) ([]Task, error)
	// Close releases the underlying resources.
	Close() error
}
