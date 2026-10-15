//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hrtsegv/convoy/internal/config"
	"github.com/hrtsegv/convoy/internal/postgres"
)

func TestOpenIntegrationStartupContextLifetime(t *testing.T) {
	databaseURL := os.Getenv("CONVOY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("CONVOY_TEST_DATABASE_URL is required for integration tests")
	}

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStartup()
	pool, err := postgres.Open(startupCtx, config.Config{
		DatabaseURL: databaseURL,
		DBMaxConns:  2,
		// This setting must be ignored: the caller's context controls startup.
		DBConnectTimeout: -time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()
	if pool.Config().MaxConns != 2 {
		t.Fatal("pool did not use the configured connection limit")
	}
	t.Log("Open succeeded after its connectivity check")

	cancelStartup()
	if !errors.Is(startupCtx.Err(), context.Canceled) {
		t.Fatalf("expected cancelled startup context, got %v", startupCtx.Err())
	}
	t.Log("startup context cancelled")

	queryCtx, cancelQuery := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelQuery()
	if err := pool.Ping(queryCtx); err != nil {
		t.Fatalf("ping with a fresh context after startup cancellation: %v", err)
	}
	t.Log("Ping with a fresh context succeeded; the caller still owns an open pool")
}
