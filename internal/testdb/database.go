//go:build integration

package testdb

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hrtsegv/convoy/internal/migrate"
)

// New creates and migrates an isolated schema, then drops it during cleanup.
func New(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("CONVOY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("CONVOY_TEST_DATABASE_URL is required for integration tests")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("integration tests require a PostgreSQL connection URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observer, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("could not connect to the integration database")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := observer.Close(ctx); err != nil {
			t.Error("could not close integration database observer")
		}
	})
	schema := "convoy_test_" + strings.ToLower(rand.Text())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := observer.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("could not create isolated test schema")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := observer.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("could not drop isolated test schema")
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	runner, err := migrate.Open(ctx, parsed.String())
	if err != nil {
		t.Fatal("could not initialize isolated test migrations")
	}
	err = runner.Up()
	sourceErr, databaseErr := runner.Close()
	if err != nil || sourceErr != nil || databaseErr != nil {
		t.Fatal("could not migrate and close the isolated test runner")
	}
	return parsed.String()
}
