//go:build integration

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hrtsegv/convoy/internal/config"
	"github.com/hrtsegv/convoy/internal/postgres"
)

func TestReadinessWithPostgreSQL(t *testing.T) {
	dsn := os.Getenv("CONVOY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("CONVOY_TEST_DATABASE_URL is required for integration tests")
	}
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStartup()
	pool, err := postgres.Open(startupCtx, config.Config{DatabaseURL: dsn, DBMaxConns: 1})
	if err != nil {
		t.Fatal("could not initialize readiness integration database")
	}
	defer pool.Close()
	cancelStartup()
	lifecycle, stop := context.WithCancel(context.Background())
	defer stop()
	httpServer := httptest.NewServer(Handler(lifecycle, pool, config.Config{DBQueryTimeout: 50 * time.Millisecond}, testLogger()))
	defer httpServer.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	check := func(path string, want int) {
		t.Helper()
		response, err := client.Get(httpServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("%s: status=%d; want %d", path, response.StatusCode, want)
		}
	}
	check("/healthz", 200)
	check("/readyz", 200)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal("could not reserve the only pool connection")
	}
	defer held.Release()
	check("/readyz", 503)
	check("/healthz", 200)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	recorder := httptest.NewRecorder()
	Handler(lifecycle, pool, config.Config{DBQueryTimeout: time.Hour}, testLogger()).ServeHTTP(recorder,
		httptest.NewRequestWithContext(requestCtx, "GET", "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("cancelled readiness request: status=%d; want 503", recorder.Code)
	}

	held.Release()
	check("/readyz", 200)
	stop()
	check("/readyz", 503)
	check("/healthz", 200)
	t.Log("readiness uses its own request deadline, recovers when the pool is available, and rejects shutdown")
}
