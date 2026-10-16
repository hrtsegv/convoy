package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProbeRoutes(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://local:local@127.0.0.1:1/test?sslmode=disable")
	if err != nil {
		t.Fatal("could not create lazy test pool")
	}
	pool.Close()
	handler := Handler(context.Background(), pool, time.Second)
	for _, test := range []struct {
		method string
		path   string
		status int
		body   string
	}{
		{method: "GET", path: "/healthz", status: 200, body: "ok\n"},
		{method: "GET", path: "/readyz", status: 503, body: "not ready\n"},
		{method: "POST", path: "/healthz", status: 405},
		{method: "POST", path: "/readyz", status: 405},
		{method: "GET", path: "/unknown", status: 404},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
		if recorder.Code != test.status {
			t.Fatalf("%s %s: status=%d; want %d", test.method, test.path, recorder.Code, test.status)
		}
		if test.body != "" && recorder.Body.String() != test.body {
			t.Fatalf("%s response=%q; want %q", test.path, recorder.Body.String(), test.body)
		}
	}
}

func TestReadinessRejectsShutdownBeforeUsingDatabase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handler := Handler(ctx, nil, time.Second)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d; want 503", recorder.Code)
	}
}

func TestServeDrainsActiveRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener := testListener(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	requestCancelled := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			close(requestCancelled)
		}
	})
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, handler, time.Second, testLogger()) }()
	response := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		result, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			result.Body.Close()
			if result.StatusCode != http.StatusOK {
				err = errors.New("active request did not complete successfully")
			}
		}
		response <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not enter the handler")
	}
	cancel()
	waitForListenerClose(t, listener.Addr().String())
	select {
	case <-requestCancelled:
		t.Fatal("shutdown cancelled the active request before draining")
	case err := <-done:
		t.Fatalf("Serve returned before the active request finished: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-response:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active request did not finish")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not finish after draining")
	}
}

func TestServeForceClosesAfterShutdownDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener := testListener(t)
	entered := make(chan struct{})
	finished := make(chan struct{})
	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(finished)
	})
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, handler, 50*time.Millisecond, testLogger()) }()
	client := &http.Client{Timeout: 5 * time.Second}
	go func() {
		response, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not enter the handler")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected a wrapped shutdown deadline error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve exceeded its shutdown budget")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("force-close did not cancel the active request")
	}
}

func TestServeReportsListenerFailure(t *testing.T) {
	listener := testListener(t)
	listener.Close()
	err := Serve(context.Background(), listener, http.NotFoundHandler(), time.Second, testLogger())
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("expected a wrapped closed-listener error, got %v", err)
	}
}

func testListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func waitForListenerClose(t *testing.T, address string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		if err != nil {
			if ctx.Err() != nil {
				t.Fatal("HTTP listener did not close during shutdown")
			}
			return
		}
		conn.Close()
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("HTTP listener did not close during shutdown")
		}
	}
}
