//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestMigrationCommand(t *testing.T) {
	dsn := os.Getenv("CONVOY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("CONVOY_TEST_DATABASE_URL is required for integration tests")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("migration integration tests require a PostgreSQL connection URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	observer, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("could not connect to migration integration database")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := observer.Close(ctx); err != nil {
			t.Error("could not close migration test connection")
		}
	})
	schema := fmt.Sprintf("convoy_cli_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := observer.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("could not create migration command test schema")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := observer.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("could not drop migration command test schema")
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	t.Setenv("CONVOY_DATABASE_URL", parsed.String())

	check := func(command string, wantCode int, wantMessage string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := run(ctx, []string{command}, &stdout, &stderr); code != wantCode {
			t.Fatalf("%s exit=%d; want %d", command, code, wantCode)
		}
		output := stdout.String()
		if wantCode != 0 {
			output = stderr.String()
			if stdout.Len() != 0 {
				t.Fatal("failed command printed success output")
			}
		} else if stderr.Len() != 0 {
			t.Fatal("successful command printed an error")
		}
		if !strings.Contains(output, wantMessage) {
			t.Fatalf("%s output=%q; want %q", command, output, wantMessage)
		}
	}
	check("version", 0, "version=none dirty=false")
	check("up", 0, "migrations applied")
	check("version", 0, "version=1 dirty=false")
	check("up", 0, "no pending migrations")
	if _, err := observer.Exec(ctx, "UPDATE "+pgx.Identifier{schema, "schema_migrations"}.Sanitize()+" SET version = 2, dirty = true"); err != nil {
		t.Fatal("could not prepare dirty migration metadata")
	}
	check("version", 0, "version=2 dirty=true")
	check("up", 1, "version 2 is dirty")
	check("version", 0, "version=2 dirty=true")
}
