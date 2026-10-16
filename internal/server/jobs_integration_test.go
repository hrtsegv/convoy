//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hrtsegv/convoy/internal/config"
	"github.com/hrtsegv/convoy/internal/jobs"
	"github.com/hrtsegv/convoy/internal/postgres"
	"github.com/hrtsegv/convoy/internal/testdb"
)

func TestJobsHTTPEnqueueAndRead(t *testing.T) {
	pool := jobHTTPPool(t)
	handler := Handler(context.Background(), pool, config.Config{JobQueryTimeout: time.Second}, testLogger())
	created := enqueueHTTP(handler, `{"kind":"echo","payload":{"message":"hello"}}`)
	if created.Code != http.StatusCreated || created.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("enqueue: status=%d; want 201 JSON", created.Code)
	}
	var job jobs.Job
	if err := json.Unmarshal(created.Body.Bytes(), &job); err != nil {
		t.Fatal("invalid job response")
	}
	if job.Queue != "default" || job.State != "available" || job.ID == "" || job.CreatedAt.IsZero() {
		t.Fatal("incorrect persisted job defaults")
	}
	location := "/v1/jobs/" + job.ID
	if created.Header().Get("Location") != location {
		t.Fatal("missing job Location header")
	}
	read := httptest.NewRecorder()
	handler.ServeHTTP(read, httptest.NewRequest("GET", location, nil))
	if read.Code != http.StatusOK || read.Body.String() != created.Body.String() {
		t.Fatal("GET did not return the committed job")
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest("GET", "/v1/jobs/00000000-0000-0000-0000-000000000000", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing job: status=%d; want 404", missing.Code)
	}
	for _, payload := range []string{`{"value":"\u0000"}`, `1e1000000`} {
		response := enqueueHTTP(handler, `{"kind":"echo","payload":`+payload+`}`)
		if response.Code != http.StatusBadRequest {
			t.Fatal("unsupported JSONB value must return 400")
		}
	}
}

func TestEnqueueWaitsForCommitAndRedactsDatabaseErrors(t *testing.T) {
	pool := jobHTTPPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx, `
		CREATE FUNCTION reject_job_commit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'private-payload-marker' USING ERRCODE = '23514', DETAIL = NEW.payload::text;
		END $$;
		CREATE CONSTRAINT TRIGGER reject_job_commit AFTER INSERT ON jobs
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_job_commit();`)
	if err != nil {
		t.Fatal("could not install deferred commit failure")
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := Handler(context.Background(), pool, config.Config{JobQueryTimeout: time.Second}, logger)
	response := enqueueHTTP(handler, `{"kind":"echo","payload":{"secret":"private-payload-marker"}}`)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Location") != "" {
		t.Fatal("commit failure must not return job creation success")
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM jobs").Scan(&count); err != nil || count != 0 {
		t.Fatal("deferred failure should roll back the inserted row")
	}
	if strings.Contains(response.Body.String()+logs.String(), "private-payload-marker") {
		t.Fatal("API or logs exposed a database error message or payload")
	}
	if !strings.Contains(logs.String(), `"sqlstate":"23514"`) {
		t.Fatal("expected a safe SQLSTATE diagnostic")
	}
	t.Log("deferred commit failure returned 503, persisted no row, and exposed no payload in the response or logs")
}

func TestJobHTTPQueryDeadline(t *testing.T) {
	pool := jobHTTPPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal("could not hold the only pool connection")
	}
	defer held.Release()
	handler := Handler(context.Background(), pool, config.Config{JobQueryTimeout: 25 * time.Millisecond}, testLogger())
	response := enqueueHTTP(handler, `{"kind":"echo","payload":{}}`)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "outcome may be unknown") {
		t.Fatal("query deadline must return an uncertain-outcome error, not creation success")
	}
	held.Release()
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM jobs").Scan(&count); err != nil || count != 0 {
		t.Fatal("no insert should occur when acquiring a connection times out")
	}
}

func jobHTTPPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := testdb.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, config.Config{DatabaseURL: dsn, DBMaxConns: 1})
	if err != nil {
		t.Fatal("could not open job HTTP test database")
	}
	t.Cleanup(pool.Close)
	return pool
}

func enqueueHTTP(handler http.Handler, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", "/v1/jobs", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
