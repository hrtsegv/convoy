package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	gomigrate "github.com/golang-migrate/migrate/v4"
)

func TestRunUsage(t *testing.T) {
	t.Setenv("CONVOY_DATABASE_URL", "")
	for _, test := range []struct {
		args []string
		code int
	}{
		{args: []string{"--help"}, code: 0},
		{args: []string{"-h"}, code: 0},
		{args: []string{"help"}, code: 0},
		{args: nil, code: 2},
		{args: []string{"down"}, code: 2},
		{args: []string{"force", "1"}, code: 2},
		{args: []string{"up", "extra"}, code: 2},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), test.args, &stdout, &stderr); code != test.code {
			t.Fatalf("args=%v: exit=%d; want %d", test.args, code, test.code)
		}
		if !strings.Contains(stdout.String()+stderr.String(), "usage: migrate <up|version>") {
			t.Fatal("expected usage without connecting to PostgreSQL")
		}
	}
}

func TestRunMissingDatabase(t *testing.T) {
	t.Setenv("CONVOY_DATABASE_URL", "")
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"up"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit=%d; want 1", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "CONVOY_DATABASE_URL is required") {
		t.Fatal("expected a configuration error without success output")
	}
}

func TestRunDoesNotLogConnectionStringParseErrors(t *testing.T) {
	for _, dsn := range []string{
		"postgres://private-user:private-password@localhost/db?connect_timeout=private-timeout",
		"host=localhost user=private-user password=private-password connect_timeout=private-timeout",
		"postgres://private-user:private-password@localhost:private-port/db",
	} {
		t.Setenv("CONVOY_DATABASE_URL", dsn)
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"up"}, &stdout, &stderr); code != 1 {
			t.Fatalf("exit=%d; want 1", code)
		}
		if stdout.Len() != 0 || stderr.String() != "invalid PostgreSQL connection configuration\n" {
			t.Fatal("parse failure must emit only the static configuration-error message")
		}
	}
}

func TestRunCancelledBeforeStartup(t *testing.T) {
	t.Setenv("CONVOY_DATABASE_URL", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := run(ctx, []string{"up"}, &stdout, &stderr); code != 130 {
		t.Fatalf("exit=%d; want 130", code)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatal("expected cancellation before database initialization")
	}
}

func TestReportDirtyVersion(t *testing.T) {
	var stderr bytes.Buffer
	reportError(&stderr, gomigrate.ErrDirty{Version: 2})
	if got := stderr.String(); !strings.Contains(got, "version 2 is dirty") || !strings.Contains(got, "recover manually") {
		t.Fatalf("unexpected dirty-state guidance: %s", got)
	}
}
