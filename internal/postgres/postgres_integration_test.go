//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

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
		var parseErr *pgconn.ParseConfigError
		if errors.As(err, &parseErr) {
			t.Fatal("open pool: invalid PostgreSQL connection configuration")
		}
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

func TestOpenIntegrationParseFailureLogging(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{
			name: "URI credentials with invalid port",
			dsn:  "postgres://dsn_test_user:dsn_test_password@localhost:invalid/test",
		},
		{
			name: "unterminated keyword password",
			dsn:  "user=dsn_test_user password='dsn_test_password",
		},
		{
			name: "secret repeated in parser cause",
			dsn:  "postgres://dsn_test_user@localhost/test?password=dsn_test_password&pool_max_conns=dsn_test_password",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CONVOY_TEST_DATABASE_URL", test.dsn)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Capture the real fatal-test output without making this test fail too.
			command := exec.CommandContext(ctx, os.Args[0],
				"-test.run=^TestOpenIntegrationStartupContextLifetime$", "-test.v")
			output, err := command.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatal("expected the child test to fail during startup")
			}
			if !bytes.Contains(output, []byte("open pool: invalid PostgreSQL connection configuration")) {
				t.Fatal("startup failure did not emit the static configuration diagnostic")
			}
			for _, sensitive := range []string{"dsn_test_user", "dsn_test_password", test.dsn} {
				if bytes.Contains(output, []byte(sensitive)) {
					t.Fatal("startup failure exposed sensitive connection details")
				}
			}
		})
	}
}
