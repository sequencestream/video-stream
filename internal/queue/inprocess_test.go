package queue_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/queue"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

func newTestQueue(t *testing.T, registry *queue.Registry) (*queue.InProcess, *store.SQLiteStore, *telemetry.MemoryReporter) {
	t.Helper()

	taskStore, err := store.OpenSQLite(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { taskStore.Close() })

	reporter := telemetry.NewMemoryReporter()
	q := queue.NewInProcess(queue.Options{
		Store:    taskStore,
		Registry: registry,
		Reporter: reporter,
		Workers:  2,
		Buffer:   8,
	})

	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("start queue: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := q.Stop(ctx); err != nil {
			t.Errorf("stop queue: %v", err)
		}
	})

	return q, taskStore, reporter
}

func waitForTerminal(t *testing.T, s store.TaskStore, id string) store.Task {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		task, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		if task.Status.Terminal() {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s stuck in %s", id, task.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSubmitRunsHandlerAndPersistsResult(t *testing.T) {
	registry := queue.NewRegistry()
	registry.Register("echo", func(_ context.Context, task store.Task) (map[string]any, error) {
		return map[string]any{"echo": task.Payload["message"]}, nil
	})

	q, taskStore, reporter := newTestQueue(t, registry)

	submitted, err := q.Submit(context.Background(), queue.Submission{
		Type:    "echo",
		Title:   "receipt check",
		Payload: map[string]any{"message": "hello"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if submitted.ID == "" {
		t.Fatal("submit returned an empty task id")
	}
	if submitted.Status != store.StatusPending {
		t.Fatalf("receipt status = %s, want pending", submitted.Status)
	}

	done := waitForTerminal(t, taskStore, submitted.ID)
	if done.Status != store.StatusSucceeded {
		t.Fatalf("status = %s, error = %q; want succeeded", done.Status, done.Error)
	}
	if got := done.Result["echo"]; got != "hello" {
		t.Fatalf("result echo = %v, want hello", got)
	}

	if names := eventNames(reporter); !contains(names, "task.submitted") || !contains(names, "task.succeeded") {
		t.Fatalf("telemetry events = %v, want submitted and succeeded", names)
	}
}

func TestSubmitRejectsUnknownTypeWithoutPersisting(t *testing.T) {
	q, taskStore, _ := newTestQueue(t, queue.NewRegistry())

	_, err := q.Submit(context.Background(), queue.Submission{Type: "nope"})
	if !errors.Is(err, queue.ErrUnknownType) {
		t.Fatalf("err = %v, want ErrUnknownType", err)
	}

	tasks, err := taskStore.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("persisted %d unrunnable tasks, want 0", len(tasks))
	}
}

func TestHandlerErrorIsRecordedAsFailure(t *testing.T) {
	registry := queue.NewRegistry()
	registry.Register("render", func(context.Context, store.Task) (map[string]any, error) {
		return nil, errors.New("render pipeline is not implemented yet")
	})

	q, taskStore, _ := newTestQueue(t, registry)

	submitted, err := q.Submit(context.Background(), queue.Submission{Type: "render", Title: "placeholder"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	done := waitForTerminal(t, taskStore, submitted.ID)
	if done.Status != store.StatusFailed {
		t.Fatalf("status = %s, want failed", done.Status)
	}
	if done.Error == "" {
		t.Fatal("failed task carries no error message")
	}
}

func TestHandlerPanicDoesNotKillTheWorkerPool(t *testing.T) {
	registry := queue.NewRegistry()
	registry.Register("boom", func(context.Context, store.Task) (map[string]any, error) {
		panic("handler exploded")
	})
	registry.Register("fine", func(context.Context, store.Task) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	})

	q, taskStore, _ := newTestQueue(t, registry)

	boom, err := q.Submit(context.Background(), queue.Submission{Type: "boom"})
	if err != nil {
		t.Fatalf("submit boom: %v", err)
	}
	if got := waitForTerminal(t, taskStore, boom.ID); got.Status != store.StatusFailed {
		t.Fatalf("panicking task status = %s, want failed", got.Status)
	}

	fine, err := q.Submit(context.Background(), queue.Submission{Type: "fine"})
	if err != nil {
		t.Fatalf("submit fine: %v", err)
	}
	if got := waitForTerminal(t, taskStore, fine.ID); got.Status != store.StatusSucceeded {
		t.Fatalf("follow-up task status = %s, want succeeded", got.Status)
	}
}

func eventNames(r *telemetry.MemoryReporter) []string {
	events := r.Events()
	names := make([]string, 0, len(events))
	for _, ev := range events {
		names = append(names, ev.Name)
	}
	return names
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
