package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRunRejectsConfiguration(t *testing.T) {
	setTestEnv(t)
	t.Setenv("CONVOY_DATABASE_URL", "")
	var output bytes.Buffer
	if code := run(context.Background(), &output); code != 1 {
		t.Fatalf("exit=%d; want 1", code)
	}
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal("expected structured JSON logging")
	}
	if entry["level"] != "ERROR" || entry["msg"] != "invalid configuration" {
		t.Fatal("expected a structured configuration error")
	}
}

func TestRunDoesNotLogConnectionStringParseErrors(t *testing.T) {
	for _, dsn := range []string{
		"postgres://private-user:private-password@localhost:private-port/db",
		"user=private-user password='private-password",
		"postgres://private-user@localhost/db?password=private-password&pool_max_conns=private-password",
	} {
		setTestEnv(t)
		t.Setenv("CONVOY_DATABASE_URL", dsn)
		var output bytes.Buffer
		if code := run(context.Background(), &output); code != 1 {
			t.Fatalf("exit=%d; want 1", code)
		}
		var entry map[string]any
		if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
			t.Fatal("expected one structured error message")
		}
		if entry["msg"] != "invalid PostgreSQL connection configuration" {
			t.Fatal("expected the static connection configuration diagnostic")
		}
		if _, exists := entry["error"]; exists {
			t.Fatal("parse error causes must not be logged")
		}
		for _, sensitive := range []string{"private-user", "private-password", "private-port", dsn} {
			if strings.Contains(output.String(), sensitive) {
				t.Fatal("startup log exposed sensitive connection information")
			}
		}
	}
}

func TestRunCancelledBeforeStartup(t *testing.T) {
	setTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	if code := run(ctx, &output); code != 0 {
		t.Fatalf("exit=%d; want 0", code)
	}
	if output.Len() != 0 {
		t.Fatal("cancelled startup should not attempt a database connection")
	}
}

func TestRunAppliesStartupTimeout(t *testing.T) {
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
	t.Setenv("CONVOY_DB_CONNECT_TIMEOUT", "250ms")
	var output bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(context.Background(), &output) }()
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	select {
	case code := <-done:
		if code != 1 || !strings.Contains(output.String(), "context deadline exceeded") {
			t.Fatal("expected startup to fail at the configured deadline")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup ignored the configured timeout")
	}
	if err := connection.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, connection); err != nil {
		t.Fatal("failed startup did not close its connection")
	}
}

func setTestEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"CONVOY_HTTP_ADDR", "CONVOY_LOG_LEVEL", "CONVOY_DB_MAX_CONNS",
		"CONVOY_DB_CONNECT_TIMEOUT", "CONVOY_DB_QUERY_TIMEOUT", "CONVOY_SHUTDOWN_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("CONVOY_DATABASE_URL", "postgres://local:local@127.0.0.1:1/test")
}
