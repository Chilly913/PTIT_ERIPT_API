// Command worker runs background jobs (replacement for Laravel's queue worker
// and scheduler). No jobs are registered yet.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logx.New(cfg.Log, cfg.App).With("process", "worker")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() {
		if err := a.Close(); err != nil {
			log.Error("close resources", "error", err.Error())
		}
	}()

	r := worker.NewRunner(log)
	// TODO(phase 5-7): register jobs, e.g.
	//   r.Add(worker.Job{Name: "flush-ai-request-logs", Interval: time.Minute, Fn: ...})
	return r.Run(ctx)
}
