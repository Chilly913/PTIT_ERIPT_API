// Command worker runs background jobs (replacement for Laravel's queue worker
// and scheduler). No jobs are registered yet.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
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
	tokens := auth.NewTokenService(a.DB, auth.TokenOptions{})
	r.Add(worker.Job{Name: "purge-expired-tokens", Interval: time.Hour, Fn: func(ctx context.Context) error {
		n, err := tokens.PurgeExpired(ctx, time.Now().UTC().Add(-24*time.Hour))
		if err == nil && n > 0 {
			log.Info("purged expired tokens", "count", n)
		}
		return err
	}})
	// TODO(phase 5-7): register jobs, e.g.
	//   r.Add(worker.Job{Name: "flush-ai-request-logs", Interval: time.Minute, Fn: ...})
	return r.Run(ctx)
}
