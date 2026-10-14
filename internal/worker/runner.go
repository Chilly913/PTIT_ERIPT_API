// Package worker runs periodic background jobs (Laravel scheduler/queue
// replacement) with panic isolation and graceful shutdown.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// Job is a periodic task.
type Job struct {
	Name       string
	Interval   time.Duration
	RunOnStart bool          // run once immediately instead of waiting one interval
	Timeout    time.Duration // per-run timeout; 0 = Interval
	Fn         func(ctx context.Context) error
}

// Runner schedules jobs on tickers. Runs of the same job never overlap.
type Runner struct {
	log  *slog.Logger
	jobs []Job
}

// NewRunner creates an empty runner.
func NewRunner(log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{log: log}
}

// Add registers a job. It panics on invalid definitions (programming error).
func (r *Runner) Add(j Job) {
	if j.Name == "" || j.Interval <= 0 || j.Fn == nil {
		panic(fmt.Sprintf("worker: invalid job %+v", j))
	}
	r.jobs = append(r.jobs, j)
}

// Jobs returns the registered job names.
func (r *Runner) Jobs() []string {
	names := make([]string, len(r.jobs))
	for i, j := range r.jobs {
		names[i] = j.Name
	}
	return names
}

// Run blocks until ctx is cancelled, then waits for in-flight runs to finish
// (their contexts are cancelled too).
func (r *Runner) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, j := range r.jobs {
		wg.Add(1)
		go func(j Job) {
			defer wg.Done()
			r.loop(ctx, j)
		}(j)
	}
	r.log.Info("worker started", "jobs", r.Jobs())
	<-ctx.Done()
	wg.Wait()
	r.log.Info("worker stopped")
	return nil
}

func (r *Runner) loop(ctx context.Context, j Job) {
	if j.RunOnStart {
		r.runOnce(ctx, j)
	}
	t := time.NewTicker(j.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.runOnce(ctx, j)
		}
	}
}

func (r *Runner) runOnce(ctx context.Context, j Job) {
	if ctx.Err() != nil {
		return
	}
	timeout := j.Timeout
	if timeout <= 0 {
		timeout = j.Interval
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	log := r.log.With("job", j.Name)
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("job panicked", "panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
		}
	}()
	if err := j.Fn(runCtx); err != nil {
		log.Error("job failed", "error", err.Error(), "elapsed_ms", time.Since(start).Milliseconds())
		return
	}
	log.Debug("job done", "elapsed_ms", time.Since(start).Milliseconds())
}
