package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Handler(lifecycle context.Context, pool *pgxpool.Pool, queryTimeout time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if lifecycle.Err() != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ready")
	})
	return mux
}

// Serve owns the listener and HTTP connections. It drains active requests on
// cancellation, then force-closes connections if the shutdown deadline expires.
// Database resources remain owned by the caller until Serve returns.
func Serve(ctx context.Context, listener net.Listener, handler http.Handler, shutdownTimeout time.Duration, logger *slog.Logger) error {
	defer listener.Close()
	if ctx.Err() != nil {
		return nil
	}
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	defer httpServer.Close()
	result := make(chan error, 1)
	go func() { result <- httpServer.Serve(listener) }()
	logger.Info("HTTP server listening", "address", listener.Addr().String())
	select {
	case err := <-result:
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down HTTP server")
	// A fresh context gives active requests a drain budget after cancellation.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown HTTP: %w", err)
	}
	if err := <-result; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}
