package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hrtsegv/convoy/internal/config"
	"github.com/hrtsegv/convoy/internal/jobs"
	"github.com/hrtsegv/convoy/internal/postgres"
	"github.com/hrtsegv/convoy/internal/worker"
)

func main() {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-interrupts:
			signal.Stop(interrupts)
			cancel()
		case <-ctx.Done():
		}
	}()
	code := run(ctx, os.Stderr)
	signal.Stop(interrupts)
	cancel()
	os.Exit(code)
}

func run(ctx context.Context, output io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		slog.New(slog.NewJSONHandler(output, nil)).Error("invalid configuration", "error", err)
		return 1
	}
	logger := slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if ctx.Err() != nil {
		return 0
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, cfg.DBConnectTimeout)
	pool, err := postgres.Open(startupCtx, cfg)
	cancelStartup()
	if err != nil {
		if ctx.Err() != nil {
			logger.Info("startup cancelled")
			return 0
		}
		var parseErr *pgconn.ParseConfigError
		if errors.As(err, &parseErr) {
			logger.Error("invalid PostgreSQL connection configuration")
		} else {
			logger.Error("database startup failed")
		}
		return 1
	}
	defer pool.Close()
	logger.Info("worker started")
	if err := worker.Run(ctx, jobs.New(pool), map[string]worker.Handler{
		"noop":  func(ctx context.Context, _ jobs.Job) error { return ctx.Err() },
		"sleep": sleepHandler,
	}, cfg.JobQueryTimeout, logger); err != nil && !(ctx.Err() != nil && errors.Is(err, ctx.Err())) {
		return 1
	}
	logger.Info("worker stopped")
	return 0
}

func sleepHandler(ctx context.Context, job jobs.Job) error {
	var payload struct {
		Duration string `json:"duration"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return errors.New("sleep payload must contain a duration")
	}
	duration, err := time.ParseDuration(payload.Duration)
	if err != nil || duration <= 0 {
		return errors.New("sleep duration must be positive")
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
