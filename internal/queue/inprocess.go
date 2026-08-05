package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

// InProcess is the MVP queue: a bounded dispatch channel feeding a worker pool,
// with every state transition written through to the store.
type InProcess struct {
	store    store.TaskStore
	registry *Registry
	logger   *slog.Logger
	reporter telemetry.Reporter

	workers int
	ch      chan string

	startOnce sync.Once
	stopOnce  sync.Once
	wg        sync.WaitGroup
	cancel    context.CancelFunc
}

var _ Queue = (*InProcess)(nil)

// Options configures an InProcess queue.
type Options struct {
	Store    store.TaskStore
	Registry *Registry
	Logger   *slog.Logger
	Reporter telemetry.Reporter
	Workers  int
	Buffer   int
}

// NewInProcess builds an in-process queue. Workers and Buffer fall back to
// safe minimums so a partially filled Options cannot deadlock.
func NewInProcess(opts Options) *InProcess {
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	if opts.Buffer < 1 {
		opts.Buffer = 1
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Reporter == nil {
		opts.Reporter = telemetry.Nop()
	}
	return &InProcess{
		store:    opts.Store,
		registry: opts.Registry,
		logger:   opts.Logger,
		reporter: opts.Reporter,
		workers:  opts.Workers,
		ch:       make(chan string, opts.Buffer),
	}
}

// Submit validates the task type, persists the task as pending and hands its id
// to a worker. Rejecting unknown types before the insert keeps the store free of
// rows that can never run.
func (q *InProcess) Submit(ctx context.Context, s Submission) (store.Task, error) {
	if _, ok := q.registry.Lookup(s.Type); !ok {
		return store.Task{}, fmt.Errorf("%w: %q", ErrUnknownType, s.Type)
	}

	now := time.Now().UTC()
	task := store.Task{
		ID:        uuid.NewString(),
		Type:      s.Type,
		Title:     s.Title,
		Status:    store.StatusPending,
		Payload:   s.Payload,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := q.store.Create(ctx, task); err != nil {
		return store.Task{}, err
	}

	q.report(ctx, "task.submitted", task, nil)

	if err := q.dispatch(ctx, task.ID); err != nil {
		return store.Task{}, err
	}
	return task, nil
}

// dispatch enqueues an id, blocking while the buffer is full so that back
// pressure surfaces as a slow submit rather than a dropped task.
func (q *InProcess) dispatch(ctx context.Context, id string) error {
	select {
	case q.ch <- id:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Start recovers interrupted work and launches the worker pool.
func (q *InProcess) Start(ctx context.Context) error {
	var startErr error
	q.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		q.cancel = cancel

		if err := q.recover(ctx); err != nil {
			cancel()
			startErr = err
			return
		}

		q.wg.Add(q.workers)
		for i := range q.workers {
			go q.work(runCtx, i)
		}

		q.logger.Info("queue started", slog.Int("workers", q.workers), slog.Any("types", q.Types()))
	})
	return startErr
}

// recover requeues tasks that were still pending at shutdown and fails the ones
// that were mid-flight. A task caught in "running" may have had side effects, so
// silently re-running it could duplicate them; failing it loudly is the honest
// outcome until handlers declare idempotency.
func (q *InProcess) recover(ctx context.Context) error {
	unfinished, err := q.store.Unfinished(ctx)
	if err != nil {
		return fmt.Errorf("recover unfinished tasks: %w", err)
	}

	for _, task := range unfinished {
		if task.Status == store.StatusRunning {
			const reason = "interrupted: daemon restarted while the task was running"
			if err := q.store.Finish(ctx, task.ID, store.StatusFailed, nil, reason); err != nil {
				return fmt.Errorf("fail interrupted task %s: %w", task.ID, err)
			}
			q.logger.Warn("failed interrupted task", slog.String("task_id", task.ID))
			q.report(ctx, "task.failed", task, map[string]any{"error": reason})
			continue
		}
		if err := q.dispatch(ctx, task.ID); err != nil {
			return fmt.Errorf("requeue task %s: %w", task.ID, err)
		}
		q.logger.Info("requeued pending task", slog.String("task_id", task.ID))
	}
	return nil
}

// Stop cancels the workers and waits for in-flight tasks to settle.
func (q *InProcess) Stop(ctx context.Context) error {
	q.stopOnce.Do(func() {
		if q.cancel != nil {
			q.cancel()
		}
	})

	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		q.logger.Info("queue stopped")
		return nil
	case <-ctx.Done():
		return fmt.Errorf("queue shutdown timed out: %w", ctx.Err())
	}
}

// Types lists the registered task types, sorted for stable output.
func (q *InProcess) Types() []string {
	types := q.registry.Types()
	sort.Strings(types)
	return types
}

func (q *InProcess) work(ctx context.Context, worker int) {
	defer q.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case id := <-q.ch:
			q.run(ctx, worker, id)
		}
	}
}

func (q *InProcess) run(ctx context.Context, worker int, id string) {
	logger := q.logger.With(slog.String("task_id", id), slog.Int("worker", worker))

	task, err := q.store.Get(ctx, id)
	if err != nil {
		logger.Error("load task", slog.String("error", err.Error()))
		return
	}

	if err := q.store.MarkRunning(ctx, id); err != nil {
		// Another worker already claimed it, or it is no longer pending.
		logger.Warn("skip task", slog.String("reason", err.Error()))
		return
	}
	task.Status = store.StatusRunning
	q.report(ctx, "task.started", task, nil)

	handler, ok := q.registry.Lookup(task.Type)
	if !ok {
		q.finish(ctx, logger, task, store.StatusFailed, nil,
			fmt.Errorf("%w: %q", ErrUnknownType, task.Type))
		return
	}

	started := time.Now()
	result, handlerErr := runHandler(ctx, handler, task)
	duration := time.Since(started)

	status := store.StatusSucceeded
	if handlerErr != nil {
		status = store.StatusFailed
	}
	logger = logger.With(slog.Duration("duration", duration))
	q.finish(ctx, logger, task, status, result, handlerErr)
}

// runHandler converts a handler panic into an error so one bad task type cannot
// take down the whole worker pool.
func runHandler(ctx context.Context, h Handler, task store.Task) (result map[string]any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panicked: %v", r)
		}
	}()
	return h(ctx, task)
}

func (q *InProcess) finish(ctx context.Context, logger *slog.Logger, task store.Task, status store.Status, result map[string]any, taskErr error) {
	msg := ""
	if taskErr != nil {
		msg = taskErr.Error()
	}

	// The worker context is cancelled during shutdown, but the terminal state
	// still has to reach the store or the task looks stuck forever.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := q.store.Finish(writeCtx, task.ID, status, result, msg); err != nil {
		logger.Error("persist task result", slog.String("error", err.Error()))
		return
	}

	attrs := map[string]any{}
	if taskErr != nil {
		attrs["error"] = msg
		logger.Error("task failed", slog.String("error", msg))
	} else {
		logger.Info("task succeeded")
	}

	event := "task.succeeded"
	if status == store.StatusFailed {
		event = "task.failed"
	}
	task.Status = status
	q.report(writeCtx, event, task, attrs)
}

func (q *InProcess) report(ctx context.Context, name string, task store.Task, extra map[string]any) {
	attrs := map[string]any{
		"task_id":   task.ID,
		"task_type": task.Type,
		"status":    string(task.Status),
	}
	for k, v := range extra {
		attrs[k] = v
	}

	if err := telemetry.Report(ctx, q.reporter, name, attrs); err != nil && !errors.Is(err, context.Canceled) {
		q.logger.Warn("telemetry report failed", slog.String("event", name), slog.String("error", err.Error()))
	}
}
