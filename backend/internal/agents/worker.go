package agents

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Worker drains the job queue. Jobs are claimed from the database rather than
// from an in-process channel, so a job survives a restart and a second API
// instance can pick up work this one dropped.
type Worker struct {
	Store      Store
	Pipeline   Pipeline
	Count      int
	JobTimeout time.Duration
	Lease      time.Duration
	Poll       time.Duration

	wake chan struct{}
	once sync.Once
}

func (w *Worker) settings() (count int, jobTimeout, lease, poll time.Duration) {
	count, jobTimeout, lease, poll = w.Count, w.JobTimeout, w.Lease, w.Poll
	if count <= 0 {
		count = 2
	}
	if jobTimeout <= 0 {
		jobTimeout = 25 * time.Minute
	}
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	if poll <= 0 {
		poll = 3 * time.Second
	}
	return count, jobTimeout, lease, poll
}

func (w *Worker) init() {
	w.once.Do(func() { w.wake = make(chan struct{}, 1) })
}

// Wake tells an idle worker that a job was just queued, so the first stage
// starts immediately instead of after the poll interval.
func (w *Worker) Wake() {
	w.init()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Start launches the workers and returns a function that waits for them to
// finish the job in hand.
func (w *Worker) Start(ctx context.Context) func() {
	w.init()
	count, jobTimeout, lease, poll := w.settings()
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			w.loop(ctx, index, jobTimeout, lease, poll)
		}(i)
	}
	return wg.Wait
}

func (w *Worker) loop(ctx context.Context, index int, jobTimeout, lease, poll time.Duration) {
	timer := time.NewTimer(poll)
	defer timer.Stop()
	for {
		worked := w.claimAndRun(ctx, jobTimeout, lease)
		if ctx.Err() != nil {
			return
		}
		if worked {
			// Another job may be waiting; do not sleep between them.
			continue
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(poll)
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-timer.C:
		}
	}
}

func (w *Worker) claimAndRun(ctx context.Context, jobTimeout, lease time.Duration) bool {
	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	job, found, err := w.Store.ClaimJob(claimCtx, lease)
	cancel()
	if err != nil {
		slog.Error("cannot claim pipeline job", "error", err)
		return false
	}
	if !found {
		return false
	}
	// The job context is detached from the request that queued it and bounded
	// by its own timeout, so a disconnected client does not cancel generation.
	// Process shutdown does cancel it; the lease then expires and another
	// worker picks the job up where the queue left it.
	jobCtx, cancelJob := context.WithTimeout(ctx, jobTimeout)
	defer cancelJob()
	slog.Info("pipeline job started", "job", job.ID, "kind", job.Kind, "mode", job.Mode)
	started := time.Now()
	if err := w.Pipeline.Run(jobCtx, job); err != nil {
		slog.Error("pipeline job could not be recorded", "job", job.ID, "error", err)
	}
	slog.Info("pipeline job finished", "job", job.ID, "duration", time.Since(started).String())
	return true
}
