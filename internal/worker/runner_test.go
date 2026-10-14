package worker

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerRunsAndStops(t *testing.T) {
	r := NewRunner(slog.New(slog.NewTextHandler(io.Discard, nil)))
	var n, panics atomic.Int32
	r.Add(Job{Name: "count", Interval: 10 * time.Millisecond, RunOnStart: true, Fn: func(context.Context) error {
		n.Add(1)
		return nil
	}})
	r.Add(Job{Name: "panic", Interval: 10 * time.Millisecond, Fn: func(context.Context) error {
		panics.Add(1)
		panic("boom")
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = r.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop")
	}
	if n.Load() < 3 || panics.Load() < 2 {
		t.Fatalf("runs: count=%d panics=%d", n.Load(), panics.Load())
	}
}
