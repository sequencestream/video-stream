// Package queue dispatches tasks to registered handlers.
//
// The MVP runs an in-process worker pool whose state transitions are persisted
// through store.TaskStore. Callers depend only on the Queue interface, so the
// implementation can be replaced with a durable workflow engine (Temporal) with
// no change at the call sites.
package queue

import (
	"context"
	"errors"
	"fmt"

	"github.com/sequencestream/video-stream/internal/store"
)

// ErrUnknownType is returned when no handler is registered for a task type.
var ErrUnknownType = errors.New("no handler registered for task type")

// Handler executes one task and returns its result payload.
type Handler func(ctx context.Context, t store.Task) (map[string]any, error)

// Submission describes work to enqueue.
type Submission struct {
	Type    string
	Title   string
	Payload map[string]any
}

// Queue accepts work and runs it.
type Queue interface {
	// Submit persists the task and schedules it for execution.
	Submit(ctx context.Context, s Submission) (store.Task, error)
	// Start begins processing. It returns once workers are running.
	Start(ctx context.Context) error
	// Stop drains in-flight work and releases the workers.
	Stop(ctx context.Context) error
	// Types lists the registered task types.
	Types() []string
}

// Registry maps a task type to its handler.
type Registry struct {
	handlers map[string]Handler
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{handlers: map[string]Handler{}}
}

// Register binds a handler to a task type. Registering a type twice is a
// programming error and panics at wiring time rather than silently shadowing.
func (r *Registry) Register(taskType string, h Handler) {
	if taskType == "" {
		panic("queue: task type must not be empty")
	}
	if h == nil {
		panic("queue: handler must not be nil")
	}
	if _, exists := r.handlers[taskType]; exists {
		panic(fmt.Sprintf("queue: duplicate handler for task type %q", taskType))
	}
	r.handlers[taskType] = h
}

// Lookup returns the handler for a task type.
func (r *Registry) Lookup(taskType string) (Handler, bool) {
	h, ok := r.handlers[taskType]
	return h, ok
}

// Types returns the registered task types in no particular order.
func (r *Registry) Types() []string {
	types := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		types = append(types, t)
	}
	return types
}
