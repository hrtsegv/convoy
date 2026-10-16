//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestServerLifecycleIntegration(t *testing.T) {
	dsn := os.Getenv("CONVOY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("CONVOY_TEST_DATABASE_URL is required for integration tests")
	}
	setTestEnv(t)
	t.Setenv("CONVOY_DATABASE_URL", dsn)
	t.Setenv("CONVOY_HTTP_ADDR", "127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	address := make(chan string, 1)
	done := make(chan struct{})
	var exitCode int
	go func() {
		exitCode = run(ctx, startupLogWriter{address: address})
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop after cancellation")
		}
	})
	var baseURL string
	select {
	case addr := <-address:
		baseURL = "http://" + addr
	case <-done:
		t.Fatalf("server exited during startup with status %d", exitCode)
	case <-ctx.Done():
		t.Fatal("server startup exceeded the test deadline")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	for _, path := range []string{"/healthz", "/readyz"} {
		response, err := client.Get(baseURL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d; want 200", path, response.StatusCode)
		}
	}
	cancel()
	select {
	case <-done:
		if exitCode != 0 {
			t.Fatalf("server shutdown exit=%d; want 0", exitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
	t.Log("application startup, PostgreSQL readiness, and graceful shutdown succeeded")
}

type startupLogWriter struct {
	address chan<- string
}

func (w startupLogWriter) Write(data []byte) (int, error) {
	var entry struct {
		Message string `json:"msg"`
		Address string `json:"address"`
	}
	if json.Unmarshal(data, &entry) == nil && entry.Message == "HTTP server listening" {
		w.address <- entry.Address
	}
	return len(data), nil
}
