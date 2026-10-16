package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hrtsegv/convoy/internal/config"
	"github.com/hrtsegv/convoy/internal/postgres"
	"github.com/hrtsegv/convoy/internal/server"
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
			logger.Error("database startup failed", "error", err)
		}
		return 1
	}
	defer pool.Close()

	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		logger.Error("HTTP listen failed", "error", err)
		return 1
	}
	if err := server.Serve(ctx, listener, server.Handler(ctx, pool, cfg.DBQueryTimeout), cfg.ShutdownTimeout, logger); err != nil {
		logger.Error("HTTP server failed", "error", err)
		return 1
	}
	logger.Info("server stopped")
	return 0
}
