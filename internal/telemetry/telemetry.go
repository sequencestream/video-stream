// Package telemetry defines the event reporting interface used across the
// pipeline. The MVP ships a no-op sink, a structured-log sink and an in-memory
// sink; a remote sink can be added later without touching call sites.
package telemetry

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"time"
)

// Event is a single reported occurrence.
type Event struct {
	// Name identifies the event, e.g. "task.succeeded".
	Name string
	// OccurredAt is filled in by NewEvent when the caller leaves it zero.
	OccurredAt time.Time
	// Attributes carries event-specific fields.
	Attributes map[string]any
}

// Reporter receives events. Implementations must be safe for concurrent use.
type Reporter interface {
	// Report submits a single event.
	Report(ctx context.Context, ev Event) error
	// Flush blocks until previously reported events have been handed to the
	// underlying sink.
	Flush(ctx context.Context) error
}

// NewEvent builds an event, defaulting the timestamp to now and copying
// attributes so later mutation by the caller cannot alter a reported event.
func NewEvent(name string, attrs map[string]any) Event {
	return Event{
		Name:       name,
		OccurredAt: time.Now().UTC(),
		Attributes: maps.Clone(attrs),
	}
}

// Report is a convenience wrapper that builds and submits an event.
func Report(ctx context.Context, r Reporter, name string, attrs map[string]any) error {
	return r.Report(ctx, NewEvent(name, attrs))
}

// nopReporter discards everything.
type nopReporter struct{}

// Nop returns a reporter that discards all events. It is the default so that
// telemetry can never be the reason a pipeline fails.
func Nop() Reporter { return nopReporter{} }

func (nopReporter) Report(context.Context, Event) error { return nil }
func (nopReporter) Flush(context.Context) error         { return nil }

// LogReporter writes events to a structured logger.
type LogReporter struct {
	logger *slog.Logger
}

// NewLogReporter returns a reporter that emits each event as a log record.
func NewLogReporter(logger *slog.Logger) *LogReporter {
	return &LogReporter{logger: logger}
}

// Report emits the event at info level with its attributes attached.
func (r *LogReporter) Report(ctx context.Context, ev Event) error {
	ev = normalize(ev)

	attrs := make([]any, 0, 2*len(ev.Attributes)+2)
	attrs = append(attrs, slog.String("event", ev.Name), slog.Time("occurred_at", ev.OccurredAt))
	for k, v := range ev.Attributes {
		attrs = append(attrs, slog.Any(k, v))
	}
	r.logger.LogAttrs(ctx, slog.LevelInfo, "telemetry", slog.Group("telemetry", attrs...))
	return nil
}

// Flush is a no-op: the logger writes synchronously.
func (r *LogReporter) Flush(context.Context) error { return nil }

// MemoryReporter retains events in memory. It backs tests today and is the
// buffering primitive a remote sink would drain.
type MemoryReporter struct {
	mu      sync.Mutex
	events  []Event
	flushes int
}

// NewMemoryReporter returns an empty in-memory reporter.
func NewMemoryReporter() *MemoryReporter { return &MemoryReporter{} }

// Report appends the event to the buffer.
func (r *MemoryReporter) Report(ctx context.Context, ev Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, normalize(ev))
	return nil
}

// Flush records that a flush happened; there is no downstream sink to drain.
func (r *MemoryReporter) Flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushes++
	return nil
}

// Events returns a snapshot of the buffered events.
func (r *MemoryReporter) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// Flushes returns how many times Flush succeeded.
func (r *MemoryReporter) Flushes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushes
}

// normalize fills in a missing timestamp and guarantees a non-nil attribute map
// so consumers never have to nil-check.
func normalize(ev Event) Event {
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	if ev.Attributes == nil {
		ev.Attributes = map[string]any{}
	}
	return ev
}
