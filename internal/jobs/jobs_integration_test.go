//go:build integration

package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hrtsegv/convoy/internal/config"
	"github.com/hrtsegv/convoy/internal/postgres"
	"github.com/hrtsegv/convoy/internal/testdb"
)

func TestEnqueueAndReadCommittedJob(t *testing.T) {
	pool := integrationPool(t)
	store := New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload := json.RawMessage(`{"message":"persisted","number":9007199254740993,"nested":[true,null]}`)
	job, err := store.Enqueue(ctx, "echo", payload)
	if err != nil {
		t.Fatal("could not enqueue a valid job")
	}
	if job.ID == "" || job.Queue != "default" || job.State != "available" || job.Kind != "echo" || job.CreatedAt.IsZero() {
		t.Fatal("enqueue did not return the persisted defaults")
	}
	read, err := store.Get(ctx, strings.ToUpper(job.ID))
	if err != nil || read.ID != job.ID || !read.CreatedAt.Equal(job.CreatedAt) {
		t.Fatal("could not read the committed job by ID")
	}
	if !strings.Contains(string(read.Payload), "9007199254740993") {
		t.Fatal("payload lost integer precision")
	}
	duplicate, err := store.Enqueue(ctx, "echo", payload)
	if err != nil || duplicate.ID == job.ID {
		t.Fatal("repeating enqueue must create a separate job without idempotency")
	}
	if _, err := store.Get(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing job must return ErrNotFound")
	}
	for _, value := range []string{`null`, `[]`, `"hello"`, `42`} {
		if _, err := store.Enqueue(ctx, "echo", json.RawMessage(value)); err != nil {
			t.Fatal("valid JSON scalar, array, or null was rejected")
		}
	}
}

func TestEnqueueRejectsUnsupportedJSONBAndCancellation(t *testing.T) {
	store := New(integrationPool(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, payload := range []string{`{"value":"\u0000"}`, `1e1000000`} {
		job, err := store.Enqueue(ctx, "echo", json.RawMessage(payload))
		var pgErr *pgconn.PgError
		if !errors.Is(err, ErrInvalidPayload) || !errors.As(err, &pgErr) || job.ID != "" {
			t.Fatal("JSONB rejection must preserve the database cause and return no job")
		}
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	job, err := store.Enqueue(cancelled, "echo", json.RawMessage(`{}`))
	if !errors.Is(err, context.Canceled) || job.ID != "" {
		t.Fatal("enqueue must respect caller cancellation and return no job")
	}
}

func TestJobSchemaConstraints(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, test := range []struct {
		sql  string
		code string
	}{
		{sql: `INSERT INTO jobs (kind, payload) VALUES ('', '{}')`, code: "23514"},
		{sql: `INSERT INTO jobs (kind, payload) VALUES (NULL, '{}')`, code: "23502"},
		{sql: `INSERT INTO jobs (kind, payload) VALUES ('echo', NULL)`, code: "23502"},
		{sql: `INSERT INTO jobs (kind, payload, state) VALUES ('echo', '{}', 'invalid')`, code: "23514"},
		{sql: `INSERT INTO jobs (kind, payload, state) VALUES ('echo', '{}', 'succeeded')`, code: "23514"},
		{sql: `INSERT INTO jobs (kind, payload, completed_at) VALUES ('echo', '{}', now())`, code: "23514"},
		{sql: `INSERT INTO jobs (kind, payload, queue) VALUES ('echo', '{}', '')`, code: "23514"},
	} {
		_, err := pool.Exec(ctx, test.sql)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != test.code {
			t.Fatal("database did not enforce the expected job constraint")
		}
	}
}

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := testdb.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, config.Config{DatabaseURL: dsn, DBMaxConns: 2})
	if err != nil {
		t.Fatal("could not open job integration pool")
	}
	t.Cleanup(pool.Close)
	return pool
}
