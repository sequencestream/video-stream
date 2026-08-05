package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/telemetry"
)

func TestNewEventFillsTimestampAndCopiesAttributes(t *testing.T) {
	attrs := map[string]any{"task_id": "t-1"}
	ev := telemetry.NewEvent("task.created", attrs)

	if ev.Name != "task.created" {
		t.Fatalf("name = %q, want task.created", ev.Name)
	}
	if ev.OccurredAt.IsZero() {
		t.Fatal("OccurredAt was not filled in")
	}

	// Mutating the caller's map must not affect the built event, otherwise a
	// reused attribute map would silently rewrite history.
	attrs["task_id"] = "mutated"
	if got := ev.Attributes["task_id"]; got != "t-1" {
		t.Fatalf("attribute aliased caller map: got %v, want t-1", got)
	}
}

func TestNopReporterAcceptsEverything(t *testing.T) {
	r := telemetry.Nop()

	if err := r.Report(context.Background(), telemetry.Event{}); err != nil {
		t.Fatalf("Report on zero event: %v", err)
	}
	if err := telemetry.Report(context.Background(), r, "any", nil); err != nil {
		t.Fatalf("Report helper: %v", err)
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func TestLogReporterEmitsNameAndAttributes(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r := telemetry.NewLogReporter(logger)

	err := r.Report(context.Background(), telemetry.Event{
		Name:       "task.succeeded",
		OccurredAt: time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
		Attributes: map[string]any{"task_id": "t-9", "duration_ms": 12},
	})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("log line is not JSON: %v (%s)", err, buf.String())
	}

	group, ok := record["telemetry"].(map[string]any)
	if !ok {
		t.Fatalf("missing telemetry group in %s", buf.String())
	}
	if group["event"] != "task.succeeded" {
		t.Errorf("event = %v, want task.succeeded", group["event"])
	}
	if group["task_id"] != "t-9" {
		t.Errorf("task_id = %v, want t-9", group["task_id"])
	}
	if group["duration_ms"] != float64(12) {
		t.Errorf("duration_ms = %v, want 12", group["duration_ms"])
	}
	if group["occurred_at"] == nil {
		t.Error("occurred_at was not emitted")
	}
}

func TestMemoryReporterBuffersAndFlushes(t *testing.T) {
	r := telemetry.NewMemoryReporter()
	ctx := context.Background()

	if err := telemetry.Report(ctx, r, "first", map[string]any{"n": 1}); err != nil {
		t.Fatalf("Report first: %v", err)
	}
	if err := telemetry.Report(ctx, r, "second", nil); err != nil {
		t.Fatalf("Report second: %v", err)
	}
	if err := r.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	events := r.Events()
	if len(events) != 2 {
		t.Fatalf("buffered %d events, want 2", len(events))
	}
	if events[0].Name != "first" || events[1].Name != "second" {
		t.Fatalf("events out of order: %q, %q", events[0].Name, events[1].Name)
	}
	if events[1].Attributes == nil {
		t.Error("nil attributes were not normalized to an empty map")
	}
	if r.Flushes() != 1 {
		t.Errorf("Flushes = %d, want 1", r.Flushes())
	}
}

func TestMemoryReporterSnapshotIsDetached(t *testing.T) {
	r := telemetry.NewMemoryReporter()
	if err := telemetry.Report(context.Background(), r, "only", nil); err != nil {
		t.Fatalf("Report: %v", err)
	}

	snapshot := r.Events()
	snapshot[0].Name = "tampered"

	if again := r.Events(); again[0].Name != "only" {
		t.Fatalf("snapshot shares backing array: got %q", again[0].Name)
	}
}

func TestMemoryReporterHonoursCancelledContext(t *testing.T) {
	r := telemetry.NewMemoryReporter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := r.Report(ctx, telemetry.NewEvent("dropped", nil)); err == nil {
		t.Error("Report accepted an event on a cancelled context")
	}
	if err := r.Flush(ctx); err == nil {
		t.Error("Flush succeeded on a cancelled context")
	}
	if len(r.Events()) != 0 {
		t.Errorf("buffered %d events, want 0", len(r.Events()))
	}
}

func TestMemoryReporterIsConcurrencySafe(t *testing.T) {
	r := telemetry.NewMemoryReporter()
	ctx := context.Background()

	const goroutines, perGoroutine = 8, 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range perGoroutine {
				if err := telemetry.Report(ctx, r, "concurrent", map[string]any{"k": "v"}); err != nil {
					t.Errorf("Report: %v", err)
					return
				}
				_ = r.Events()
			}
		}()
	}
	wg.Wait()

	if got, want := len(r.Events()), goroutines*perGoroutine; got != want {
		t.Fatalf("recorded %d events, want %d", got, want)
	}
}
