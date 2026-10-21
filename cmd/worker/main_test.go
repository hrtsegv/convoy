package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hrtsegv/convoy/internal/jobs"
)

func TestRunRejectsConfigurationAndRedactsParseErrors(t *testing.T) {
	for _, dsn := range []string{"", "postgres://private-user:private-password@localhost:private-port/db", "user=private-user password='private-password"} {
		setTestEnv(t)
		t.Setenv("CONVOY_DATABASE_URL", dsn)
		var output bytes.Buffer
		if code := run(context.Background(), &output); code != 1 {
			t.Fatalf("exit=%d; want 1", code)
		}
		if strings.Contains(output.String(), "private-") {
			t.Fatal("worker startup exposed connection-string contents")
		}
		if dsn != "" && !strings.Contains(output.String(), "invalid PostgreSQL connection configuration") {
			t.Fatal("expected static connection-configuration diagnostic")
		}
	}
}

func TestRunCancelledBeforeStartup(t *testing.T) {
	setTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	if code := run(ctx, &output); code != 0 || output.Len() != 0 {
		t.Fatal("cancelled worker must not connect to the database")
	}
}

func TestSleepHandlerValidationAndCancellation(t *testing.T) {
	for _, payload := range []string{`{`, `{}`, `{"duration":"bad"}`, `{"duration":"0s"}`, `{"duration":"-1s"}`} {
		if err := sleepHandler(context.Background(), jobs.Job{Payload: []byte(payload)}); err == nil {
			t.Fatal("invalid sleep duration was accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepHandler(ctx, jobs.Job{Payload: []byte(`{"duration":"1h"}`)}); !errors.Is(err, context.Canceled) {
		t.Fatal("sleep handler must respect cancellation")
	}
}

func TestRunCancelsStartupAndClosesConnection(t *testing.T) {
	setTestEnv(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONVOY_DATABASE_URL", "postgres://local:local@"+listener.Addr().String()+"/test?sslmode=disable")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, &output) }()
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	cancel()
	select {
	case code := <-done:
		if code != 0 || !strings.Contains(output.String(), "startup cancelled") {
			t.Fatal("worker startup cancellation must stop normally")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker ignored startup cancellation")
	}
	if err := connection.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, connection); err != nil {
		t.Fatal("cancelled startup did not close its connection")
	}
}

func setTestEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"CONVOY_HTTP_ADDR", "CONVOY_LOG_LEVEL", "CONVOY_DB_MAX_CONNS",
		"CONVOY_DB_CONNECT_TIMEOUT", "CONVOY_DB_QUERY_TIMEOUT", "CONVOY_JOB_QUERY_TIMEOUT", "CONVOY_SHUTDOWN_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("CONVOY_DATABASE_URL", "postgres://local:local@127.0.0.1:1/test")
}
